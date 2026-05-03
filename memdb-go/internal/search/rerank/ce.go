// Package rerank — cross-encoder strategy (precompute lookup + live fallback).
//
// Three execution paths handled by the dispatcher in this file:
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
// File map (single-responsibility split, 2026-04-30):
//
//   ce.go                  — type CrossEncoder + Rerank/rerank dispatcher
//   ce_live.go             — runLive / runLiveSubset (HTTP transport)
//   ce_math_fallback.go    — degraded / low-quality CE → MathReranker
//   ce_precompute.go       — fireOnPrecompute, cache extraction helpers
//
// Metric hooks (OnLiveCall, OnPrecompute, OnMathFallback) let the parent
// search pkg attach memdb.search.* counters without leaking otel imports
// into this package's caller paths. nil hooks are no-ops.
package rerank

import (
	"context"

	"github.com/anatolykoptev/go-kit/rerank"
)

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
	// QueryCubeID is the search request's CubeID. Used by the per-cube
	// CE low_spread circuit breaker (ce_circuit_breaker.go) to short-
	// circuit live CE calls on cubes whose recent history shows CE
	// returns no ranking signal. Empty string disables the breaker for
	// this call (no per-cube tracking possible).
	QueryCubeID string
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
// missing query identity fields → boost is a no-op.
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
