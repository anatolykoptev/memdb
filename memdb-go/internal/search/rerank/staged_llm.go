// Package rerank — staged retrieval LLM backend internals (M12.6).
//
// Splits the LLM-driven Stage 2 / Stage 3 logic out of staged.go so the
// strategy entrypoint stays under the file-size budget. The two helpers
// here are called by LLMBackend (always) and HybridBackend (Stage 3 only).
// Behaviour is byte-for-byte identical to the pre-M12.6 inline LLM path —
// when MEMDB_STAGED_BACKEND=llm the on-the-wire prompts and JSON shapes
// match the legacy implementation exactly.
package rerank

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// llmStage2Refine is the LLM-driven shortlist refinement, used by
// LLMBackend. Capped to shortlistCap on output (the backend honours
// this so the strategy doesn't need a second pass).
func llmStage2Refine(ctx context.Context, cfg LLMConfig, query string, items []Item, shortlistCap int) ([]string, error) {
	if cfg.APIURL == "" {
		return nil, errLLMUnavailable
	}
	var b strings.Builder
	for i, it := range items {
		mem := it.EmbeddingText()
		if len(mem) > stagedMemTruncStage2 {
			mem = mem[:stagedMemTruncStage2] + "..."
		}
		fmt.Fprintf(&b, "[%d] id=%s  %s\n", i+1, it.ID(), strings.TrimSpace(mem))
	}
	userMsg := fmt.Sprintf("Query: %s\n\nCandidates:\n%s\n\nReturn JSON {\"ids\":[...]} — top 10 ids, most relevant first.", query, b.String())

	var parsed struct {
		IDs []string `json:"ids"`
	}
	if err := callStagedLLM(ctx, cfg, stagedStage2SystemPrompt, userMsg, &parsed); err != nil {
		return nil, fmt.Errorf("stage2: %w", err)
	}
	if shortlistCap > 0 && len(parsed.IDs) > shortlistCap {
		parsed.IDs = parsed.IDs[:shortlistCap]
	}
	return parsed.IDs, nil
}

// llmStage3Justify is the LLM-driven per-ID relevance judge, used by
// LLMBackend and HybridBackend.
func llmStage3Justify(ctx context.Context, cfg LLMConfig, query string, shortlist []string, allItems []Item) ([]stagedJustifiedItem, error) {
	if cfg.APIURL == "" {
		return nil, errLLMUnavailable
	}
	byID := make(map[string]string, len(allItems))
	for _, it := range allItems {
		byID[it.ID()] = it.EmbeddingText()
	}
	var b strings.Builder
	for _, id := range shortlist {
		mem, ok := byID[id]
		if !ok {
			continue
		}
		if len(mem) > stagedMemTruncStage3 {
			mem = mem[:stagedMemTruncStage3] + "..."
		}
		fmt.Fprintf(&b, "id=%s\n  %s\n\n", id, strings.TrimSpace(mem))
	}
	userMsg := fmt.Sprintf("Query: %s\n\nShortlisted memories:\n%s\n\nReturn JSON {\"items\":[{\"id\",\"justification\",\"relevant\"}...]}", query, b.String())

	var parsed struct {
		Items []stagedJustifiedItem `json:"items"`
	}
	if err := callStagedLLM(ctx, cfg, stagedStage3SystemPrompt, userMsg, &parsed); err != nil {
		return nil, fmt.Errorf("stage3: %w", err)
	}
	return parsed.Items, nil
}

// errLLMUnavailable is returned by the LLM helpers when no API URL is
// configured. Symmetric with errCEUnavailable in staged_backend.go.
var errLLMUnavailable = llmUnavailableError{}

type llmUnavailableError struct{}

func (llmUnavailableError) Error() string { return "rerank: staged LLM endpoint unavailable" }

// callStagedLLM dispatches a single chat-completion via llm.ChatStructured.
// Free function so the generic type T is inferred from the target argument
// at call sites.
func callStagedLLM[T any](ctx context.Context, cfg LLMConfig, systemPrompt, userMsg string, target *T) error {
	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
	return llm.ChatStructured(ctx, client, "d5_staged", []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}, target,
		llm.WithMaxTokens(stagedMaxTokens),
		llm.WithTimeout(stagedTimeout),
		llm.WithRespBodyLimit(stagedRespBodyLimit),
	)
}
