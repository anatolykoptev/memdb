// Package search — linked_expand.go: M11 F12 search-side 1-hop expansion.
//
// Companion to internal/handlers/linked_resolver.go (the add-side resolver
// that populates properties->'linked_memory_ids' after each atomic fact is
// persisted). This file implements the read-side: for each top-K text seed,
// pull memories whose linked_memory_ids array references the seed, score
// them by cosine-against-query, and inject them into the candidate pool.
//
// Single round-trip via migration 0022's idx_memory_linked_ids GIN index
// (USING GIN ((properties -> 'linked_memory_ids'))). The ?| operator on
// the jsonb array stays index-bound — confirmed via EXPLAIN ANALYZE on the
// LoCoMo corpus during development (sub-10ms typical).
//
// Pool cap: 1.5× the original seed count (less aggressive than D2's 2×
// because linked-by edges are higher-signal — false-positive rate is far
// lower than entity edges, so we don't need as much headroom for the
// cross-encoder to sort it out).
package search

import (
	"context"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// linkedExpandEnvVar gates stageLinkedExpand. Default ON per the M11 spec.
const linkedExpandEnvVar = "MEMDB_F12_LINKED"

// linkedExpandFactor caps the post-expansion pool relative to the original
// seed set. 1.5× balances recall (we want the linked-by neighbours visible
// to CE rerank) against latency (the bigger the pool, the longer CE takes).
const linkedExpandFactor = 1.5

// linkedExpandSeedTopN — only the top-N seeds (by Score) are used as the
// link-target argument list. Rationale: we don't care about linking to
// low-scoring seeds; they will be trimmed downstream anyway. Keeps the
// SQL ANY($1::text[]) array small.
const linkedExpandSeedTopN = 20

// linkedExpandLimit — hard cap on rows returned from
// GetMemoriesByLinkedIDs.  Chosen to be generous enough that the linked
// pool plus the original seeds rarely exceed the 1.5× cap, but bounded so
// a pathological cube (every memory linked to every other memory) cannot
// blow up the candidate pool.
const linkedExpandLimit = 100

// linkedExpandDecay applies a flat penalty to linked-by neighbours so they
// land slightly below cosine-rescored seeds in the merged pool. 0.85 is
// the same shape the CONTRADICTS path uses for its negative penalty —
// keeps ranking semantics consistent.
const linkedExpandDecay = 0.85

// linkedExpandEnabled reads MEMDB_F12_LINKED. Default ON: unset / empty /
// truthy => true. Mirrors linkedResolverEnabled but lives in the search
// package (the package boundary forbids cross-import to handlers).
func linkedExpandEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(linkedExpandEnvVar)))
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// expandViaLinkedIDs is the F12 search-side 1-hop expansion. For each of
// the top-N seeds it pulls memories whose linked_memory_ids array
// references the seed, scores each by
//
//	cosine(query, neighbour_emb) * linkedExpandDecay
//
// (or falls back to seed-Score × decay when no embedding) and merges them
// into the candidate pool capped at linkedExpandFactor × original size.
//
// Soft-fails: DB error / empty result returns the input unchanged.
// Mirrors expandViaGraph's seed-preserving cap design — seeds are NEVER
// evicted; only the expansion budget is bounded.
func expandViaLinkedIDs(
	ctx context.Context,
	pg postgresClient,
	logger *slog.Logger,
	origCandidates []MergedResult,
	queryVec []float32,
	cubeID, personID, agentID string,
) []MergedResult {
	if len(origCandidates) == 0 || pg == nil {
		linkedExpandRecord(ctx, "empty_seeds", 0, 0)
		return origCandidates
	}
	origSize := len(origCandidates)
	cap15x := int(math.Ceil(float64(origSize) * linkedExpandFactor))

	// Pull top-N seed IDs (by Score) — small ANY($1) array keeps the SQL
	// plan tight. We sort defensively; in practice the merged input is
	// already RRF-sorted but the contract isn't enforced.
	seedSorted := make([]MergedResult, len(origCandidates))
	copy(seedSorted, origCandidates)
	sort.SliceStable(seedSorted, func(i, j int) bool {
		return seedSorted[i].Score > seedSorted[j].Score
	})
	topSeeds := seedSorted
	if len(topSeeds) > linkedExpandSeedTopN {
		topSeeds = topSeeds[:linkedExpandSeedTopN]
	}
	seedIDs := make([]string, 0, len(topSeeds))
	seedSet := make(map[string]struct{}, origSize)
	for _, c := range origCandidates {
		seedSet[c.ID] = struct{}{}
	}
	for _, c := range topSeeds {
		if c.ID != "" {
			seedIDs = append(seedIDs, c.ID)
		}
	}
	if len(seedIDs) == 0 {
		linkedExpandRecord(ctx, "empty_seeds", 0, 0)
		return origCandidates
	}

	rows, err := pg.GetMemoriesByLinkedIDs(ctx, seedIDs, cubeID, personID, agentID, linkedExpandLimit)
	if err != nil {
		linkedExpandRecord(ctx, "error", 0, 0)
		if logger != nil {
			logger.Debug("linked_expand: db error", slog.Any("error", err))
		}
		return origCandidates
	}
	if len(rows) == 0 {
		linkedExpandRecord(ctx, "no_neighbors", 0, 0)
		return origCandidates
	}

	expansionItems := make([]MergedResult, 0, len(rows))
	withEmb, withoutEmb := 0, 0
	for _, r := range rows {
		if _, dup := seedSet[r.ID]; dup {
			continue
		}
		var score float64
		if len(queryVec) > 0 && len(r.Embedding) > 0 {
			cosNorm := (float64(CosineSimilarity(queryVec, r.Embedding)) + 1.0) / 2.0
			score = cosNorm * linkedExpandDecay
			withEmb++
		} else {
			// Fallback: gentle decay on a flat 0.5 baseline so the item
			// still surfaces above the cap-floor but below cosine-scored
			// neighbours. Tracked under withoutEmb in the debug log.
			score = 0.5 * linkedExpandDecay
			withoutEmb++
		}
		expansionItems = append(expansionItems, MergedResult{
			ID:         r.ID,
			Properties: r.Properties,
			Score:      score,
			// Embedding nil deliberately — same reasoning as expandViaGraph:
			// we have already baked cosine into Score, exposing Embedding
			// would let ReRankByCosine downstream overwrite our decayed
			// score.
		})
	}

	sort.SliceStable(expansionItems, func(i, j int) bool {
		return expansionItems[i].Score > expansionItems[j].Score
	})
	expBudget := cap15x - origSize
	if expBudget < 0 {
		expBudget = 0
	}
	if len(expansionItems) > expBudget {
		expansionItems = expansionItems[:expBudget]
	}

	out := make([]MergedResult, 0, origSize+len(expansionItems))
	out = append(out, origCandidates...)
	out = append(out, expansionItems...)
	linkedExpandRecord(ctx, "expanded", len(expansionItems), withEmb)
	if logger != nil {
		logger.Debug("linked_expand: 1-hop expanded",
			slog.Int("seed_count", origSize),
			slog.Int("seed_topn", len(seedIDs)),
			slog.Int("rows_returned", len(rows)),
			slog.Int("expansion_count", len(expansionItems)),
			slog.Int("scored_by_cosine", withEmb),
			slog.Int("scored_by_decay_only", withoutEmb),
			slog.Int("pool_after_cap", len(out)),
		)
	}
	return out
}

// linkedExpandRecord bumps the F12 search-side counter. Outcome label
// matches the D2 multihop counter shape so dashboards can stack them.
func linkedExpandRecord(ctx context.Context, outcome string, expansionCount, withEmb int) {
	mx := searchMx()
	if mx.LinkedExpand != nil {
		mx.LinkedExpand.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
	if mx.LinkedExpandCount != nil && expansionCount > 0 {
		mx.LinkedExpandCount.Record(ctx, int64(expansionCount))
	}
	_ = withEmb // reserved for a future cosine-coverage histogram
}
