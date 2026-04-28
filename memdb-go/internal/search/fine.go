// Package search — fine.go: LLM fine-mode relevance filtering + recall hint.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	fineFilterTimeout = 15 * time.Second
	fineRecallThresh  = 0.30 // if >30% dropped, consider recall
	fineMaxMemories   = 20
	fineRespLimit     = 32 * 1024 // 32 KB response cap
)

const fineFilterPrompt = `You are a relevance judge. Given a user query and a list of memories, decide which memories are relevant.

Output a JSON array: [{"id": "memory_id", "keep": true/false}]
Include a decision for EVERY memory. Keep memories that help answer the query; drop ones that are clearly unrelated.`

const fineRecallPrompt = `You are a search assistant. Given a user query and the memories already found, identify what information is MISSING to fully answer the query.

Output JSON: {"query": "a refined search query to find the missing information"}
If nothing is missing, return {"query": ""}.`

// FineConfig configures LLM calls for fine-mode.
type FineConfig struct {
	APIURL string
	APIKey string
	Model  string
}

type filterDecision struct {
	ID   string `json:"id"`
	Keep bool   `json:"keep"`
}

// LLMFilter sends memories to the LLM for relevance filtering.
// On any error or empty result, returns all memories unchanged (graceful degradation).
func LLMFilter(ctx context.Context, query string, memories []map[string]any, cfg FineConfig) []map[string]any {
	if len(memories) == 0 {
		return memories
	}

	items := memories
	if len(items) > fineMaxMemories {
		items = items[:fineMaxMemories]
	}

	inputJSON, _ := json.Marshal(buildMemoryList(items))
	prompt := fmt.Sprintf("Query: %s\n\nMemories:\n%s", query, string(inputJSON))

	var decisions []filterDecision
	if err := callLLMForJSON(ctx, fineFilterPrompt, prompt, cfg, &decisions); err != nil {
		return memories
	}

	keepSet := make(map[string]bool, len(decisions))
	for _, d := range decisions {
		if d.Keep {
			keepSet[d.ID] = true
		}
	}

	// If nothing kept, return originals (LLM may have been too aggressive).
	if len(keepSet) == 0 {
		return memories
	}

	result := make([]map[string]any, 0, len(keepSet))
	for _, m := range memories {
		if keepSet[extractID(m)] {
			result = append(result, m)
		}
	}

	if len(result) == 0 {
		return memories
	}
	return result
}

// LLMRecallHint asks the LLM what info is missing. Returns "" on any error.
func LLMRecallHint(ctx context.Context, query string, memories []map[string]any, cfg FineConfig) string {
	inputJSON, _ := json.Marshal(buildMemoryList(memories))
	prompt := fmt.Sprintf("Query: %s\n\nKept memories:\n%s", query, string(inputJSON))

	var hint struct {
		Query string `json:"query"`
	}
	if err := callLLMForJSON(ctx, fineRecallPrompt, prompt, cfg, &hint); err != nil {
		return ""
	}
	return hint.Query
}

func extractID(m map[string]any) string {
	if id, ok := m["id"].(string); ok && id != "" {
		return id
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		if id, ok := meta["id"].(string); ok {
			return id
		}
	}
	return ""
}

type memEntry struct {
	ID     string `json:"id"`
	Memory string `json:"memory"`
}

func buildMemoryList(items []map[string]any) []memEntry {
	entries := make([]memEntry, 0, len(items))
	for _, m := range items {
		id := extractID(m)
		text, _ := m["memory"].(string)
		if id != "" {
			entries = append(entries, memEntry{ID: id, Memory: text})
		}
	}
	return entries
}

// callLLMForJSON makes an OpenAI-compatible chat completions call with the
// JSON-object response_format hint and unmarshals the assistant content into
// target via llm.ChatStructured.
func callLLMForJSON[T any](ctx context.Context, systemPrompt, userMsg string, cfg FineConfig, target *T) error {
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	return llm.ChatStructured(ctx, client, "search_fine_judge", []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}, target,
		llm.WithTimeout(fineFilterTimeout),
		llm.WithRespBodyLimit(fineRespLimit),
		llm.WithJSONResponseMode(),
	)
}
