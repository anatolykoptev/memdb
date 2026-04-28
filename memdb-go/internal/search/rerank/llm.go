// Package rerank — LLM-judge strategy.
//
// Ports llm_rerank.go from the parent search pkg into a typed Reranker.
// Behaviour parity:
//   - Cap to LLMRerankMaxCandidates (top-N by cosine).
//   - 5-minute in-process result cache keyed by sha256(query+sorted_ids).
//   - Apply LLM scores, mark llm_reranked=true, stable-sort DESC.
//   - Fall back to input order on any LLM error.
//
// Transport stays on llm.ChatStructured (R0 contract). No raw net/http.
package rerank

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	llmRerankRespBodyLimit          = 32 * 1024
	llmRerankCacheTTL               = 5 * time.Minute
	llmRerankMaxCandidates          = 20
	llmRerankPromptID               = "llm_rerank"
	llmRerankSystemPrompt           = "You are a memory relevance scorer. Given a query and memory candidates, score each [0.0,1.0]. Respond with only a JSON array."
)

// LLMConfig carries the LLM endpoint credentials. Mirrors the legacy
// LLMRerankConfig — same field shape so the parent search pkg can pass
// it through unchanged.
type LLMConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// LLMJudge reranks the top-N candidates by an LLM-assigned [0,1] score.
//
// Decision is now made by the chain caller (the parent search pkg's gate
// computes whether to run + how many to send via Cap). Strategy honours
// Cap if > 0; 0 means "all".
type LLMJudge struct {
	Config LLMConfig
	// Cap limits the prefix of items sent to the LLM (0 = all up to
	// llmRerankMaxCandidates). The remainder is appended after the
	// reranked prefix in original order.
	Cap int
}

// Name implements Reranker.
func (LLMJudge) Name() string { return "llm_judge" }

// Rerank implements Reranker.
func (j LLMJudge) Rerank(ctx context.Context, query string, items []Item) ([]Item, error) {
	if len(items) <= 1 || j.Config.APIURL == "" {
		RecordSkipped(ctx, j.Name())
		return items, nil
	}

	// Decide the prefix to score: respect Cap, then the hard ceiling.
	prefixSize := len(items)
	if j.Cap > 0 && j.Cap < prefixSize {
		prefixSize = j.Cap
	}
	if prefixSize > llmRerankMaxCandidates {
		prefixSize = llmRerankMaxCandidates
	}
	candidates := items[:prefixSize]
	rest := items[prefixSize:]

	// Cache key: query + sorted candidate IDs.
	nodeIDs := make([]string, 0, len(candidates))
	for _, it := range candidates {
		if id := it.ID(); id != "" {
			nodeIDs = append(nodeIDs, id)
		}
	}
	sort.Strings(nodeIDs)
	cacheKey := fmt.Sprintf("%x", sha256.Sum256([]byte(query+"\x00"+strings.Join(nodeIDs, ","))))

	if cached, ok := globalLLMCache.get(cacheKey); ok {
		out := applyLLMScores(candidates, rest, cached)
		RecordSuccess(ctx, j.Name())
		return out, nil
	}

	scores, err := callLLMJudge(ctx, query, candidates, j.Config)
	if err != nil {
		// Surface the failure: return (nil, err) so Chain.Rerank records
		// outcome=error against memdb.search.rerank_strategy_total and
		// forwards the input items unchanged (chain is best-effort — see
		// strategy.go RerankWithTimings). Operators can detect LLM rerank
		// failures from the metric instead of silently mistaking them for
		// successful no-ops.
		return nil, err
	}
	globalLLMCache.set(cacheKey, scores, llmRerankCacheTTL)
	out := applyLLMScores(candidates, rest, scores)
	RecordSuccess(ctx, j.Name())
	return out, nil
}

// applyLLMScores writes scores onto each candidate, marks llm_reranked,
// sorts by score DESC, and re-appends the unscored remainder.
func applyLLMScores(candidates, rest []Item, scores map[string]float64) []Item {
	for _, it := range candidates {
		if score, ok := scores[it.ID()]; ok {
			it.SetScore(score)
			it.SetMeta("llm_reranked", true)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score() > candidates[j].Score()
	})
	out := make([]Item, 0, len(candidates)+len(rest))
	out = append(out, candidates...)
	out = append(out, rest...)
	return out
}

// callLLMJudge sends a single chat completion asking the LLM to score
// each memory candidate.
func callLLMJudge(ctx context.Context, query string, items []Item, cfg LLMConfig) (map[string]float64, error) {
	type candidate struct {
		ID     string `json:"id"`
		Memory string `json:"memory"`
	}
	cands := make([]candidate, 0, len(items))
	for _, it := range items {
		id := it.ID()
		mem := it.EmbeddingText()
		if id != "" && mem != "" {
			cands = append(cands, candidate{ID: id, Memory: mem})
		}
	}
	if len(cands) == 0 {
		return nil, errors.New("no valid candidates")
	}
	candsJSON, _ := json.Marshal(cands)
	userMsg := fmt.Sprintf(`Query: %s

Candidates:
%s

Score each candidate's relevance to the query on [0.0, 1.0].
Return ONLY valid JSON: [{"id": "...", "score": 0.8}, ...]`, query, string(candsJSON))

	type rerankScore struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	}
	var scored []rerankScore
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	if err := llm.ChatStructured(ctx, client, llmRerankPromptID, []llm.Message{
		{Role: "system", Content: llmRerankSystemPrompt},
		{Role: "user", Content: userMsg},
	}, &scored,
		llm.WithMaxTokens(512),
		llm.WithTimeout(15*time.Second),
		llm.WithRespBodyLimit(llmRerankRespBodyLimit),
	); err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(scored))
	for _, s := range scored {
		result[s.ID] = s.Score
	}
	return result, nil
}

// llmCache is the per-process TTL cache for LLM rerank results.
type llmCache struct {
	mu      sync.Mutex
	entries map[string]*llmCacheEntry
}

type llmCacheEntry struct {
	expires time.Time
	scores  map[string]float64
}

var globalLLMCache = &llmCache{
	entries: make(map[string]*llmCacheEntry),
}

func (c *llmCache) get(key string) (map[string]float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.scores, true
}

func (c *llmCache) set(key string, scores map[string]float64, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &llmCacheEntry{expires: time.Now().Add(ttl), scores: scores}
}
