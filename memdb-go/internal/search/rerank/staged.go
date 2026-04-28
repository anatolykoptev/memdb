// Package rerank — staged retrieval (D5).
//
// Ports staged_retrieval.go from the parent search pkg. Two-stage refine
// (top-10 shortlist) → justify (drop irrelevants), each a single LLM call.
// Behaviour parity with the legacy RunStagedRetrieval entrypoint:
//   - Env-gated by MEMDB_SEARCH_STAGED=true (default off → skipped).
//   - Reuses LLMConfig credentials.
//   - Graceful degrade on any LLM error: returns input unchanged.
//   - Never invents IDs — reorderByIDs tolerates ids absent from the input.
package rerank

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	stagedTimeout              = 15 * time.Second
	stagedMaxTokens            = 800
	stagedMinInputSize         = 5 // below this, staged adds no value
	stagedRespBodyLimit  int64 = 16 * 1024
	stagedMemTruncStage2       = 200
	stagedMemTruncStage3       = 300

	stagedStage2SystemPrompt = `You are a precision retrieval judge. Given a user's query and N candidate memories, identify the TOP 10 that are most likely to contain the answer.

Output JSON array of exactly 10 memory IDs (strings), in order of relevance (most relevant first):
{"ids": ["id1", "id2", ...]}

If fewer than 10 candidates are genuinely relevant, return fewer. Never invent IDs not in the input.`

	stagedStage3SystemPrompt = `For each memory ID provided, write a 1-sentence justification explaining why it answers the query. If the memory does NOT actually answer the query, set "relevant" to false and justification to "IRRELEVANT".

Output JSON: {"items": [{"id": "...", "justification": "...", "relevant": true}]}

Do not invent IDs. If an input ID doesn't match any memory you saw, omit it.`
)

// Staged is the D5 two-stage rerank strategy.
//
// ShortlistSize / MaxInputSize are wired from the parent search package's
// env-readable accessors at construction time so the legacy
// MEMDB_D5_SHORTLIST_SIZE / MEMDB_D5_MAX_INPUT_SIZE knobs keep working.
// Defaults match the legacy values when zero is passed.
//
// Hook callbacks let the parent register otel metric writes without
// pulling otel into this package's transport.
type Staged struct {
	Config        LLMConfig
	Logger        *slog.Logger
	ShortlistSize int // 0 → 10
	MaxInputSize  int // 0 → 50
	OnStage       func(ctx context.Context, stage, outcome string)
	OnJustified   func(ctx context.Context, relevance string)
	// OnStageSize fires at each stage entry with the candidate count.
	// stage ∈ {0_cosine_prefilter, 2_refine, 3_justify}. The
	// 0_cosine_prefilter label reflects that cosine is the upstream
	// pre-filter feeding D5 (NOT a stage of D5 itself) — Q5 followup
	// renamed this from the misleading "1_cosine" so dashboards
	// stop suggesting D5 owns three stages. Optional (nil → no-op).
	OnStageSize func(ctx context.Context, stage string, size int)
}

// Name implements Reranker.
func (Staged) Name() string { return "staged" }

// Rerank implements Reranker.
func (s Staged) Rerank(ctx context.Context, query string, items []Item) ([]Item, error) {
	if !stagedEnabled() || s.Config.APIURL == "" || len(items) < stagedMinInputSize {
		RecordSkipped(ctx, s.Name())
		return items, nil
	}
	candidates := items
	maxIn := s.maxInput()
	if len(candidates) > maxIn {
		candidates = candidates[:maxIn]
	}

	// Cosine pre-filter (Q5 followup: was misleadingly labelled "1_cosine"
	// suggesting D5 stage-1; cosine actually runs upstream and feeds D5).
	// Record the candidate count entering D5 under the corrected label.
	s.fireStageSize(ctx, "0_cosine_prefilter", len(candidates))

	// Stage 2: refine (LLM shortlist).
	s.fireStageSize(ctx, "2_refine", len(candidates))
	shortlist, err := s.stage2Refine(ctx, query, candidates)
	if err != nil {
		s.fireStage(ctx, "2_refine", "error")
		s.debug("staged stage2 failed, returning original", err)
		return items, nil
	}
	s.fireStage(ctx, "2_refine", "success")
	if len(shortlist) == 0 {
		RecordSuccess(ctx, s.Name())
		return items, nil
	}

	// Stage 3: justify (LLM relevance filter).
	s.fireStageSize(ctx, "3_justify", len(shortlist))
	justified, err := s.stage3Justify(ctx, query, shortlist, candidates)
	if err != nil {
		s.fireStage(ctx, "3_justify", "fallback")
		s.debug("staged stage3 failed, using stage2 output as final", err)
		out := reorderByIDs(items, shortlist)
		RecordSuccess(ctx, s.Name())
		return out, nil
	}
	s.fireStage(ctx, "3_justify", "success")

	relevantIDs := make([]string, 0, len(justified))
	for _, j := range justified {
		if j.ID == "" {
			continue
		}
		if j.Relevant {
			s.fireJustified(ctx, "relevant")
			relevantIDs = append(relevantIDs, j.ID)
		} else {
			s.fireJustified(ctx, "irrelevant")
		}
	}
	if len(relevantIDs) == 0 {
		RecordSuccess(ctx, s.Name())
		return items, nil
	}
	out := reorderByIDs(items, relevantIDs)
	RecordSuccess(ctx, s.Name())
	return out, nil
}

// stage2Refine returns the ordered shortlist of IDs.
func (s Staged) stage2Refine(ctx context.Context, query string, items []Item) ([]string, error) {
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
	if err := callStagedLLM(ctx, s.Config, stagedStage2SystemPrompt, userMsg, &parsed); err != nil {
		return nil, fmt.Errorf("stage2: %w", err)
	}
	cap := s.shortlist()
	if len(parsed.IDs) > cap {
		parsed.IDs = parsed.IDs[:cap]
	}
	return parsed.IDs, nil
}

type stagedJustifiedItem struct {
	ID            string `json:"id"`
	Justification string `json:"justification"`
	Relevant      bool   `json:"relevant"`
}

// stage3Justify returns justified items for the shortlist.
func (s Staged) stage3Justify(ctx context.Context, query string, shortlist []string, allItems []Item) ([]stagedJustifiedItem, error) {
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
	if err := callStagedLLM(ctx, s.Config, stagedStage3SystemPrompt, userMsg, &parsed); err != nil {
		return nil, fmt.Errorf("stage3: %w", err)
	}
	return parsed.Items, nil
}

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

// reorderByIDs places items with IDs in `order` first (in that order),
// followed by the remainder preserving original ordering.
func reorderByIDs(items []Item, order []string) []Item {
	orderIdx := make(map[string]int, len(order))
	for i, id := range order {
		orderIdx[id] = i
	}
	shortlisted := make([]Item, 0, len(order))
	rest := make([]Item, 0, len(items))
	for _, it := range items {
		if _, ok := orderIdx[it.ID()]; ok {
			shortlisted = append(shortlisted, it)
		} else {
			rest = append(rest, it)
		}
	}
	for i := 0; i < len(shortlisted); i++ {
		for j := i + 1; j < len(shortlisted); j++ {
			if orderIdx[shortlisted[i].ID()] > orderIdx[shortlisted[j].ID()] {
				shortlisted[i], shortlisted[j] = shortlisted[j], shortlisted[i]
			}
		}
	}
	return append(shortlisted, rest...)
}

func stagedEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_STAGED") == "true"
}

func (s Staged) shortlist() int {
	if s.ShortlistSize > 0 {
		return s.ShortlistSize
	}
	return 10
}

func (s Staged) maxInput() int {
	if s.MaxInputSize > 0 {
		return s.MaxInputSize
	}
	return 50
}

func (s Staged) fireStage(ctx context.Context, stage, outcome string) {
	if s.OnStage != nil {
		s.OnStage(ctx, stage, outcome)
	}
}

func (s Staged) fireJustified(ctx context.Context, relevance string) {
	if s.OnJustified != nil {
		s.OnJustified(ctx, relevance)
	}
}

func (s Staged) debug(msg string, err error) {
	if s.Logger != nil {
		s.Logger.Debug(msg, slog.Any("error", err))
	}
}

func (s Staged) fireStageSize(ctx context.Context, stage string, size int) {
	if s.OnStageSize != nil {
		s.OnStageSize(ctx, stage, size)
	}
}
