package search

// iterative_retrieval.go — Multi-stage iterative retrieval (MemOS AdvancedSearcher port).
//
// Motivation: After a first-pass vector recall, a single LLM call checks whether
// the current memories are sufficient to answer the query. If not, it generates
// focused retrieval_phrases — sub-queries targeting the specific gaps identified.
// Each sub-query runs a fresh vector search; results are merged via RRF.
//
// This is the single highest-impact remaining improvement: MemOS's AdvancedSearcher
// uses exactly this pattern to handle multi-hop questions (e.g. "What did Alice say
// about the project Bob is managing?") where the first hop retrieves Alice's note
// but misses the Bob→project connection.
//
// Configuration:
//   - NumStages: max expansion stages (0 = disabled, 2 = fast, 3 = fine)
//   - Each stage adds ~300ms of latency (one LLM call + one vector query)
//   - Results cached per (query+stage+node_ids) with 2-min TTL

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// IterativeConfig configures multi-stage iterative retrieval.
type IterativeConfig struct {
	APIURL    string
	APIKey    string
	Model     string
	NumStages int // max expansion stages (0 = disabled, 2 = fast, 3 = fine)
}

// iterativeCache is a simple in-process TTL cache for iterative retrieval results.
type iterativeCache struct {
	mu      sync.Mutex
	entries map[string]*iterativeCacheEntry
}

type iterativeCacheEntry struct {
	expires time.Time
	phrases []string
}

var globalIterativeCache = &iterativeCache{
	entries: make(map[string]*iterativeCacheEntry),
}

func (c *iterativeCache) get(key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.phrases, true
}

func (c *iterativeCache) set(key string, phrases []string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &iterativeCacheEntry{expires: time.Now().Add(ttl), phrases: phrases}
}

const iterativeCacheTTL = 2 * time.Minute

// stageDecision is the structured output from one LLM expansion stage.
type stageDecision struct {
	CanAnswer        bool     `json:"can_answer"`
	Reason           string   `json:"reason,omitempty"`
	RetrievalPhrases []string `json:"retrieval_phrases"`
}

// IterativeExpand runs up to cfg.NumStages of LLM-guided retrieval expansion.
//
// Given:
//   - query: the original user query
//   - firstPassItems: already-formatted results from first-pass vector recall
//   - embedFn: function to embed sub-queries and run vector search (returns formatted items)
//   - cfg: configuration including LLM endpoint and max stages
//
// Returns the merged + RRF-sorted result set combining all stages.
// Non-fatal: any LLM or embed failure falls back to firstPassItems unchanged.
func IterativeExpand(
	ctx context.Context,
	query string,
	firstPassItems []map[string]any,
	embedFn func(ctx context.Context, subQuery string) ([]map[string]any, error),
	cfg IterativeConfig,
) []map[string]any {
	if cfg.NumStages <= 0 || cfg.APIURL == "" || len(firstPassItems) == 0 {
		return firstPassItems
	}

	all := firstPassItems
	seen := make(map[string]bool)
	for _, item := range firstPassItems {
		if id, ok := item["id"].(string); ok {
			seen[id] = true
		}
	}

	for stage := 0; stage < cfg.NumStages; stage++ {
		// Build memory context from current results (top-5 for brevity)
		memCtx := buildMemoryContext(all, 5)

		// Check cache for expansion phrases
		cacheKey := buildStageKey(query, stage, all)
		phrases, cached := globalIterativeCache.get(cacheKey)
		if !cached {
			decision, err := callExpansionLLM(ctx, query, memCtx, stage, cfg)
			if err != nil {
				break // non-fatal: stop expansion, return what we have
			}
			if decision.CanAnswer {
				break // LLM says we have enough — stop early
			}
			phrases = decision.RetrievalPhrases
			globalIterativeCache.set(cacheKey, phrases, iterativeCacheTTL)
		}

		if len(phrases) == 0 {
			break
		}

		// Run vector search for each expansion phrase, collect new items
		for _, phrase := range phrases {
			if strings.TrimSpace(phrase) == "" {
				continue
			}
			expanded, err := embedFn(ctx, phrase)
			if err != nil {
				continue
			}
			for _, item := range expanded {
				id, _ := item["id"].(string)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				// Tag provenance for debug/logging
				if meta, ok := item["metadata"].(map[string]any); ok {
					meta["iterative_stage"] = stage + 1
					meta["expansion_phrase"] = phrase
				}
				all = append(all, item)
			}
		}
	}

	// Re-sort by relativity (score) descending so new items slot in correctly
	sort.SliceStable(all, func(i, j int) bool {
		si, sj := 0.0, 0.0
		if mi, ok := all[i]["metadata"].(map[string]any); ok {
			si, _ = mi["relativity"].(float64)
		}
		if mj, ok := all[j]["metadata"].(map[string]any); ok {
			sj, _ = mj["relativity"].(float64)
		}
		return si > sj
	})

	return all
}

// buildMemoryContext formats the top-N items as a concise bullet list for the LLM.
func buildMemoryContext(items []map[string]any, n int) string {
	if n > len(items) {
		n = len(items)
	}
	var sb strings.Builder
	for i, item := range items[:n] {
		mem, _ := item["memory"].(string)
		if mem == "" {
			continue
		}
		fmt.Fprintf(&sb, "%d. %s\n", i+1, mem)
	}
	return sb.String()
}

// buildStageKey produces a cache key for a given stage.
func buildStageKey(query string, stage int, items []map[string]any) string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id, ok := item["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	raw := fmt.Sprintf("%s\x00%d\x00%s", query, stage, strings.Join(ids, ","))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}

// callExpansionLLM calls the LLM to check if current memories suffice and get expansion phrases.
func callExpansionLLM(ctx context.Context, query, memCtx string, stage int, cfg IterativeConfig) (stageDecision, error) {
	systemPrompt := `You are a memory retrieval assistant. You are given:
1. A user query
2. Memory fragments retrieved so far

Determine if the retrieved memories are sufficient to answer the query.
If YES: set can_answer=true and retrieval_phrases=[].
If NO: set can_answer=false and provide 1-3 short retrieval_phrases (noun phrases or short sentences) that would help find the missing information.

Respond ONLY with valid JSON matching this schema:
{"can_answer": bool, "reason": "one sentence", "retrieval_phrases": ["phrase1", ...]}`

	userMsg := fmt.Sprintf("Query: %s\n\nRetrieved memories:\n%s\n\nJSON:", query, memCtx)
	if stage > 0 {
		userMsg = fmt.Sprintf("Query: %s\n\nExpansion stage %d. Retrieved memories so far:\n%s\n\nJSON:", query, stage+1, memCtx)
	}

	payload := map[string]any{
		"model":       cfg.Model,
		"temperature": 0.0,
		"max_tokens":  256,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMsg},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return stageDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return stageDecision{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return stageDecision{}, err
	}

	var chatResp struct {
		Choices []struct {
			Message struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return stageDecision{}, fmt.Errorf("iterative: bad LLM response")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var decision stageDecision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return stageDecision{}, fmt.Errorf("iterative: parse failed: %w", err)
	}
	return decision, nil
}
