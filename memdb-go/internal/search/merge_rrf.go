// Package search — merge_rrf.go: go-kit/rerank.RRF-backed 3-source hybrid fusion.
//
// Env gates:
//
//	MEMDB_USE_GOKIT_RRF=1  — enable go-kit RRF path (legacy gate, still honoured)
//	MEMDB_RRF_K=<int>      — override RRF k constant (default 60, Robertson 2009)
//	MEMDB_FUSION_STRATEGY  — select fusion strategy (see merge_fusion.go)
//
// When MEMDB_USE_GOKIT_RRF is unset or 0 AND MEMDB_FUSION_STRATEGY is unset,
// MergeVectorAndFulltext is called unchanged — bytewise parity with pre-PR
// behaviour is guaranteed.
package search

import (
	"context"
	"os"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

const (
	goKitRRFEnvVar = "MEMDB_USE_GOKIT_RRF"
	rrfKEnvVar     = "MEMDB_RRF_K"
)

// gokitRRFEnabled reads MEMDB_USE_GOKIT_RRF. Default OFF.
func gokitRRFEnabled() bool {
	return os.Getenv(goKitRRFEnvVar) == "1"
}

// rrfKValue returns the configured RRF k constant.
// Falls back to gokitrerank.DefaultRRFK (60) on missing or invalid env.
func rrfKValue() int {
	return envcfg.Int(rrfKEnvVar, gokitrerank.DefaultRRFK)
}

// extractIDs returns a slice of IDs from a VectorSearchResult slice, preserving
// rank order (index 0 = rank 1).
func extractIDs(results []db.VectorSearchResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

// extractGraphIDs returns a slice of IDs from a GraphRecallResult slice.
func extractGraphIDs(results []db.GraphRecallResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

// buildPropsIndex builds a map from ID to (Properties, Embedding) for fast
// lookup when reconstructing MergedResult from fused IDs.
func buildPropsIndex(vec, ft []db.VectorSearchResult) map[string]db.VectorSearchResult {
	idx := make(map[string]db.VectorSearchResult, len(vec)+len(ft))
	for _, r := range vec {
		if _, ok := idx[r.ID]; !ok {
			idx[r.ID] = r
		} else if len(r.Embedding) > 0 && len(idx[r.ID].Embedding) == 0 {
			idx[r.ID] = r
		}
	}
	for _, r := range ft {
		if _, ok := idx[r.ID]; !ok {
			idx[r.ID] = r
		} else if len(r.Embedding) > 0 && len(idx[r.ID].Embedding) == 0 {
			idx[r.ID] = r
		}
	}
	return idx
}

// MergeVectorAndFulltextGokit fuses vector and fulltext ranked lists via
// go-kit/rerank.RRF. Drop-in replacement for MergeVectorAndFulltext; the
// env flag MEMDB_USE_GOKIT_RRF=1 routes calls here from the pipeline.
//
// Result ordering and score semantics differ from the hand-rolled path:
// scores are RRF sums (Σ 1/(k+rank_i)) rather than accumulated floats, but
// downstream cosine-rerank replaces scores anyway — so the difference is in
// candidate ordering entering Stage-2.
func MergeVectorAndFulltextGokit(vec, ft []db.VectorSearchResult) []MergedResult {
	k := rrfKValue()
	mx := searchMx()
	ctx := context.Background()
	mx.RRFEngaged.Add(ctx, 1, metric.WithAttributes(attribute.String("strategy", strategyRRF)))
	mx.RRFKGauge.Record(ctx, float64(k))

	fused := gokitrerank.RRF(k, extractIDs(vec), extractIDs(ft))

	props := buildPropsIndex(vec, ft)
	out := make([]MergedResult, 0, len(fused))
	for _, f := range fused {
		r, ok := props[f.ID]
		if !ok {
			// ID only from one list and lookup missed — shouldn't happen but be safe.
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

// mergeVectorAndFulltextDispatch routes to the appropriate fusion backend.
//
// Priority:
//  1. MEMDB_FUSION_STRATEGY is set → use new multi-strategy dispatcher (merge_fusion.go)
//  2. MEMDB_USE_GOKIT_RRF=1 (legacy gate) → use plain go-kit RRF path
//  3. Neither → fall back to original MergeVectorAndFulltext (custom merge)
func mergeVectorAndFulltextDispatch(vec, ft []db.VectorSearchResult) []MergedResult {
	if os.Getenv(fusionStrategyEnvVar) != "" {
		return mergeVectorAndFulltextFusion(vec, ft)
	}
	if gokitRRFEnabled() {
		return MergeVectorAndFulltextGokit(vec, ft)
	}
	return MergeVectorAndFulltext(vec, ft)
}
