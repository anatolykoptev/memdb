// Package rerank — cross-encoder strategy (precompute lookup + live fallback).
//
// Ports cross_encoder_adapter.go + cross_encoder_precompute.go from the
// parent search pkg into a typed Reranker. Three execution paths:
//
//  1. Full hit (M10 Stream 6, default ON): if items[0] (the cosine
//     anchor) has a ce_score_topk cache covering every other item, we
//     reorder by the cached pair scores and skip the live HTTP call entirely.
//  2. Partial hit (M14 Y2 fix): some items hit cache, remaining items fire
//     a single batched live CE call. Merged and sorted by score. Emits
//     outcome="partial" metric so operators see partial-hit rate separately.
//  3. Live fallback: anchor without cache / env-disabled / too-few items /
//     stale cache → full live HTTP call.
//
// Metric hooks (OnLiveCall, OnPrecompute) let the parent search pkg attach
// the legacy memdb.search.ce_live_call_total + .ce_precompute_hit_total
// counters without leaking otel imports into this package's caller paths.
// nil hooks are no-ops.
package rerank

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-kit/rerank"
)

// ceQualityFloorEnv gates the low-quality CE → MathReranker fallback. CE
// returning StatusOk with top-1 score below this floor is treated as a
// hallucination (gte-multi-rerank's known failure mode on narrow OOD queries)
// and routes through cosine instead. Default 0.1 matches go-search.
// Set to 0 to disable the quality gate entirely (preserve pre-#14 behavior).
const ceQualityFloorEnv = "MEMDB_CE_QUALITY_FLOOR"
const ceQualityFloorDefault = 0.1

func ceQualityFloor() float64 {
	raw := strings.TrimSpace(os.Getenv(ceQualityFloorEnv))
	if raw == "" {
		return ceQualityFloorDefault
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return ceQualityFloorDefault
	}
	return v
}

// CrossEncoder reranks via a precompute lookup with live HTTP fallback.
// Disabled (skip lookup, always live) when MEMDB_CE_PRECOMPUTE=false.
//
// The strategy is constructed per-search; Client may be nil (in which case
// Rerank skips entirely — chain treats it as skipped).
type CrossEncoder struct {
	Client *rerank.Client
	// OnLiveCall fires when the live HTTP path executes (regardless of
	// the reason — miss, anchor without cache, env-disabled).
	OnLiveCall func(ctx context.Context)
	// OnPrecompute fires for every lookup outcome (hit | partial | miss | stale).
	OnPrecompute func(ctx context.Context, outcome string)
	// OnMathFallback fires when the live CE call routes through MathReranker
	// (cosine on QueryVec) — either because go-kit returned StatusDegraded
	// (timeout, 5xx, ErrCircuitOpen) OR because top-1 score fell below the
	// quality floor. Reason is "degraded" or "low_quality"; nil = skip metric.
	OnMathFallback func(ctx context.Context, reason string)
	// QueryUserID, QuerySessionID, QueryTags carry the query-time identity
	// context used by the metadata boost post-step (Q3 stream). Empty
	// strings / nil slice = no boost for that factor.
	QueryUserID    string
	QuerySessionID string
	QueryTags      []string
	// BoostWeights configures the per-factor multipliers. Zero value
	// (all fields = 0) disables all boosting silently. Use
	// DefaultBoostWeights() for MemOS-parity defaults.
	BoostWeights BoostWeights
	// QueryVec and EmbeddingsByID enable the math fallback path: when the
	// live CE call returns degraded or near-zero scores, MathReranker
	// reorders items by cosine on these vectors. Nil/empty = math fallback
	// disabled (CE-only behavior, parity with pre-fallback ports).
	//
	// Pattern lifted from go-search ce_rerank.go (PRs #10 + #14): CE
	// hallucinates near-zero scores on narrow OOD queries even when cosine
	// says the docs are highly relevant. On those queries the
	// require_positive_ce / threshold gate downstream drops every result —
	// math fallback keeps the pipeline producing scores.
	QueryVec       []float32
	EmbeddingsByID map[string][]float32
}

// Name implements Reranker.
func (CrossEncoder) Name() string { return "cross_encoder" }

// Rerank implements Reranker.
//
// Skip → no client / empty batch.
// Live (no lookup) → 1 item, env-disabled, anchor without ID, missing / stale cache,
// or zero cached neighbours in the result set.
// Partial hit (M14 Y2) → some neighbours cached, rest batched to live CE once.
// Full hit → all neighbours present in cache; no live call.
//
// After CE scoring (whichever path), applyMetadataBoost multiplies scores
// by (1 + matched weight factors) per the Q3 spec. Zero BoostWeights or
// empty query identity strings produce no-ops without branches.
func (ce CrossEncoder) Rerank(ctx context.Context, query string, items []Item) ([]Item, error) {
	out, err := ce.rerank(ctx, query, items)
	if err != nil || out == nil {
		return out, err
	}
	applyMetadataBoost(ctx, out, ce.QueryUserID, ce.QuerySessionID, ce.QueryTags, ce.BoostWeights)
	return out, nil
}

// rerank is the inner CE scoring logic (precompute lookup + live fallback).
// applyMetadataBoost is applied by the public Rerank wrapper above.
func (ce CrossEncoder) rerank(ctx context.Context, query string, items []Item) ([]Item, error) {
	if ce.Client == nil || !ce.Client.Available() || len(items) == 0 {
		RecordSkipped(ctx, ce.Name())
		return items, nil
	}

	// Single-item batch — precompute lookup needs ≥2 items (anchor +
	// neighbour). Defer to live, which runs the HTTP path even for n=1
	// (parity with legacy rerankMemoryItems).
	if !cePrecomputeEnabled() || len(items) < 2 {
		out := ce.runLive(ctx, query, items)
		RecordSuccess(ctx, ce.Name())
		return out, nil
	}

	anchorID := items[0].ID()
	if anchorID == "" {
		out := ce.runLive(ctx, query, items)
		RecordSuccess(ctx, ce.Name())
		return out, nil
	}

	scoreByID, hasStale := extractCEScoreTopK(items[0])
	if scoreByID == nil {
		if hasStale {
			ce.fireOnPrecompute(ctx, "stale")
		} else {
			ce.fireOnPrecompute(ctx, "miss")
		}
		out := ce.runLive(ctx, query, items)
		RecordSuccess(ctx, ce.Name())
		return out, nil
	}
	if hasStale {
		ce.fireOnPrecompute(ctx, "stale")
		out := ce.runLive(ctx, query, items)
		RecordSuccess(ctx, ce.Name())
		return out, nil
	}

	// Walk non-anchor items: collect cached scores and separate uncached items.
	// M14 Y2: use-what-you-have — no longer bail on first miss.
	type indexed struct {
		score    float32
		item     Item
		fromLive bool // true = score came from live CE call
	}
	rest := make([]indexed, 0, len(items)-1)
	var uncachedItems []Item    // items with no cache entry → need live CE
	var uncachedRestIdx []int   // parallel index into rest[] for uncachedItems

	for i := 1; i < len(items); i++ {
		id := items[i].ID()
		if id == "" {
			// No ID — cannot match against cache; treat as uncached.
			rest = append(rest, indexed{item: items[i], fromLive: true})
			uncachedItems = append(uncachedItems, items[i])
			uncachedRestIdx = append(uncachedRestIdx, len(rest)-1)
			continue
		}
		s, ok := scoreByID[id]
		if ok {
			rest = append(rest, indexed{score: s, item: items[i]})
		} else {
			rest = append(rest, indexed{item: items[i], fromLive: true})
			uncachedItems = append(uncachedItems, items[i])
			uncachedRestIdx = append(uncachedRestIdx, len(rest)-1)
		}
	}

	if len(uncachedItems) == len(items)-1 {
		// Nothing was cached — full live call (emit miss, not partial).
		ce.fireOnPrecompute(ctx, "miss")
		out := ce.runLive(ctx, query, items)
		RecordSuccess(ctx, ce.Name())
		return out, nil
	}

	if len(uncachedItems) > 0 {
		// Partial hit: fire live CE only for the uncached subset.
		liveScored := ce.runLiveSubset(ctx, query, uncachedItems)
		for k, ri := range uncachedRestIdx {
			rest[ri].score = float32(liveScored[k].Score())
		}
		ce.fireOnPrecompute(ctx, "partial")
	} else {
		// All items had cached scores — full hit.
		ce.fireOnPrecompute(ctx, "hit")
	}

	// Insertion-sort rest DESC by score. Anchor stays at index 0.
	for i := 1; i < len(rest); i++ {
		j := i
		for j > 0 && rest[j-1].score < rest[j].score {
			rest[j-1], rest[j] = rest[j], rest[j-1]
			j--
		}
	}

	out := make([]Item, 0, len(items))
	out = append(out, items[0])
	items[0].SetMeta("cross_encoder_reranked", true)
	for _, r := range rest {
		r.item.SetScore(float64(r.score))
		r.item.SetMeta("cross_encoder_reranked", true)
		out = append(out, r.item)
	}

	RecordSuccess(ctx, ce.Name())
	return out, nil
}

// runLive performs the live HTTP rerank call. Mirrors rerankMemoryItems
// from the legacy adapter.
func (ce CrossEncoder) runLive(ctx context.Context, query string, items []Item) []Item {
	if len(items) == 0 {
		return items
	}
	docs := make([]rerank.Doc, 0, len(items))
	idxByID := make(map[string]int, len(items))
	for i, it := range items {
		text := it.EmbeddingText()
		if text == "" {
			continue
		}
		id := fmt.Sprintf("%d", i)
		// Carry the embed vector when we have one — required by the math
		// fallback path. EmbeddingsByID may be nil (legacy callers) or
		// missing this item; in that case Doc.EmbedVector is nil and the
		// fallback path will skip cosine scoring for it.
		var vec []float32
		if ce.EmbeddingsByID != nil {
			vec = ce.EmbeddingsByID[it.ID()]
		}
		docs = append(docs, rerank.Doc{ID: id, Text: text, EmbedVector: vec})
		idxByID[id] = i
	}
	if len(docs) == 0 {
		return items
	}

	if ce.OnLiveCall != nil {
		ce.OnLiveCall(ctx)
	}
	scored := ce.rerankWithMathFallback(ctx, query, docs)

	seen := make(map[int]bool, len(scored))
	out := make([]Item, 0, len(items))
	for _, s := range scored {
		origIdx, ok := idxByID[s.ID]
		if !ok {
			continue
		}
		items[origIdx].SetScore(float64(s.Score))
		items[origIdx].SetMeta("cross_encoder_reranked", true)
		out = append(out, items[origIdx])
		seen[origIdx] = true
	}
	for i, it := range items {
		if !seen[i] {
			out = append(out, it)
		}
	}
	return out
}

// rerankWithMathFallback is the CE-or-cosine wrapper. Two fallback triggers:
//
//  1. Availability: ceClient returns StatusDegraded (timeout, 5xx,
//     ErrCircuitOpen). Pattern from go-search PR #10.
//  2. Quality: ceClient returns StatusOk but the top-1 score is below
//     MEMDB_CE_QUALITY_FLOOR. Pattern from go-search PR #14 — gte-multi-rerank
//     hallucinates near-zero scores on narrow OOD queries even when cosine
//     similarity is 0.9+. Without this fallback the require_positive_ce
//     gate / threshold filter downstream drops every result.
//
// When either trigger fires AND a non-empty QueryVec is set, MathReranker
// reorders by cosine on Doc.EmbedVector. Otherwise the CE result is returned
// as-is so existing tests / nil-vec callers keep their behavior.
func (ce CrossEncoder) rerankWithMathFallback(ctx context.Context, query string, docs []rerank.Doc) []rerank.Scored {
	res, err := ce.Client.RerankWithResult(ctx, query, docs)

	ceDegraded := res == nil || res.Status == rerank.StatusDegraded
	ceLowQuality := false
	floor := ceQualityFloor()
	if !ceDegraded && floor > 0 && len(res.Scored) > 0 {
		topScore := float64(res.Scored[0].Score)
		for _, s := range res.Scored {
			if float64(s.Score) > topScore {
				topScore = float64(s.Score)
			}
		}
		ceLowQuality = topScore < floor
	}

	// Healthy path — return CE scores directly.
	if !ceDegraded && !ceLowQuality {
		return res.Scored
	}

	// No query vector → can't run math fallback. Return what CE gave (if
	// anything) so the caller still has a stable shape to work with.
	if len(ce.QueryVec) == 0 {
		if res != nil {
			return res.Scored
		}
		return nil
	}

	reason := "degraded"
	if ceLowQuality {
		reason = "low_quality"
	}
	slog.Warn("ce_rerank: falling back to MathReranker",
		slog.String("reason", reason),
		slog.Any("error", err),
	)
	if ce.OnMathFallback != nil {
		ce.OnMathFallback(ctx, reason)
	}
	return mathRerankCosine(ce.QueryVec, docs)
}

// mathRerankCosine reorders docs by cosine similarity to queryVec. Docs
// without an EmbedVector get score 0 and sink to the bottom in original
// order. Drop-in replacement for ce.Client.Rerank when the live path
// hallucinates or fails.
func mathRerankCosine(queryVec []float32, docs []rerank.Doc) []rerank.Scored {
	if len(queryVec) == 0 || len(docs) == 0 {
		return nil
	}
	scored := make([]rerank.Scored, 0, len(docs))
	for i, d := range docs {
		score := float32(0)
		if len(d.EmbedVector) == len(queryVec) && len(queryVec) > 0 {
			score = cosineFloat32(queryVec, d.EmbedVector)
		}
		scored = append(scored, rerank.Scored{Doc: d, Score: score, OrigRank: i})
	}
	// Insertion sort DESC by score — small N (chat top-K is ≤30 typically).
	for i := 1; i < len(scored); i++ {
		j := i
		for j > 0 && scored[j-1].Score < scored[j].Score {
			scored[j-1], scored[j] = scored[j], scored[j-1]
			j--
		}
	}
	return scored
}

// cosineFloat32 is a minimal pure-Go cosine. Returns 0 on zero-norm input.
// Kept local to avoid a go-kit/rerank import cycle for this single helper.
func cosineFloat32(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(float64(dot) / math.Sqrt(float64(na)*float64(nb)))
}

// runLiveSubset calls the live HTTP rerank backend for a subset of items
// (the uncached portion in a partial-hit). Returns items in the order the
// CE backend scored them, with SetScore applied. Items without text are
// returned with score 0 and no mutation — mirrors runLive behaviour for
// no-text items. Fires OnLiveCall exactly once.
func (ce CrossEncoder) runLiveSubset(ctx context.Context, query string, items []Item) []Item {
	if len(items) == 0 {
		return items
	}
	docs := make([]rerank.Doc, 0, len(items))
	idxByID := make(map[string]int, len(items))
	for i, it := range items {
		text := it.EmbeddingText()
		if text == "" {
			continue
		}
		id := fmt.Sprintf("%d", i)
		var vec []float32
		if ce.EmbeddingsByID != nil {
			vec = ce.EmbeddingsByID[it.ID()]
		}
		docs = append(docs, rerank.Doc{ID: id, Text: text, EmbedVector: vec})
		idxByID[id] = i
	}

	out := make([]Item, len(items))
	copy(out, items)

	if len(docs) == 0 {
		return out
	}

	if ce.OnLiveCall != nil {
		ce.OnLiveCall(ctx)
	}
	scored := ce.rerankWithMathFallback(ctx, query, docs)

	for _, s := range scored {
		origIdx, ok := idxByID[s.ID]
		if !ok {
			continue
		}
		out[origIdx].SetScore(float64(s.Score))
	}
	return out
}

func (ce CrossEncoder) fireOnPrecompute(ctx context.Context, outcome string) {
	if ce.OnPrecompute != nil {
		ce.OnPrecompute(ctx, outcome)
	}
}

// cePrecomputeEnabled gates the lookup-first path. Default TRUE.
// Set MEMDB_CE_PRECOMPUTE=false to force live every time (used for A/B).
func cePrecomputeEnabled() bool {
	return os.Getenv("MEMDB_CE_PRECOMPUTE") != "false"
}

// extractCEScoreTopK pulls the cached neighbour map out of an item's
// ce_score_topk metadata field. Returns (nil, false) when absent;
// (nil, true) when present but every entry malformed; (scores, true)
// for partial parse; (scores, false) when fully clean.
//
// Tolerates float64 / float32 / json.Number for the score field — covers
// in-process map[string]any and JSON-decoded paths.
func extractCEScoreTopK(item Item) (map[string]float32, bool) {
	raw, ok := item.GetMeta("ce_score_topk")
	if !ok || raw == nil {
		return nil, false
	}
	var entries []map[string]any
	switch v := raw.(type) {
	case []any:
		entries = make([]map[string]any, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				entries = append(entries, m)
			}
		}
	case []map[string]any:
		entries = v
	case string:
		if v == "" {
			return nil, false
		}
		_ = json.Unmarshal([]byte(v), &entries)
	default:
		return nil, false
	}

	if len(entries) == 0 {
		return nil, false
	}
	out := make(map[string]float32, len(entries))
	hasInvalid := false
	for _, e := range entries {
		nid, _ := e["neighbor_id"].(string)
		if nid == "" {
			hasInvalid = true
			continue
		}
		switch s := e["score"].(type) {
		case float64:
			out[nid] = float32(s)
		case float32:
			out[nid] = s
		case json.Number:
			f, err := s.Float64()
			if err == nil {
				out[nid] = float32(f)
			} else {
				hasInvalid = true
			}
		default:
			hasInvalid = true
		}
	}
	if len(out) == 0 {
		return nil, hasInvalid
	}
	return out, hasInvalid
}
