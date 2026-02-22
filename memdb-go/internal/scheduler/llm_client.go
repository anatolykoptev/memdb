package scheduler

// llm_client.go — shared LLM call helper for all Reorganizer sub-pipelines.
//
// Delegates to the shared llm.Client which handles retry with exponential
// backoff and model fallback on quota errors. All callers (consolidate,
// feedback, prefs, enhance) only build messages and parse domain-specific JSON.

import (
	"context"
	"strings"
)

// callLLM sends a chat completions request via the shared LLM client and
// returns the raw content string from the first choice.
// The caller is responsible for parsing the domain-specific JSON from the result.
func (r *Reorganizer) callLLM(ctx context.Context, msgs []map[string]string, maxTokens int) (string, error) {
	return r.llmClient.Chat(ctx, msgs, maxTokens)
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
	return string(runes[:maxLen]) + "..."
}
