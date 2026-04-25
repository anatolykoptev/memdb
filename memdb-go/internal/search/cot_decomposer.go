package search

// cot_decomposer.go — D11: Chain-of-Thought query decomposition for multi-hop +
// temporal questions.
//
// CoTDecomposer takes a complex query and returns 1-N sub-queries that, taken
// together, gather the evidence needed to answer the original. Each sub-query
// is then fanned out to D2 multi-hop expansion independently and the results
// are merged via the existing union-by-id pool. Distinct from D7 (cot_decompose.go):
//   - D7 is a generic conjunction splitter ("X and Y" → ["X", "Y"]).
//   - D11 specifically targets *temporal* and *multi-hop* questions like
//     "What did Caroline do in Boston after she met Emma?" where the gain
//     comes from separating the "when" and the "what" and from breaking each
//     hop into its own retrieval.
//
// Design invariants:
//   - Best-effort: any LLM error / timeout / parse failure → fall back to
//     []string{originalQuery}. NEVER blocks the pipeline.
//   - Heuristic gate: skip the LLM call entirely for short / non-temporal /
//     single-entity queries (the common case).
//   - In-process TTL cache (5min) keyed on sha256(query) — deterministic
//     prompt + temperature 0 means cache safe for repeated identical queries.
//   - The original query is ALWAYS at index 0 of the returned slice so the
//     primary search path is unaffected when only sub-queries differ.
//
// Cache, key, and normalize helpers live in cot_decomposer_cache.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	cotDecomposerCacheTTL    = 5 * time.Minute
	cotDecomposerRespBodyMax = 8 * 1024
	cotDecomposerMinWords    = 8
	cotDecomposerMinEntities = 2
	cotDecomposerMaxTokens   = 256
)

// cotTemporalConnectors are the surface tokens that strongly correlate with
// multi-hop or temporally-relational questions in LoCoMo cat-2 / cat-3.
var cotTemporalConnectors = map[string]struct{}{
	"and": {}, "after": {}, "before": {}, "since": {},
	"while": {}, "when": {}, "until": {}, "then": {},
	"because": {}, "so": {},
}

// CoTDecomposerConfig carries the LLM endpoint config + tunables.
type CoTDecomposerConfig struct {
	APIURL        string
	APIKey        string
	Model         string
	Enabled       bool
	MaxSubQueries int           // clamp [1, 5]; 0 → use default 3
	Timeout       time.Duration // clamp [500ms, 10s]; 0 → use default 2s
}

// CoTDecomposer is safe for concurrent use.
type CoTDecomposer struct {
	cfg   CoTDecomposerConfig
	cache *cotDecomposerCache
}

// NewCoTDecomposer normalizes config bounds and wires the cache.
func NewCoTDecomposer(cfg CoTDecomposerConfig) *CoTDecomposer {
	if cfg.MaxSubQueries <= 0 {
		cfg.MaxSubQueries = 3
	}
	if cfg.MaxSubQueries > 5 {
		cfg.MaxSubQueries = 5
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Timeout < 500*time.Millisecond {
		cfg.Timeout = 500 * time.Millisecond
	}
	if cfg.Timeout > 10*time.Second {
		cfg.Timeout = 10 * time.Second
	}
	return &CoTDecomposer{cfg: cfg, cache: newCoTDecomposerCache()}
}

// Decompose returns sub-queries (with the original always at index 0 as a
// fallback). On error, timeout, disabled config, or heuristic-skip, returns
// []string{originalQuery} — never blocks, never returns empty.
func (d *CoTDecomposer) Decompose(ctx context.Context, logger *slog.Logger, query string) []string {
	original := []string{query}
	if d == nil || !d.cfg.Enabled || d.cfg.APIURL == "" {
		searchMx().D11CoTDecompose.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skip")))
		return original
	}
	if !shouldDecompose(query) {
		searchMx().D11CoTDecompose.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skip")))
		return original
	}
	cacheKey := cotCacheKey(query)
	if cached, ok := d.cache.get(cacheKey); ok {
		searchMx().D11CoTCacheHit.Add(ctx, 1)
		searchMx().D11CoTSubqueries.Record(ctx, int64(len(cached)))
		return cached
	}
	start := time.Now()
	subs, err := d.callLLM(ctx, query)
	durMS := time.Since(start).Milliseconds()
	searchMx().D11CoTDuration.Record(ctx, durMS)
	if err != nil {
		searchMx().D11CoTDecompose.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "error")))
		if logger != nil {
			logger.Debug("cot decomposer: llm failed, falling back",
				slog.String("query", query), slog.Any("error", err))
		}
		return original
	}
	cleaned := normalizeSubqueries(subs, query, d.cfg.MaxSubQueries)
	if len(cleaned) == 0 {
		searchMx().D11CoTDecompose.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skip")))
		return original
	}
	// Always ensure the original is at index 0 — service.go relies on this
	// to keep the primary search path identical to the no-CoT path.
	if !strings.EqualFold(strings.TrimSpace(cleaned[0]), strings.TrimSpace(query)) {
		cleaned = append([]string{query}, cleaned...)
		if len(cleaned) > d.cfg.MaxSubQueries+1 {
			cleaned = cleaned[:d.cfg.MaxSubQueries+1]
		}
	}
	d.cache.set(cacheKey, cleaned, cotDecomposerCacheTTL)
	searchMx().D11CoTDecompose.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success")))
	searchMx().D11CoTSubqueries.Record(ctx, int64(len(cleaned)))
	if logger != nil {
		logger.Info("cot decomposer applied",
			slog.String("original", query),
			slog.Any("sub_queries", cleaned),
			slog.Int64("duration_ms", durMS))
	}
	return cleaned
}

// shouldDecompose is the heuristic gate. Avoid the LLM round-trip for queries
// that are unlikely to benefit. All three signals must be present:
//   - length > cotDecomposerMinWords words
//   - contains a temporal/causal connector
//   - has at least cotDecomposerMinEntities capitalized name-like tokens
//
// Exposed for white-box testing; do not depend on this from outside the
// package.
func shouldDecompose(query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	words := strings.Fields(q)
	if len(words) <= cotDecomposerMinWords {
		return false
	}
	hasConnector := false
	entityCount := 0
	for i, w := range words {
		lower := strings.ToLower(strings.TrimFunc(w, isNonAlphaNum))
		if _, ok := cotTemporalConnectors[lower]; ok {
			hasConnector = true
		}
		if i == 0 {
			continue // ignore sentence-initial capitalization
		}
		if isLikelyEntity(w) {
			entityCount++
		}
	}
	return hasConnector && entityCount >= cotDecomposerMinEntities
}

func isNonAlphaNum(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// isLikelyEntity returns true if the token starts with an uppercase letter
// and has at least one more letter — i.e. a proper name, not a stray "I" or
// "A". Punctuation around the token is stripped.
func isLikelyEntity(tok string) bool {
	stripped := strings.TrimFunc(tok, isNonAlphaNum)
	if len(stripped) < 2 {
		return false
	}
	runes := []rune(stripped)
	if !unicode.IsUpper(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// cotDecomposerPrompt is the user-facing instruction. The LLM should ONLY
// emit a JSON array of strings — no prose, no preamble.
const cotDecomposerPrompt = `You are decomposing a question to retrieve memories piece-by-piece.

Question: %s

Output a JSON array of 1-%d simpler sub-questions that together cover the
original. Rules:
- Each sub-question should be answerable independently
- Preserve named entities verbatim
- For temporal questions, separate the "when" and the "what"
- For multi-hop, separate each hop into its own sub-question
- If the question is already simple, return [<original>]

Output ONLY the JSON array, no prose. Example:
["When did Caroline meet Emma?", "What did Caroline do in Boston?"]`

func (d *CoTDecomposer) callLLM(ctx context.Context, query string) ([]string, error) {
	payload := map[string]any{
		"model":       d.cfg.Model,
		"temperature": 0.0,
		"max_tokens":  cotDecomposerMaxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": fmt.Sprintf(cotDecomposerPrompt, query, d.cfg.MaxSubQueries)},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		d.cfg.APIURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.APIKey)
	}
	client := &http.Client{Timeout: d.cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cot decomposer: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, cotDecomposerRespBodyMax))
	if err != nil {
		return nil, err
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return nil, errors.New("cot decomposer: bad response")
	}
	content := llm.StripJSONFence([]byte(chatResp.Choices[0].Message.Content))
	var arr []string
	if err := json.Unmarshal(content, &arr); err != nil {
		return nil, fmt.Errorf("cot decomposer: parse: %w", err)
	}
	return arr, nil
}
