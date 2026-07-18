package search

// enhance.go — Post-retrieval memory enhancement (MemOS MEMORY_RECREATE_ENHANCEMENT port).
//
// Before passing retrieved memories to the chat handler, runs a single LLM call
// to disambiguate and fuse the result set:
//   - Resolve pronouns ("she" → "Caroline") using only the retrieved memory set
//   - Convert relative times ("last night" → "November 25, 2025") using memory context
//   - Merge closely related entries with complementary details
//   - Drop entries irrelevant to the current query
//
// This is critical for multi-hop queries where stored memories contain unresolved
// pronouns and relative times from their original conversation context.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const enhancePrompt = `You are a memory enhancement assistant. Given a user query and a list of retrieved memory fragments, your task is to produce a CLEANED version of each memory that is useful for answering the query.

For each memory:
1. Resolve pronouns: Replace "she", "he", "they", "it" with explicit entity names using ONLY information from the provided memories. If disambiguation is impossible, keep the original phrasing exactly. Never guess.
2. Resolve relative times: Convert "yesterday", "last week", "next month" to concrete dates ONLY if other memories provide the temporal context. Otherwise keep the original wording.
3. Merge: If two memories say nearly the same thing with complementary details, combine them into one entry (pick one ID).

Output a JSON array of objects: [{"id": "original_id", "memory": "enhanced text"}]
Include ALL input memories in the output. Keep original memory IDs.
If a memory needs no changes, return it as-is with its original text.`

const (
	enhanceMaxTokens   = 2048
	enhanceRespLimit   = 32 * 1024 // 32 KB
	enhanceMinMemories = 3         // skip enhancement for trivial result sets
	enhanceMaxMemories = 15        // cap to avoid prompt overflow
	enhanceCacheTTL    = 3 * time.Minute
)

// enhanceCache is an in-process TTL cache for EnhanceMemories results.
// Key: sha256(query + sorted_memory_ids). Value: enhanced []map[string]any.
type enhanceCacheStore struct {
	mu      sync.Mutex
	entries map[string]*enhanceCacheEntry
}

type enhanceCacheEntry struct {
	expires time.Time
	result  []map[string]any
}

func (c *enhanceCacheStore) get(key string) ([]map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.result, true
}

func (c *enhanceCacheStore) set(key string, result []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &enhanceCacheEntry{expires: time.Now().Add(enhanceCacheTTL), result: result}
}

var globalEnhanceCache = &enhanceCacheStore{entries: make(map[string]*enhanceCacheEntry)}

// EnhanceConfig configures the post-retrieval enhancement LLM call.
type EnhanceConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// enhancedMemory is the LLM output for a single enhanced memory.
type enhancedMemory struct {
	ID     string `json:"id"`
	Memory string `json:"memory"`
}

// EnhanceMemories runs a disambiguation+fusion LLM pass on retrieved memories.
// Returns the enhanced memory list. On any error, returns the original list unchanged.
func EnhanceMemories(
	ctx context.Context,
	query string,
	memories []map[string]any,
	cfg EnhanceConfig,
) []map[string]any {
	if len(memories) < enhanceMinMemories || cfg.APIURL == "" {
		return memories
	}

	// Forensic 2026-05-02 fix #4: cap by RELATIVITY, not insertion order.
	// The previous code took items[:enhanceMaxMemories] which silently
	// dropped high-scoring rows whenever the caller had merged
	// alternating-speaker buckets (the dual-speaker path). Sort first;
	// take the top-N by score; pass the dropped count out as a metric so
	// operators can spot pools that habitually overflow the cap.
	items := memories
	dropped := 0
	if len(items) > enhanceMaxMemories {
		sorted := make([]map[string]any, len(items))
		copy(sorted, items)
		sort.SliceStable(sorted, func(i, j int) bool {
			return enhanceItemRelativity(sorted[i]) > enhanceItemRelativity(sorted[j])
		})
		dropped = len(items) - enhanceMaxMemories
		items = sorted[:enhanceMaxMemories]
	}
	recordEnhanceTruncated(ctx, dropped)

	type memInput struct {
		ID     string `json:"id"`
		Memory string `json:"memory"`
	}
	inputs := make([]memInput, 0, len(items))
	for _, m := range items {
		id, _ := m["id"].(string)
		text, _ := m["memory"].(string)
		if id == "" || text == "" {
			continue
		}
		inputs = append(inputs, memInput{ID: id, Memory: text})
	}
	if len(inputs) == 0 {
		return memories
	}

	// Cache lookup: key = sha256(query + sorted memory IDs).
	ids := make([]string, len(inputs))
	for i, inp := range inputs {
		ids[i] = inp.ID
	}
	sort.Strings(ids)
	// Forensic 2026-05-02 fix #4: include cfg.Model in the cache key so a
	// model rollout (e.g. swapping gemini-2.5-flash → gemini-3-flash) does
	// not return stale enhancements computed by the previous model.
	cacheKey := fmt.Sprintf("%x", sha256.Sum256([]byte(query+"\x00"+cfg.Model+"\x00"+strings.Join(ids, ","))))
	if cached, ok := globalEnhanceCache.get(cacheKey); ok {
		return cached
	}

	inputJSON, _ := json.Marshal(inputs)
	userMsg := fmt.Sprintf("Query: %s\n\nMemories:\n%s\n\nJSON:", query, string(inputJSON))

	enhanced, err := callEnhanceLLM(ctx, userMsg, cfg)
	if err != nil || len(enhanced) == 0 {
		return memories
	}

	result := applyEnhancements(memories, enhanced)
	globalEnhanceCache.set(cacheKey, result)
	return result
}

// callEnhanceLLM makes the LLM call for memory enhancement.
func callEnhanceLLM(ctx context.Context, userMsg string, cfg EnhanceConfig) ([]enhancedMemory, error) {
	var result []enhancedMemory
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	if err := llm.ChatStructured(ctx, client, "d10_enhance_legacy", []llm.Message{
		{Role: "system", Content: enhancePrompt},
		{Role: "user", Content: userMsg},
	}, &result,
		llm.WithMaxTokens(enhanceMaxTokens),
		llm.WithTimeout(20*time.Second),
		llm.WithRespBodyLimit(enhanceRespLimit),
	); err != nil {
		return nil, err
	}
	return result, nil
}

// enhanceItemRelativity reads metadata.relativity safely. Items missing
// the score sort to the bottom (0). Mirrors the search package's
// `relativity` accessor in handlers/chat_memories.go without crossing
// the package boundary.
func enhanceItemRelativity(m map[string]any) float64 {
	md, ok := m["metadata"].(map[string]any)
	if !ok {
		return 0
	}
	v, _ := md["relativity"].(float64)
	return v
}

// applyEnhancements merges enhanced texts back into the original memory maps.
// Memories not in the enhanced list are kept as-is (LLM may have filtered them).
func applyEnhancements(originals []map[string]any, enhanced []enhancedMemory) []map[string]any {
	lookup := make(map[string]string, len(enhanced))
	for _, e := range enhanced {
		if e.ID != "" && e.Memory != "" {
			lookup[e.ID] = e.Memory
		}
	}

	result := make([]map[string]any, 0, len(originals))
	for _, m := range originals {
		id, _ := m["id"].(string)
		if text, ok := lookup[id]; ok {
			// Clone the map and update memory text.
			clone := make(map[string]any, len(m))
			for k, v := range m {
				clone[k] = v
			}
			clone["memory"] = text
			result = append(result, clone)
		} else {
			result = append(result, m)
		}
	}
	return result
}
