package search

// fine.go — LLM fine-mode: relevance filtering + recall hint for search gaps.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

// FineConfig configures LLM calls for fine-mode filtering and recall.
type FineConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// filterDecision is a single keep/drop judgment from the LLM.
type filterDecision struct {
	ID   string `json:"id"`
	Keep bool   `json:"keep"`
}

// LLMFilter sends memories to the LLM for relevance filtering.
// On any error, returns all memories unchanged (graceful degradation).
// If the LLM keeps nothing, returns the originals as well.
func LLMFilter(
	ctx context.Context,
	query string,
	memories []map[string]any,
	cfg FineConfig,
) []map[string]any {
	if len(memories) == 0 {
		return memories
	}

	items := memories
	if len(items) > fineMaxMemories {
		items = items[:fineMaxMemories]
	}

	inputJSON, _ := json.Marshal(buildMemoryList(items))
	prompt := fmt.Sprintf("Query: %s\n\nMemories:\n%s", query, string(inputJSON))

	raw, err := callLLMForJSON(ctx, fineFilterPrompt, prompt, cfg)
	if err != nil {
		return memories
	}

	var decisions []filterDecision
	if err := json.Unmarshal(raw, &decisions); err != nil {
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

// LLMRecallHint asks the LLM what info is missing, returning a hint query.
// Returns "" on any error.
func LLMRecallHint(
	ctx context.Context,
	query string,
	memories []map[string]any,
	cfg FineConfig,
) string {
	inputJSON, _ := json.Marshal(buildMemoryList(memories))
	prompt := fmt.Sprintf("Query: %s\n\nKept memories:\n%s", query, string(inputJSON))

	raw, err := callLLMForJSON(ctx, fineRecallPrompt, prompt, cfg)
	if err != nil {
		return ""
	}

	var hint struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &hint); err != nil {
		return ""
	}
	return hint.Query
}

// extractID gets the ID string from a memory map (top-level "id" or metadata.id).
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

// callLLMForJSON makes an OpenAI-compatible chat completions call requesting JSON output.
func callLLMForJSON(
	ctx context.Context,
	systemPrompt, userMsg string,
	cfg FineConfig,
) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fineFilterTimeout)
	defer cancel()

	payload := map[string]any{
		"model":           cfg.Model,
		"temperature":     0.0,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMsg},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.APIURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fine: LLM returned status %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, fineRespLimit))
	if err != nil {
		return nil, err
	}

	var chatResp struct {
		Choices []struct {
			Message struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return nil, errors.New("fine: bad LLM response")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	return []byte(content), nil
}
