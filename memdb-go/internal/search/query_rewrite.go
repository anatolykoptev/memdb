package search

// query_rewrite.go — D4: retrieval-oriented query rewriting BEFORE embedding.
//
// An LLM rewrites the user's query into a retrieval-optimised form
// (third-person, absolute temporal, noun-phrase dense) so the embedded
// vector is closer to the stored-memory embeddings. Complements D10
// (post-retrieval answer extraction).
//
// Design invariants:
//   - Env-gated (MEMDB_QUERY_REWRITE=true, default off).
//   - Only the EMBEDDING uses the rewritten query. BM25, CE rerank, LLM
//     rerank, and D10 enhance all continue to use the original `p.Query`
//     (they want user intent, not the retrieval transform).
//   - Fall-back to original on any of: LLM error, confidence < 0.5, parse
//     failure, short query (<5 tokens), long query (>400 chars).
//   - Temperature = 0 for determinism.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	queryRewriteTimeout       = 8 * time.Second
	queryRewriteMaxTokens     = 80
	queryRewriteMinTokens     = 5
	queryRewriteMinConf       = 0.5
	queryRewriteMaxOrigLen    = 400 // characters; skip very long queries
	queryRewriteRespBodyLimit = int64(8 * 1024)
)

var queryRewriteSystemPrompt = `You are a retrieval-query-rewriter. Given a user's question, rewrite it to maximise vector-retrieval recall from a long-term memory database.

Rules:
- Convert first/second-person pronouns to explicit subjects when context makes it clear ("I said" → "User said"; "you told me" → "Assistant told user").
- Convert relative temporal references to absolute (e.g., "last week" → "the week before {now_date}"). If the rewriter has no reliable anchor, leave as-is.
- Preserve proper nouns verbatim — names, places, events.
- Output as a noun-phrase-dense question if possible.
- Keep the rewrite SHORT — target same or fewer tokens than input.
- If no meaningful rewrite improves recall, return input verbatim with confidence ≤ 0.5.

Return strict JSON: {"rewritten": string, "confidence": float between 0.0 and 1.0}`

// QueryRewriteConfig carries the LLM endpoint credentials. Mirrors
// LLMRerankConfig so the service can reuse existing plumbing.
type QueryRewriteConfig struct {
	APIURL string
	APIKey string
	Model  string
}

func queryRewriteEnabled() bool {
	return os.Getenv("MEMDB_QUERY_REWRITE") == "true"
}

// QueryRewriteResult is the observability payload returned by
// RewriteQueryForRetrieval. `Rewritten` is safe to embed directly: on any
// fallback path it is the original query verbatim.
type QueryRewriteResult struct {
	Original   string
	Rewritten  string
	Confidence float64
	Used       bool
	Err        error
}

// RewriteQueryForRetrieval rewrites `query` into a retrieval-optimised form
// via the configured LLM. Returns the original query on any fallback path
// (disabled, short, long, LLM error, low confidence, parse failure).
func RewriteQueryForRetrieval(ctx context.Context, query, nowISO string, cfg QueryRewriteConfig) QueryRewriteResult {
	result := QueryRewriteResult{Original: query, Rewritten: query}
	if !queryRewriteEnabled() || cfg.APIURL == "" {
		searchMx().D4Rewrite.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return result
	}
	if len(query) > queryRewriteMaxOrigLen {
		searchMx().D4Rewrite.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return result
	}
	if len(strings.Fields(query)) < queryRewriteMinTokens {
		searchMx().D4Rewrite.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return result
	}

	userMsg := fmt.Sprintf("Now: %s\n\nUser query: %s\n\nRespond with strict JSON only.", nowISO, query)

	rewritten, conf, err := callRewriteLLM(ctx, userMsg, cfg)
	if err != nil {
		searchMx().D4Rewrite.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "error")))
		result.Err = err
		return result
	}
	if strings.TrimSpace(rewritten) == "" || conf < queryRewriteMinConf {
		searchMx().D4Rewrite.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "low_confidence")))
		return result
	}
	searchMx().D4Rewrite.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "rewritten")))
	result.Rewritten = rewritten
	result.Confidence = conf
	result.Used = true
	return result
}

func callRewriteLLM(ctx context.Context, userMsg string, cfg QueryRewriteConfig) (string, float64, error) {
	var parsed struct {
		Rewritten  string  `json:"rewritten"`
		Confidence float64 `json:"confidence"`
	}
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	err := llm.ChatStructured(ctx, client, "d4_rewrite", []llm.Message{
		{Role: "system", Content: queryRewriteSystemPrompt},
		{Role: "user", Content: userMsg},
	}, &parsed,
		llm.WithMaxTokens(queryRewriteMaxTokens),
		llm.WithTimeout(queryRewriteTimeout),
		llm.WithRespBodyLimit(queryRewriteRespBodyLimit),
	)
	if err != nil {
		return "", 0, err
	}
	return parsed.Rewritten, parsed.Confidence, nil
}

// applyQueryRewrite is the pipeline hook called immediately before embedding.
// Returns (embedQuery, originalQuery). Only the embed call should use
// embedQuery — every other scorer (BM25, CE rerank, D10) should use the
// original for user-intent fidelity.
func applyQueryRewrite(ctx context.Context, logger *slog.Logger, query, nowISO string, cfg QueryRewriteConfig) (embedQuery, original string) {
	res := RewriteQueryForRetrieval(ctx, query, nowISO, cfg)
	if res.Err != nil && logger != nil {
		logger.Debug("query rewrite failed, using original", slog.Any("error", res.Err))
		return query, query
	}
	if res.Used && logger != nil {
		logger.Info("query rewrite applied",
			slog.String("original", res.Original),
			slog.String("rewritten", res.Rewritten),
			slog.Float64("confidence", res.Confidence),
		)
	}
	return res.Rewritten, query
}
