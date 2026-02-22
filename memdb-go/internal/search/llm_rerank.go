package search

// llm_rerank.go — LLM-powered reranker for top-K memory candidates.
//
// After vector+fulltext recall, the LLM assigns a relevance score [0.0, 1.0]
// to each candidate. This replaces the cosine-only ordering before final trimming.
//
// Architecture decisions:
//   - Uses the same OpenAI-compatible CLIProxyAPI endpoint as the extractor.
//   - Applied ONLY to the final text_mem top-K (not skill/pref; too low value).
//   - Results are cached per (query × node_ids_hash) with a 5-minute TTL.
//     This means repeated searches with identical queries are essentially free.
//   - Non-fatal: any LLM or parse failure falls back to cosine scores silently.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const rerankRespBodyLimit = 32 * 1024 // 32 KB max LLM response body for reranking

// LLMRerankConfig holds the parameters for the LLM reranker.
type LLMRerankConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// llmRerankCache is an in-process TTL cache for reranker results.
// Key: sha256(query + sorted_node_ids), Value: map[nodeID]score
type llmRerankCache struct {
	mu      chan struct{}
	entries map[string]*llmRerankEntry
}

type llmRerankEntry struct {
	expires time.Time
	scores  map[string]float64
}

var globalRerankCache = &llmRerankCache{
	mu:      make(chan struct{}, 1),
	entries: make(map[string]*llmRerankEntry),
}

func (c *llmRerankCache) get(key string) (map[string]float64, bool) {
	c.mu <- struct{}{}
	defer func() { <-c.mu }()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.scores, true
}

func (c *llmRerankCache) set(key string, scores map[string]float64, ttl time.Duration) {
	c.mu <- struct{}{}
	defer func() { <-c.mu }()
	c.entries[key] = &llmRerankEntry{expires: time.Now().Add(ttl), scores: scores}
}

const llmRerankCacheTTL = 5 * time.Minute
const llmRerankMaxCandidates = 20

// LLMRerank re-scores the top-K candidates using an LLM and re-sorts them.
// items must already be sorted by cosine score descending (best first).
// Returns a new slice ordered by LLM relevance score.
// Falls back to the original cosine ordering on any error.
func LLMRerank(ctx context.Context, query string, items []map[string]any, cfg LLMRerankConfig) []map[string]any {
	if len(items) <= 1 || cfg.APIURL == "" {
		return items
	}
	// Cap: only rerank top-N to control latency
	candidates := items
	rest := []map[string]any{}
	if len(candidates) > llmRerankMaxCandidates {
		candidates = items[:llmRerankMaxCandidates]
		rest = items[llmRerankMaxCandidates:]
	}

	// Build cache key
	nodeIDs := make([]string, 0, len(candidates))
	for _, item := range candidates {
		if id, ok := item["id"].(string); ok {
			nodeIDs = append(nodeIDs, id)
		}
	}
	sort.Strings(nodeIDs)
	cacheKey := fmt.Sprintf("%x", sha256.Sum256([]byte(query+"\x00"+strings.Join(nodeIDs, ","))))

	if cached, ok := globalRerankCache.get(cacheKey); ok {
		return applyRerankScores(candidates, rest, cached)
	}

	scores, err := callLLMReranker(ctx, query, candidates, cfg)
	if err != nil {
		return items // fallback
	}
	globalRerankCache.set(cacheKey, scores, llmRerankCacheTTL)
	return applyRerankScores(candidates, rest, scores)
}

// applyRerankScores updates metadata.relativity with LLM scores and re-sorts candidates.
func applyRerankScores(candidates, rest []map[string]any, scores map[string]float64) []map[string]any {
	for _, item := range candidates {
		id, _ := item["id"].(string)
		if score, ok := scores[id]; ok {
			if meta, ok := item["metadata"].(map[string]any); ok {
				meta["relativity"] = score
				meta["llm_reranked"] = true
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		si, sj := 0.0, 0.0
		if mi, ok := candidates[i]["metadata"].(map[string]any); ok {
			si, _ = mi["relativity"].(float64)
		}
		if mj, ok := candidates[j]["metadata"].(map[string]any); ok {
			sj, _ = mj["relativity"].(float64)
		}
		return si > sj
	})
	return append(candidates, rest...)
}

// callLLMReranker sends a single chat completion request asking the LLM to score each memory.
func callLLMReranker(ctx context.Context, query string, items []map[string]any, cfg LLMRerankConfig) (map[string]float64, error) {
	type candidate struct {
		ID     string `json:"id"`
		Memory string `json:"memory"`
	}
	cands := make([]candidate, 0, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		mem, _ := item["memory"].(string)
		if id != "" && mem != "" {
			cands = append(cands, candidate{ID: id, Memory: mem})
		}
	}
	if len(cands) == 0 {
		return nil, errors.New("no valid candidates")
	}

	content, err := rerankHTTPCall(ctx, query, cands, cfg)
	if err != nil {
		return nil, err
	}
	return parseRerankScores(content)
}

// rerankHTTPCall builds and sends the LLM rerank request, returning the raw response content.
func rerankHTTPCall(ctx context.Context, query string, cands any, cfg LLMRerankConfig) (string, error) {
	candsJSON, _ := json.Marshal(cands)
	userMsg := fmt.Sprintf(`Query: %s

Candidates:
%s

Score each candidate's relevance to the query on [0.0, 1.0].
Return ONLY valid JSON: [{"id": "...", "score": 0.8}, ...]`, query, string(candsJSON))

	payload := map[string]any{
		"model":       cfg.Model,
		"temperature": 0.0,
		"max_tokens":  512,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a memory relevance scorer. Given a query and memory candidates, score each [0.0,1.0]. Respond with only a JSON array.",
			},
			{"role": "user", "content": userMsg},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, rerankRespBodyLimit))
	if err != nil {
		return "", err
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", errors.New("llm reranker: bad response")
	}

	// Strip markdown code fences if present
	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content), nil
}

// parseRerankScores parses the JSON array of {id, score} objects into a map.
func parseRerankScores(content string) (map[string]float64, error) {
	var scored []struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(content), &scored); err != nil {
		return nil, fmt.Errorf("llm reranker: parse failed: %w", err)
	}
	result := make(map[string]float64, len(scored))
	for _, s := range scored {
		result[s.ID] = s.Score
	}
	return result, nil
}
