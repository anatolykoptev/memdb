// Package search — service_multihop.go: D2 multi-hop graph expansion.
//
// After VectorSearch returns top-K seeds, walk the memory_edges table up
// to maxHop steps (default 2) to surface related memories. Each expanded
// neighbor is scored as cosine(query, neighbor_embedding) * 0.8^hop so it
// competes on the same scale as the seeds (which get re-scored to cosine
// by ReRankByCosine downstream). Pool is capped at 2× the original size.
// Gated by MEMDB_SEARCH_MULTIHOP=true (default off for safe rollout).
// When disabled, returns the input unchanged.
//
// M8 fix history: prior to 2026-04-26 this function set Score = parent_RRF *
// decay^hop. Because seeds were re-scored to cosine ([0.5, 1.0]) by
// ReRankByCosine while expansions kept the tiny RRF score (~0.013) — and
// expansions had no Embedding so cosine rerank skipped them — D2-injected
// items always lost the TrimSlice(TopK) battle to seeds, even when
// semantically more relevant. Diagnosed against conv-26 cat-2 (multi-hop)
// at F1=0.091. See docs/design/2026-04-26-d2-multihop-diagnosis.md.
package search

import (
	"context"
	"log/slog"
	"math"
	"os"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// multihopDecay / multihopMaxDepth — defaults live in tuning.go as
// defaultMultihopDecay (0.8) and defaultMultihopMaxDepth (2). Accessors
// are env-readable via MEMDB_D2_HOP_DECAY / MEMDB_D2_MAX_HOP so grid-runs
// can sweep them without a rebuild. Per-hop decay is deliberately
// aggressive — 1-hop neighbors rarely match the seed in relevance,
// 2-hop neighbors very rarely do. Depth matches the spec's [*1..2]
// Cypher semantics; deeper and false-positive rate explodes while
// marginal recall gain drops off.

// multihopExpandFactor caps the expanded pool size relative to the
// original seed set. 2× lets CE rerank see some new candidates without
// doubling its batch latency.
const multihopExpandFactor = 2

// multihopEnabled reads the env flag on every call so tests can flip it
// with t.Setenv without package-level var gymnastics. Matches the pattern
// used by d1ImportanceEnabled in rerank.go.
func multihopEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_MULTIHOP") == "true"
}

// expandViaGraph takes merged text search results and walks memory_edges
// up to multihopMaxDepth steps, injecting reachable neighbors into the
// candidate pool. Each neighbor is scored as
//
//	score = ((cos(queryVec, neighbor_emb) + 1) / 2) * decay^hop
//
// so it lands on the same [0, decay^hop] scale the seeds will reach after
// ReRankByCosine downstream. Items whose stored embedding could not be
// parsed fall back to the legacy hop-decayed-RRF score; those rarely
// surface (no embedding ≈ no semantic placement) and we explicitly
// log them at Debug for ops visibility.
//
// Seeds are preserved as-is. The merged pool is capped at
// multihopExpandFactor × origSize ordered by score.
//
// Degrades gracefully on any DB error: logs debug and returns the
// original candidates unchanged. Safe to call when pg == nil and
// multihopEnabled() == false (no-op path).
func expandViaGraph(
	ctx context.Context,
	pg postgresClient,
	logger *slog.Logger,
	origCandidates []MergedResult,
	queryVec []float32,
	cubeID, personID, agentID string,
) []MergedResult {
	if !multihopEnabled() {
		searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "disabled")))
		return origCandidates
	}
	if len(origCandidates) == 0 || pg == nil {
		searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "empty_seeds")))
		return origCandidates
	}
	origSize := len(origCandidates)
	cap2x := origSize * multihopExpandFactor

	seedIDs := make([]string, 0, origSize)
	seedScore := make(map[string]float64, origSize)
	for _, c := range origCandidates {
		seedIDs = append(seedIDs, c.ID)
		// Keep the highest score if the same ID somehow appears twice
		// (defensive — MergeVectorAndFulltext should have deduped already).
		if prev, ok := seedScore[c.ID]; !ok || c.Score > prev {
			seedScore[c.ID] = c.Score
		}
	}

	expansions, err := pg.MultiHopEdgeExpansion(ctx, seedIDs, cubeID, personID, multihopMaxDepth(), cap2x, agentID)
	if err != nil {
		searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "error")))
		if logger != nil {
			logger.Debug("multi-hop graph expansion failed", slog.Any("error", err))
		}
		return origCandidates
	}
	if len(expansions) == 0 {
		searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "empty_seeds")))
		searchMx().HopsPerQuery.Record(ctx, 0,
			metric.WithAttributes(attribute.String("outcome", "empty_seeds")))
		if logger != nil {
			logger.Debug("d2: no neighbors reached",
				slog.Int("seed_count", origSize),
				slog.Int("depth_attempted", multihopMaxDepth()),
			)
		}
		return origCandidates
	}
	searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "expanded")))

	// Build merged pool keyed by ID, starting from the seeds so their
	// scores always win vs. a hop-decayed inherited score if a duplicate
	// somehow appears.
	merged := make(map[string]MergedResult, origSize+len(expansions))
	for _, c := range origCandidates {
		merged[c.ID] = c
	}
	maxHop, withEmb, withoutEmb := 0, 0, 0
	for _, e := range expansions {
		if _, already := merged[e.ID]; already {
			continue
		}
		parent, ok := seedScore[e.SeedID]
		if !ok {
			// Walk started from a seed we didn't see — should be impossible
			// given the CTE base case, but guard anyway.
			continue
		}
		hop := e.Hop
		if hop < 1 {
			hop = 1
		}
		if hop > maxHop {
			maxHop = hop
		}
		decay := math.Pow(multihopDecay(), float64(hop))
		// Prefer cosine-against-query × decay so the expansion item lands
		// on the same scale as a cosine-reranked seed. Fallback to the
		// pre-M8 RRF×decay when embedding is missing or queryVec is empty
		// (shouldn't happen in production but tests hit it).
		var score float64
		if len(queryVec) > 0 && len(e.Embedding) > 0 {
			cosNorm := (float64(CosineSimilarity(queryVec, e.Embedding)) + 1.0) / 2.0
			score = cosNorm * decay
			withEmb++
		} else {
			score = parent * decay
			withoutEmb++
		}
		merged[e.ID] = MergedResult{
			ID:         e.ID,
			Properties: e.Properties,
			Score:      score,
			// Embedding intentionally nil — we have already baked cosine into
			// Score, and exposing the embedding would let ReRankByCosine
			// overwrite our hop-decayed score with a plain cosine, defeating
			// the decay. FormatMergedItems writes meta["relativity"] = Score
			// regardless, and ReRankByCosine only updates entries it finds
			// in embeddingsByID (which we are deliberately not in).
		}
	}

	out := make([]MergedResult, 0, len(merged))
	for _, v := range merged {
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > cap2x {
		out = out[:cap2x]
	}
	searchMx().HopsPerQuery.Record(ctx, int64(maxHop),
		metric.WithAttributes(attribute.String("outcome", "expanded")))
	if logger != nil {
		logger.Debug("d2: multi-hop expanded",
			slog.Int("seed_count", origSize),
			slog.Int("expansion_count", len(expansions)),
			slog.Int("max_hop", maxHop),
			slog.Int("scored_by_cosine", withEmb),
			slog.Int("scored_by_decay_only", withoutEmb),
			slog.Int("pool_after_cap", len(out)),
		)
	}
	return out
}
