package scheduler

// llm_client.go — shared LLM HTTP call helper for all Reorganizer sub-pipelines.
//
// All four LLM methods (consolidate, feedback, prefs, enhance) share the same
// request/response shape: POST /v1/chat/completions → choices[0].message.content.
// callLLM centralises the HTTP round-trip so each caller only builds messages
// and parses the domain-specific JSON result.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// llmRequest is the OpenAI-compatible chat completions request body.
type llmRequest struct {
	Model       string               `json:"model"`
	Messages    []map[string]string  `json:"messages"`
	Temperature float64              `json:"temperature"`
	MaxTokens   int                  `json:"max_tokens"`
}

// callLLM sends a chat completions request to the configured LLM proxy and
// returns the raw content string from the first choice.
// The caller is responsible for parsing the domain-specific JSON from the result.
func (r *Reorganizer) callLLM(ctx context.Context, msgs []map[string]string, maxTokens int) (string, error) {
	reqBody, err := json.Marshal(llmRequest{
		Model:       r.llmModel,
		Messages:    msgs,
		Temperature: 0.1,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("callLLM marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.llmURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("callLLM build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.llmKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.llmKey)
	}

	httpResp, err := r.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("callLLM request: %w", err)
	}
	defer httpResp.Body.Close()

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("callLLM decode: %w", err)
	}
	if apiResp.Error != nil {
		return "", fmt.Errorf("callLLM api error: %s", apiResp.Error.Message)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("callLLM: no choices returned")
	}

	return apiResp.Choices[0].Message.Content, nil
}

// stripFences removes optional ```json ... ``` markdown fences from LLM output.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// truncate shortens s to maxLen runes for safe log output.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}
