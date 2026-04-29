package search

// query_decompose.go — D7: Chain-of-Thought query decomposition.
//
// A multi-part user query ("X and Y", "X, also Z") is split by an LLM into
// up to 3 atomic retrieval-sub-questions. Each sub-question is embedded
// independently and its vector-search results are unioned with the primary
// pool by id (keeping max score). The rest of the pipeline (CE rerank,
// LLM rerank, D5 staged, D10 enhance) still runs on the unioned pool with
// the ORIGINAL query — preserving user intent for downstream scorers.
//
// Design invariants:
//   - Env-gated (MEMDB_SEARCH_COT=true, default off).
//   - Skipped for queries with fewer than cotMinQueryWords tokens.
//   - Capped at cotMaxSubqueries sub-questions.
//   - Graceful degrade: LLM error / parse failure / empty response → single
//     (original query only).
//   - Temperature 0.0 for determinism.

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	cotTimeout              = 8 * time.Second
	cotMaxTokens            = 200
	cotMaxSubqueries        = 3
	cotMinQueryWords        = 8 // don't decompose short atomic queries
	cotRespBodyLimit  int64 = 8 * 1024
)

var cotSystemPrompt = `You are a retrieval-query decomposer. Given a user's question, split it into atomic retrieval-questions if it contains multiple independent facts to look up.

Return JSON: {"questions": [string, ...]}

Rules:
- If the query is already atomic (a single fact-lookup), return it as a single-element list.
- Decompose only on genuine conjunctions: "X and Y", "X, what about Y", "X or Y".
- Do NOT decompose clauses that share a subject ("Caroline's age and job" is atomic about Caroline).
- Keep the original wording as much as possible for each atomic sub-question.
- Return at most 3 questions.`

// CoTConfig carries the LLM endpoint credentials. Mirrors QueryRewriteConfig
// so the service can reuse existing plumbing.
type CoTConfig struct {
	APIURL string
	APIKey string
	Model  string
}

func cotDecomposeEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_COT") == "true"
}

// DecomposeQuery returns a list of atomic sub-questions. If decomposition
// doesn't apply (env off, short query, LLM error, atomic), returns [query].
// The returned slice always has at least one element.
//
// Cache: results are stored in the process-global rewriteCache (keyed on
// query + model). On a cache hit the LLM is bypassed entirely; on miss the
// result is written back. When MEMDB_REWRITE_CACHE_SIZE=0 the cache is nil
// and every call always reaches the LLM — D7 functionality is NOT disabled.
func DecomposeQuery(ctx context.Context, logger *slog.Logger, query string, cfg CoTConfig) []string {
	original := []string{query}
	if !cotDecomposeEnabled() || cfg.APIURL == "" {
		searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return original
	}
	wordCount := len(strings.Fields(query))
	if wordCount < cotMinQueryWords {
		searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return original
	}

	// Cache lookup — bypass LLM on hit.
	cache := getRewriteCache()
	cacheKey := rewriteCacheKey("d7:"+query, cfg.Model)
	if cached, ok := cache.Get(cacheKey); ok {
		searchMx().RewriteCacheHit.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", "d7")))
		searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "decomposed")))
		return strings.Split(cached, "\x00")
	}
	searchMx().RewriteCacheMiss.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", "d7")))

	subs, err := callCoTLLM(ctx, query, cfg)
	if err != nil {
		searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "error")))
		if logger != nil {
			logger.Debug("cot decompose failed, using original", slog.Any("error", err))
		}
		return original
	}
	// Filter blank entries.
	cleaned := make([]string, 0, len(subs))
	for _, s := range subs {
		if t := strings.TrimSpace(s); t != "" {
			cleaned = append(cleaned, t)
		}
	}
	if len(cleaned) == 0 {
		searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "atomic")))
		return original
	}
	// LLM returned a single item equal to the original → atomic query.
	if len(cleaned) == 1 && strings.EqualFold(cleaned[0], strings.TrimSpace(query)) {
		searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "atomic")))
		return original
	}
	if len(cleaned) > cotMaxSubqueries {
		cleaned = cleaned[:cotMaxSubqueries]
	}
	searchMx().D7CoT.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", "decomposed"),
		attribute.String("subq_count", strconv.Itoa(len(cleaned))),
	))
	if logger != nil {
		logger.Info("cot decompose applied",
			slog.String("original", query),
			slog.Any("sub_questions", cleaned),
		)
	}
	// Store NUL-joined slice in cache for future identical queries.
	cache.Set(cacheKey, strings.Join(cleaned, "\x00"))
	return cleaned
}

// unionVectorResults unions `extra` into `primary`, keeping the higher score
// when the same id appears in both. Used to merge per-subquery vector-search
// results into the primary pool in the D7 CoT pipeline.
func unionVectorResults(primary, extra []db.VectorSearchResult) []db.VectorSearchResult {
	if len(extra) == 0 {
		return primary
	}
	// Build index of primary by id.
	idx := make(map[string]int, len(primary))
	for i, r := range primary {
		idx[r.ID] = i
	}
	out := primary
	for _, r := range extra {
		if i, ok := idx[r.ID]; ok {
			if r.Score > out[i].Score {
				out[i].Score = r.Score
			}
			continue
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	return out
}

func callCoTLLM(ctx context.Context, query string, cfg CoTConfig) ([]string, error) {
	var parsed struct {
		Questions []string `json:"questions"`
	}
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	err := llm.ChatStructured(ctx, client, "d7_decompose", []llm.Message{
		{Role: "system", Content: cotSystemPrompt},
		{Role: "user", Content: "Query: " + query + "\n\nReturn strict JSON only."},
	}, &parsed,
		llm.WithMaxTokens(cotMaxTokens),
		llm.WithTimeout(cotTimeout),
		llm.WithRespBodyLimit(cotRespBodyLimit),
	)
	if err != nil {
		return nil, err
	}
	return parsed.Questions, nil
}
