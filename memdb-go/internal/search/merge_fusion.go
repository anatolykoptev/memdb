// Package search — merge_fusion.go: env-selectable fusion strategy dispatcher.
//
// Env gates:
//
//	MEMDB_FUSION_STRATEGY=<strategy>  — select fusion strategy (default "rrf")
//	  rrf     — plain RRF via go-kit (requires MEMDB_USE_GOKIT_RRF=1 superseded)
//	  wrrf    — WeightedRRF (per-source weights via MEMDB_RRF_WEIGHTS)
//	  dbsf    — Distribution-Based Score Fusion (Qdrant 1.11+)
//	  linear  — LinearMinMax weighted sum (Elasticsearch convention)
//	  legacy  — bypass go-kit, use existing custom merge (MergeVectorAndFulltext)
//	MEMDB_RRF_WEIGHTS=semantic:1.0,bm25:0.7,graph:0.5
//	  per-source weights for wrrf in fixed order: semantic, bm25, graph
//	  empty / unparseable → uniform weights {1.0, 1.0, 1.0} (plain RRF equivalent)
//
// Default ("rrf") preserves the exact behaviour of MEMDB_USE_GOKIT_RRF=1.
// All non-default strategies must be set explicitly via MEMDB_FUSION_STRATEGY.
package search

import (
	"context"
	"os"
	"strconv"
	"strings"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	fusionStrategyEnvVar = "MEMDB_FUSION_STRATEGY"
	rrfWeightsEnvVar     = "MEMDB_RRF_WEIGHTS"

	strategyRRF    = "rrf"
	strategyWRRF   = "wrrf"
	strategyDBSF   = "dbsf"
	strategyLinear = "linear"
	strategyLegacy = "legacy"
)

// fusionStrategy returns the active MEMDB_FUSION_STRATEGY value.
// Empty env (unset) → "rrf" (plain go-kit RRF, current default).
func fusionStrategy() string {
	s := os.Getenv(fusionStrategyEnvVar)
	if s == "" {
		return strategyRRF
	}
	return s
}

// parseWeightedRRFWeights parses MEMDB_RRF_WEIGHTS into a 3-element slice
// in fixed source order: [semantic, bm25, graph].
//
// Expected format: "semantic:1.0,bm25:0.7,graph:0.5" (keys are optional labels;
// values are parsed as float64 in the fixed order they appear).
// Returns uniform weights {1.0, 1.0, 1.0} on empty or unparseable input.
func parseWeightedRRFWeights() []float64 {
	uniform := []float64{1.0, 1.0, 1.0}
	raw := os.Getenv(rrfWeightsEnvVar)
	if raw == "" {
		return uniform
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return uniform
	}
	out := make([]float64, 3)
	for i, p := range parts {
		// Accept "key:value" or bare "value".
		val := p
		if idx := strings.LastIndex(p, ":"); idx >= 0 {
			val = p[idx+1:]
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil || v < 0 {
			return uniform
		}
		out[i] = v
	}
	return out
}

// convertToScoredIDLists converts vector+fulltext results into two ScoredIDLists
// suitable for score-based fusion helpers (DBSF, LinearMinMax).
func convertToScoredIDLists(vec, ft []db.VectorSearchResult) (gokitrerank.ScoredIDList, gokitrerank.ScoredIDList) {
	vecList := make(gokitrerank.ScoredIDList, len(vec))
	for i, r := range vec {
		vecList[i] = gokitrerank.ScoredID{ID: r.ID, Score: float64(r.Score)}
	}
	ftList := make(gokitrerank.ScoredIDList, len(ft))
	for i, r := range ft {
		ftList[i] = gokitrerank.ScoredID{ID: r.ID, Score: float64(r.Score)}
	}
	return vecList, ftList
}

// fusedToMergedResults reconstructs MergedResult from a []gokitrerank.Fused
// by looking up Properties and Embedding in the pre-built props index.
func fusedToMergedResults(fused []gokitrerank.Fused, props map[string]db.VectorSearchResult) []MergedResult {
	out := make([]MergedResult, 0, len(fused))
	for _, f := range fused {
		r, ok := props[f.ID]
		if !ok {
			out = append(out, MergedResult{ID: f.ID, Score: f.Score})
			continue
		}
		out = append(out, MergedResult{
			ID:         r.ID,
			Properties: r.Properties,
			Score:      f.Score,
			Embedding:  r.Embedding,
		})
	}
	return out
}

// recordFusionEngaged increments RRFEngaged with the strategy label.
func recordFusionEngaged(ctx context.Context, strategy string) {
	mx := searchMx()
	mx.RRFEngaged.Add(ctx, 1, metric.WithAttributes(attribute.String("strategy", strategy)))
}

// mergeVectorAndFulltextFusion is the env-driven fusion dispatcher.
// It routes to the appropriate go-kit fusion helper based on MEMDB_FUSION_STRATEGY.
// Default (unset env or "rrf") is plain go-kit RRF — identical to the pre-PR
// MergeVectorAndFulltextGokit behaviour.
//
// "legacy" bypasses go-kit entirely and calls MergeVectorAndFulltext.
func mergeVectorAndFulltextFusion(vec, ft []db.VectorSearchResult) []MergedResult {
	strategy := fusionStrategy()
	ctx := context.Background()
	k := rrfKValue()

	switch strategy {
	case strategyWRRF:
		// parseWeightedRRFWeights returns 3 slots (semantic, bm25, graph).
		// With 2 input lists (vec, ft) we use the first 2 weights.
		weights := parseWeightedRRFWeights()[:2]
		recordFusionEngaged(ctx, strategyWRRF)
		mx := searchMx()
		mx.RRFKGauge.Record(ctx, float64(k))
		fused := gokitrerank.WeightedRRF(k, weights, extractIDs(vec), extractIDs(ft))
		return fusedToMergedResults(fused, buildPropsIndex(vec, ft))

	case strategyDBSF:
		vecList, ftList := convertToScoredIDLists(vec, ft)
		recordFusionEngaged(ctx, strategyDBSF)
		fused := gokitrerank.DBSF(vecList, ftList)
		return fusedToMergedResults(fused, buildPropsIndex(vec, ft))

	case strategyLinear:
		vecList, ftList := convertToScoredIDLists(vec, ft)
		weights := parseWeightedRRFWeights()[:2] // semantic + bm25 only for 2-list linear
		recordFusionEngaged(ctx, strategyLinear)
		fused := gokitrerank.LinearMinMax(weights, vecList, ftList)
		return fusedToMergedResults(fused, buildPropsIndex(vec, ft))

	case strategyLegacy:
		// Bypass go-kit entirely — existing custom merge.
		return MergeVectorAndFulltext(vec, ft)

	default: // "rrf" or unrecognised
		recordFusionEngaged(ctx, strategyRRF)
		mx := searchMx()
		mx.RRFKGauge.Record(ctx, float64(k))
		fused := gokitrerank.RRF(k, extractIDs(vec), extractIDs(ft))
		return fusedToMergedResults(fused, buildPropsIndex(vec, ft))
	}
}
