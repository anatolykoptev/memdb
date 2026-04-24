// Package search — service_multihop.go: D2 multi-hop graph expansion.
//
// After VectorSearch returns top-K seeds, walk the memory_edges table up
// to maxHop steps (default 2) to surface related memories. Inherit each
// seed's score with a 0.8^hop decay and cap the expanded pool at 2× the
// original size. Gated by MEMDB_SEARCH_MULTIHOP=true (default off for
// safe rollout). When disabled, returns the input unchanged.
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

// multihopDecay is the per-hop score penalty multiplier. A neighbor at
// hop=1 inherits 0.8× its seed's score; hop=2 → 0.64×. The coefficient is
// deliberately aggressive — 1-hop neighbors rarely match the seed in
// relevance, 2-hop neighbors very rarely do.
const multihopDecay = 0.8

// multihopMaxDepth is the recursive BFS depth. Matches the spec's
// [*1..2] Cypher semantics — any deeper and false-positive rate
// explodes while marginal recall gain drops off.
const multihopMaxDepth = 2

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
// candidate pool. Each neighbor inherits the reaching seed's score with
// a multihopDecay^hop penalty. Seeds are preserved as-is. The merged
// pool is capped at multihopExpandFactor × origSize ordered by score.
//
// Degrades gracefully on any DB error: logs debug and returns the
// original candidates unchanged. Safe to call when pg == nil and
// multihopEnabled() == false (no-op path).
func expandViaGraph(
	ctx context.Context,
	pg postgresClient,
	logger *slog.Logger,
	origCandidates []MergedResult,
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

	expansions, err := pg.MultiHopEdgeExpansion(ctx, seedIDs, cubeID, personID, multihopMaxDepth, cap2x, agentID)
	if err != nil {
		searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "error")))
		if logger != nil {
			logger.Debug("multi-hop graph expansion failed", slog.Any("error", err))
		}
		return origCandidates
	}
	if len(expansions) == 0 {
		searchMx().Multihop.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "empty_seeds")))
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
		score := parent * math.Pow(multihopDecay, float64(hop))
		merged[e.ID] = MergedResult{
			ID:         e.ID,
			Properties: e.Properties,
			Score:      score,
			// Embedding intentionally nil — CE rerank operates on text only,
			// and cosine rerank uses embeddingsByID (populated only for
			// items that came from vector/fulltext search).
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
	return out
}
