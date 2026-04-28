package search

// ppr_scorer.go — F14: Personalized PageRank integration in SearchService.
//
// PPRScorer is a narrow interface over scheduler.Worker.ComputePersonalizedPR.
// Keeping the interface here (rather than importing scheduler) avoids an import
// cycle: search → scheduler → db → search would cycle.
//
// The blending formula (HippoRAG 2 ablation):
//
//	blended_score = pprWeight * ppr[id] + globalWeight * global_pr[id]
//
// where global_pr comes from the "pagerank" metadata field already stored by
// the background PageRank goroutine (M10 S7). The blended score overwrites the
// "pagerank" metadata key so that applyDecayToItem picks it up transparently
// through the existing pageRankMultiplier() call — zero changes to D1 formula.
//
// If PPR scores are empty (no entity seeds or cache miss degraded to empty),
// the "pagerank" key is left untouched — global PR remains in effect.
//
// Env gates (read via scheduler.PPRBlendWeights):
//
//	MEMDB_PPR_BLEND_PPR    — PPR weight (default 0.7)
//	MEMDB_PPR_BLEND_GLOBAL — global PR weight (default 0.3)
//	MEMDB_PPR_ENABLED      — set "false" to skip PPR entirely (default true)

import (
	"context"
	"os"
	"strconv"
)

// PPRScorer computes query-seeded Personalized PageRank scores for a cube.
// The concrete implementation is *scheduler.Worker — registered via
// SearchService.SetPPRScorer. nil is valid (PPR disabled).
type PPRScorer interface {
	// ComputePersonalizedPR returns id→score map for the given cube and seed entity IDs.
	// Returns an empty map (not nil) when seedEntityIDs is empty or seeds not in graph.
	// Returns nil only on hard error (callers fall back to global PR).
	ComputePersonalizedPR(ctx context.Context, cubeID string, seedEntityIDs []string) (map[string]float64, error)
}

// SetPPRScorer wires the PPR scorer into SearchService. May be called after
// NewSearchService but before any Search calls.
func (s *SearchService) SetPPRScorer(p PPRScorer) {
	s.pprScorer = p
}

// pprEnabled reports whether PPR is active. Default true.
func pprEnabled() bool {
	v := os.Getenv("MEMDB_PPR_ENABLED")
	return v == "" || v == "true"
}

// blendPPRIntoMetadata computes PPR for the query and blends the result into
// each item's "pagerank" metadata key. Items without an existing "pagerank"
// value are treated as having global_pr=0 — they still get the PPR component.
//
// This function is a no-op when:
//   - PPR is disabled (MEMDB_PPR_ENABLED=false)
//   - PPR scorer is not configured (s.pprScorer == nil)
//   - No entity seeds were extracted (empty seedIDs)
//   - PPR returned empty scores (seeds not in graph)
//   - Items slice is empty
//
// Blending: blended = pprW * ppr[id] + globalW * global_pr[id].
// pprW + globalW should sum to 1.0 but are not enforced — out-of-range values
// are env-tunable without code changes.
func (s *SearchService) blendPPRIntoMetadata(
	ctx context.Context,
	cubeID string,
	seedIDs []string,
	items []map[string]any,
) {
	if !pprEnabled() || s.pprScorer == nil || len(items) == 0 || len(seedIDs) == 0 {
		return
	}

	pprScores, err := s.pprScorer.ComputePersonalizedPR(ctx, cubeID, seedIDs)
	if err != nil || len(pprScores) == 0 {
		// Fallback: global PR already in metadata — nothing to do.
		return
	}

	pprW, globalW := pprBlendWeightsFromEnv()

	for _, item := range items {
		meta, ok := item["metadata"].(map[string]any)
		if !ok || meta == nil {
			continue
		}

		// Read item ID from metadata (stored as "id" by FormatMemoryItem).
		id, _ := meta["id"].(string)
		if id == "" {
			continue
		}

		pprScore, hasPPR := pprScores[id]
		if !hasPPR {
			// This item was not in the PPR graph — leave metadata untouched
			// so the global PR boost still applies via pageRankMultiplier.
			continue
		}

		// Read existing global PR (may be string or float64).
		globalPR := readMetaFloat(meta, "pagerank")

		blended := pprW*pprScore + globalW*globalPR
		if blended < 0 {
			blended = 0
		}
		meta["pagerank"] = blended
	}
}

// pprBlendWeightsFromEnv reads MEMDB_PPR_BLEND_PPR / MEMDB_PPR_BLEND_GLOBAL.
// Defaults: 0.7 / 0.3.
func pprBlendWeightsFromEnv() (pprW, globalW float64) {
	pprW = envFloatSearch("MEMDB_PPR_BLEND_PPR", 0.7)
	globalW = envFloatSearch("MEMDB_PPR_BLEND_GLOBAL", 0.3)
	return
}

// envFloatSearch reads key from env as float64; returns def on missing or parse error.
func envFloatSearch(key string, def float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

// readMetaFloat reads a float64 from a metadata map that may store the value
// as float64 (JSON decode) or string (agtype TEXT). Returns 0 on any miss.
func readMetaFloat(meta map[string]any, key string) float64 {
	switch v := meta[key].(type) {
	case float64:
		return v
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}
