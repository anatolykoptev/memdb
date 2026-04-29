// Package rerank — staged retrieval backends (M12.6).
//
// The staged D5 retrieval was originally a two-stage LLM call sequence
// (refine → justify). On the LoCoMo benchmark this was the single most
// expensive component: ~4304 calls × 4s ≈ 4.8h per full run. Stage 2
// returns top-K IDs ordered by relevance — pure scoring, perfectly
// replaceable by a cross-encoder. Stage 3 returns per-ID `relevant: bool`
// + an unused free-text justification — also reducible to a CE threshold.
//
// This file factors the two LLM calls behind a StagedBackend interface
// and provides three implementations:
//
//   - LLMBackend  — original behaviour (preserved exactly).
//   - CEBackend   — both stages via embed-server bge-reranker-v2-m3.
//   - HybridBackend — stage 2 via CE (cheap top-K), stage 3 via LLM
//                     (keeps the structured judgement path for cases
//                     where a CE threshold is too coarse).
//
// Backend selection is gated by MEMDB_STAGED_BACKEND ∈ {ce, llm, hybrid}.
// Unset / empty / unknown → ce (default), but only when a *rerank.Client
// is wired AND it is Available(); otherwise we fall through to LLM so
// existing call sites that did not migrate keep functioning.
//
// Both backends emit the SAME OnStage / OnJustified hooks the original
// LLM-only path emitted, so the dashboards (`memdb.search.d5_staged_total`
// and `memdb.search.d5_justified_total`) keep their series — operators
// only see a latency drop, not a metric reshape.
package rerank

import (
	"context"
	"os"
	"strconv"

	"github.com/anatolykoptev/go-kit/rerank"
)

// StagedBackend is the abstraction `Staged.Rerank` calls into for the
// two scoring steps. Implementations MUST be best-effort: any error
// is returned and the caller falls through to "no rerank" (input order
// preserved). Stage2Refine returns an ordered list of candidate IDs
// (most relevant first, ≤ shortlistCap). Stage3Justify decides per-ID
// relevance on the shortlist; a missing ID counts as "not relevant"
// (filtered out) — matching the existing LLM behaviour.
type StagedBackend interface {
	// Name is the backend label exported on metric attributes
	// (`backend=ce|llm|hybrid`). Stable; do not change.
	Name() string

	// Stage2Refine returns a top-K ID list ordered by relevance,
	// where K = shortlistCap. Returning fewer is allowed (sparse
	// candidates / threshold cutoff). Errors propagate.
	Stage2Refine(ctx context.Context, query string, items []Item, shortlistCap int) ([]string, error)

	// Stage3Justify decides per-shortlist-ID relevance. The returned
	// slice is the SAME shape the LLM produced — IDs missing from
	// the result count as filtered. The "Justification" field is
	// empty for CE-driven backends (downstream code does not read
	// it; only `Relevant` and `ID` are consumed in staged.go).
	Stage3Justify(ctx context.Context, query string, shortlist []string, allItems []Item) ([]stagedJustifiedItem, error)
}

// LLMBackend preserves the original two-LLM-call behaviour. Extracted
// verbatim from the inline implementation in staged.go.
type LLMBackend struct {
	Config LLMConfig
}

// Name implements StagedBackend.
func (LLMBackend) Name() string { return "llm" }

// Stage2Refine implements StagedBackend.
func (b LLMBackend) Stage2Refine(ctx context.Context, query string, items []Item, shortlistCap int) ([]string, error) {
	return llmStage2Refine(ctx, b.Config, query, items, shortlistCap)
}

// Stage3Justify implements StagedBackend.
func (b LLMBackend) Stage3Justify(ctx context.Context, query string, shortlist []string, allItems []Item) ([]stagedJustifiedItem, error) {
	return llmStage3Justify(ctx, b.Config, query, shortlist, allItems)
}

// CEBackend reranks both staged-retrieval stages via embed-server's
// /v1/rerank endpoint (currently gte-multi-rerank in production; the
// model name comes from the rerank.Client config, this code is
// model-agnostic). Stage 2 = top-K by score. Stage 3 = per-ID
// `relevant = score >= CEThreshold`.
type CEBackend struct {
	Client *rerank.Client
	// CEThreshold is the score cutoff above which a doc is
	// considered relevant in stage 3. Default 0.0 (above-neutral).
	// Override via MEMDB_STAGED_CE_THRESHOLD. Tune by inspecting
	// the score distribution on your corpus — gte-multi-rerank
	// scores cluster differently from bge-reranker-v2-m3.
	CEThreshold float32
}

// Name implements StagedBackend.
func (CEBackend) Name() string { return "ce" }

// Stage2Refine implements StagedBackend. Delegates to the live HTTP
// rerank client; treats Client==nil / unavailable as a hard error so
// the caller falls back to "no rerank" (input order preserved).
func (b CEBackend) Stage2Refine(ctx context.Context, query string, items []Item, shortlistCap int) ([]string, error) {
	scored, err := ceScoreItems(ctx, b.Client, query, items)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, shortlistCap)
	for i, s := range scored {
		if i >= shortlistCap {
			break
		}
		if s.id == "" {
			continue
		}
		ids = append(ids, s.id)
	}
	return ids, nil
}

// Stage3Justify implements StagedBackend. Reranks the shortlist (only)
// and applies the CEThreshold to produce per-ID relevance booleans.
// The shape matches the LLM judge's so downstream code is identical.
func (b CEBackend) Stage3Justify(ctx context.Context, query string, shortlist []string, allItems []Item) ([]stagedJustifiedItem, error) {
	if len(shortlist) == 0 {
		return nil, nil
	}
	byID := make(map[string]Item, len(allItems))
	for _, it := range allItems {
		byID[it.ID()] = it
	}
	subset := make([]Item, 0, len(shortlist))
	for _, id := range shortlist {
		if it, ok := byID[id]; ok {
			subset = append(subset, it)
		}
	}
	if len(subset) == 0 {
		return nil, nil
	}
	scored, err := ceScoreItems(ctx, b.Client, query, subset)
	if err != nil {
		return nil, err
	}
	out := make([]stagedJustifiedItem, 0, len(scored))
	for _, s := range scored {
		if s.id == "" {
			continue
		}
		out = append(out, stagedJustifiedItem{
			ID:       s.id,
			Relevant: s.score >= b.CEThreshold,
		})
	}
	return out, nil
}

// HybridBackend uses CE for the cheap top-K refine and LLM for the
// per-ID justification — tradeoff for callers who want the LLM's
// nuanced relevance judgement preserved while saving ~50% on stage 2
// LLM cost (the larger candidate set).
type HybridBackend struct {
	Client      *rerank.Client
	Config      LLMConfig
	CEThreshold float32
}

// Name implements StagedBackend.
func (HybridBackend) Name() string { return "hybrid" }

// Stage2Refine implements StagedBackend (CE).
func (b HybridBackend) Stage2Refine(ctx context.Context, query string, items []Item, shortlistCap int) ([]string, error) {
	ce := CEBackend{Client: b.Client, CEThreshold: b.CEThreshold}
	return ce.Stage2Refine(ctx, query, items, shortlistCap)
}

// Stage3Justify implements StagedBackend (LLM).
func (b HybridBackend) Stage3Justify(ctx context.Context, query string, shortlist []string, allItems []Item) ([]stagedJustifiedItem, error) {
	return llmStage3Justify(ctx, b.Config, query, shortlist, allItems)
}

// ceScored holds (id, raw bge score) for a single doc, in CE-ranked
// order. Used internally by both CE backends.
type ceScored struct {
	id    string
	score float32
}

// ceScoreItems calls the rerank client and returns the items ordered
// by relevance score (descending). Inputs without an ID() are dropped
// silently — the LLM path also skipped invented IDs, so symmetry holds.
// Empty embedding text is filtered.
func ceScoreItems(ctx context.Context, client *rerank.Client, query string, items []Item) ([]ceScored, error) {
	if client == nil || !client.Available() {
		return nil, errCEUnavailable
	}
	if len(items) == 0 {
		return nil, nil
	}
	docs := make([]rerank.Doc, 0, len(items))
	idByDocID := make(map[string]string, len(items))
	for i, it := range items {
		text := it.EmbeddingText()
		if text == "" {
			continue
		}
		docID := strconv.Itoa(i)
		docs = append(docs, rerank.Doc{ID: docID, Text: text})
		idByDocID[docID] = it.ID()
	}
	if len(docs) == 0 {
		return nil, nil
	}
	scored := client.Rerank(ctx, query, docs)
	out := make([]ceScored, 0, len(scored))
	for _, s := range scored {
		out = append(out, ceScored{
			id:    idByDocID[s.ID],
			score: s.Score,
		})
	}
	return out, nil
}

// errCEUnavailable is returned by CE backends when the rerank client
// is missing — the caller (Staged) treats it as a stage error and
// falls back gracefully (returns input unchanged), so this does NOT
// silently disable staged retrieval.
var errCEUnavailable = ceUnavailableError{}

type ceUnavailableError struct{}

func (ceUnavailableError) Error() string { return "rerank: cross-encoder client unavailable" }

// stagedBackendName parses MEMDB_STAGED_BACKEND. Returns "" for unset
// so callers can apply their own defaulting logic.
func stagedBackendName() string {
	switch os.Getenv("MEMDB_STAGED_BACKEND") {
	case "ce", "llm", "hybrid":
		return os.Getenv("MEMDB_STAGED_BACKEND")
	}
	return ""
}


// stagedCEThreshold parses MEMDB_STAGED_CE_THRESHOLD. Default 0.0 — the
// bge-reranker-v2-m3 sigmoid-0.5 boundary in raw-logit space (the model
// was trained with binary cross-entropy so logit > 0 ≈ "relevant").
func stagedCEThreshold() float32 {
	raw := os.Getenv("MEMDB_STAGED_CE_THRESHOLD")
	if raw == "" {
		return 0.0
	}
	v, err := strconv.ParseFloat(raw, 32)
	if err != nil {
		return 0.0
	}
	return float32(v)
}
