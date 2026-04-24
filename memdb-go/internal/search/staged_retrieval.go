package search

// staged_retrieval.go — D5: 3-stage iterative retrieval prompts.
//
// Replaces / supplements the single flat LLM rerank with a
// coarse → refine → justify cascade modeled after LoCoMo top-performer
// retrievers.
//
// Pipeline:
//   Stage 1 (no LLM): existing vector + BM25 + CE rerank already
//     produces the top-N input slice (typically N≤50 post-CE).
//   Stage 2 (1 LLM call): shortlist top 10 ids most likely to answer
//     the query. Prompts return a JSON {"ids": [...]}.
//   Stage 3 (1 LLM call on the 10): 1-sentence justification per item;
//     drops items marked IRRELEVANT. Reorders so survivors rank first,
//     non-shortlisted remainder preserved after.
//
// Design invariants:
//   - Env-gated (MEMDB_SEARCH_STAGED=true, default off).
//   - Reuses LLMReranker {APIURL, APIKey, Model}.
//   - Temperature 0.0 for determinism.
//   - Runs as its own pipeline step (6.15), independent of LLMRerank
//     (which may be enabled in parallel — this step consumes whatever
//     order it produces).
//   - Graceful degrade on any LLM error: returns input unchanged.
//   - Never invents ids — reorderByIDs tolerates ids not present in the
//     input set.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	stagedTimeout             = 15 * time.Second
	stagedMaxTokens           = 800
	stagedMinInputSize        = 5 // below this, staged adds no value
	stagedRespBodyLimit int64 = 16 * 1024
	stagedMemTruncStage2      = 200 // char cap per memory in stage-2 prompt
	stagedMemTruncStage3      = 300 // char cap per memory in stage-3 prompt
)

// stagedShortlistSize / stagedMaxInputSize — moved to tuning.go as
// env-readable accessors (MEMDB_D5_SHORTLIST_SIZE, MEMDB_D5_MAX_INPUT_SIZE).
// Defaults preserved as defaultStagedShortlistSize=10, defaultStagedMaxInputSize=50.

var stagedStage2SystemPrompt = `You are a precision retrieval judge. Given a user's query and N candidate memories, identify the TOP 10 that are most likely to contain the answer.

Output JSON array of exactly 10 memory IDs (strings), in order of relevance (most relevant first):
{"ids": ["id1", "id2", ...]}

If fewer than 10 candidates are genuinely relevant, return fewer. Never invent IDs not in the input.`

var stagedStage3SystemPrompt = `For each memory ID provided, write a 1-sentence justification explaining why it answers the query. If the memory does NOT actually answer the query, set "relevant" to false and justification to "IRRELEVANT".

Output JSON: {"items": [{"id": "...", "justification": "...", "relevant": true}]}

Do not invent IDs. If an input ID doesn't match any memory you saw, omit it.`

// StagedRetrievalConfig carries the LLM endpoint credentials. Mirrors
// LLMRerankConfig so the service can reuse existing plumbing.
type StagedRetrievalConfig struct {
	APIURL string
	APIKey string
	Model  string
}

func stagedRetrievalEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_STAGED") == "true"
}

// RunStagedRetrieval executes stages 2 and 3 on the candidates. Returns a
// reordered slice: justified relevant items first (in stage-2 order), then
// the non-shortlisted remainder preserving original order. On any LLM error
// returns items unchanged (graceful degrade).
func RunStagedRetrieval(ctx context.Context, logger *slog.Logger, query string, items []map[string]any, cfg StagedRetrievalConfig) []map[string]any {
	if !stagedRetrievalEnabled() || cfg.APIURL == "" || len(items) < stagedMinInputSize {
		return items
	}
	candidates := items
	maxIn := stagedMaxInputSize()
	if len(candidates) > maxIn {
		candidates = candidates[:maxIn]
	}

	// Stage 2: refinement
	shortlist, err := stagedStage2Refine(ctx, query, candidates, cfg)
	if err != nil {
		searchMx().D5Staged.Add(ctx, 1, metric.WithAttributes(
			attribute.String("stage", "2_refine"),
			attribute.String("outcome", "error"),
		))
		if logger != nil {
			logger.Debug("staged stage2 failed, returning original", slog.Any("error", err))
		}
		return items
	}
	searchMx().D5Staged.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", "2_refine"),
		attribute.String("outcome", "success"),
	))
	if len(shortlist) == 0 {
		return items
	}

	// Stage 3: justification
	justified, err := stagedStage3Justify(ctx, query, shortlist, candidates, cfg)
	if err != nil {
		searchMx().D5Staged.Add(ctx, 1, metric.WithAttributes(
			attribute.String("stage", "3_justify"),
			attribute.String("outcome", "fallback"),
		))
		if logger != nil {
			logger.Debug("staged stage3 failed, using stage2 output as final", slog.Any("error", err))
		}
		// Fall back to stage 2 without justification filtering.
		return reorderByIDs(items, shortlist)
	}
	searchMx().D5Staged.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", "3_justify"),
		attribute.String("outcome", "success"),
	))

	relevantIDs := make([]string, 0, len(justified))
	for _, j := range justified {
		if j.ID == "" {
			continue
		}
		if j.Relevant {
			searchMx().D5Justified.Add(ctx, 1, metric.WithAttributes(attribute.String("relevance", "relevant")))
			relevantIDs = append(relevantIDs, j.ID)
		} else {
			searchMx().D5Justified.Add(ctx, 1, metric.WithAttributes(attribute.String("relevance", "irrelevant")))
		}
	}
	if len(relevantIDs) == 0 {
		return items
	}
	return reorderByIDs(items, relevantIDs)
}

// stagedStage2Refine returns ordered shortlist of IDs.
func stagedStage2Refine(ctx context.Context, query string, items []map[string]any, cfg StagedRetrievalConfig) ([]string, error) {
	var b strings.Builder
	for i, it := range items {
		id, _ := it["id"].(string)
		mem, _ := it["memory"].(string)
		// Truncate each memory to keep prompt size bounded.
		if len(mem) > stagedMemTruncStage2 {
			mem = mem[:stagedMemTruncStage2] + "..."
		}
		fmt.Fprintf(&b, "[%d] id=%s  %s\n", i+1, id, strings.TrimSpace(mem))
	}
	userMsg := fmt.Sprintf("Query: %s\n\nCandidates:\n%s\n\nReturn JSON {\"ids\":[...]} — top 10 ids, most relevant first.", query, b.String())

	content, err := callStagedLLM(ctx, stagedStage2SystemPrompt, userMsg, cfg)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(llm.StripJSONFence([]byte(content)), &parsed); err != nil {
		return nil, fmt.Errorf("stage2 parse: %w", err)
	}
	// Cap to shortlist size
	shortlistCap := stagedShortlistSize()
	if len(parsed.IDs) > shortlistCap {
		parsed.IDs = parsed.IDs[:shortlistCap]
	}
	return parsed.IDs, nil
}

type stagedJustifiedItem struct {
	ID            string `json:"id"`
	Justification string `json:"justification"`
	Relevant      bool   `json:"relevant"`
}

// stagedStage3Justify returns justified items for the shortlist.
func stagedStage3Justify(ctx context.Context, query string, shortlist []string, allItems []map[string]any, cfg StagedRetrievalConfig) ([]stagedJustifiedItem, error) {
	// Build lookup id → memory text for the shortlisted items.
	byID := make(map[string]string, len(allItems))
	for _, it := range allItems {
		if id, ok := it["id"].(string); ok {
			mem, _ := it["memory"].(string)
			byID[id] = mem
		}
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

	content, err := callStagedLLM(ctx, stagedStage3SystemPrompt, userMsg, cfg)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Items []stagedJustifiedItem `json:"items"`
	}
	if err := json.Unmarshal(llm.StripJSONFence([]byte(content)), &parsed); err != nil {
		return nil, fmt.Errorf("stage3 parse: %w", err)
	}
	return parsed.Items, nil
}

func callStagedLLM(ctx context.Context, systemPrompt, userMsg string, cfg StagedRetrievalConfig) (string, error) {
	payload := map[string]any{
		"model":       cfg.Model,
		"temperature": 0.0,
		"max_tokens":  stagedMaxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMsg},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, stagedTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		cfg.APIURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := &http.Client{Timeout: stagedTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("staged llm: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, stagedRespBodyLimit))
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
	if err := json.Unmarshal(raw, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", errors.New("staged llm: bad response")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// reorderByIDs places items with IDs in `order` first (in that order),
// followed by the remainder preserving original ordering.
func reorderByIDs(items []map[string]any, order []string) []map[string]any {
	orderIdx := make(map[string]int, len(order))
	for i, id := range order {
		orderIdx[id] = i
	}
	shortlisted := make([]map[string]any, 0, len(order))
	rest := make([]map[string]any, 0, len(items))
	for _, it := range items {
		id, _ := it["id"].(string)
		if _, ok := orderIdx[id]; ok {
			shortlisted = append(shortlisted, it)
		} else {
			rest = append(rest, it)
		}
	}
	// Sort shortlisted by stage-2 order.
	for i := 0; i < len(shortlisted); i++ {
		for j := i + 1; j < len(shortlisted); j++ {
			idI, _ := shortlisted[i]["id"].(string)
			idJ, _ := shortlisted[j]["id"].(string)
			if orderIdx[idI] > orderIdx[idJ] {
				shortlisted[i], shortlisted[j] = shortlisted[j], shortlisted[i]
			}
		}
	}
	return append(shortlisted, rest...)
}
