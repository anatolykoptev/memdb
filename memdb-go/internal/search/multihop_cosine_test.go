package search

// multihop_cosine_test.go — M8 fix coverage. Verifies expandViaGraph scores
// expansion items by cosine(query, neighbor_emb) × hop-decay when both
// queryVec and neighbor Embedding are present, falling back to the legacy
// parent-RRF × decay path when either is missing. Sister file
// multihop_test.go covers no-op / empty / DB-error / cap / orphan paths.

import (
	"context"
	"math"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// orthogonalEmbedding returns a 4-dim unit vector pointing along axis ax.
// Cosine(unit_x, unit_x) = 1.0; cosine(unit_x, unit_y) = 0.
func orthogonalEmbedding(ax int) []float32 {
	v := make([]float32, 4)
	v[ax%4] = 1
	return v
}

func TestExpandViaGraph_CosineScoring_QueryAlignedWins(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	queryVec := orthogonalEmbedding(0)

	// n1 (hop 1) is identical to query → cos=1 → score=1.0 * 0.8 = 0.8.
	// n2 (hop 1) is orthogonal      → cos=0 → score=0.5 * 0.8 = 0.4.
	pg := &mockExpansionPG{returnExpansions: []db.GraphExpansion{
		{ID: "n1", Hop: 1, SeedID: "seed-0", Properties: `{"id":"n1"}`, Embedding: orthogonalEmbedding(0)},
		{ID: "n2", Hop: 1, SeedID: "seed-0", Properties: `{"id":"n2"}`, Embedding: orthogonalEmbedding(1)},
	}}
	seeds := []MergedResult{{ID: "seed-0", Score: 0.01}} // Tiny RRF score
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, queryVec, "cube", "user", "")

	var n1, n2 *MergedResult
	for i := range got {
		switch got[i].ID {
		case "n1":
			n1 = &got[i]
		case "n2":
			n2 = &got[i]
		}
	}
	if n1 == nil || n2 == nil {
		t.Fatalf("missing expansion items: %+v", got)
	}
	const eps = 1e-6
	if math.Abs(n1.Score-0.8) > eps {
		t.Fatalf("n1 cosine=1.0 × decay 0.8 want 0.8, got %v", n1.Score)
	}
	if math.Abs(n2.Score-0.4) > eps {
		t.Fatalf("n2 cosine=0 (norm 0.5) × decay 0.8 want 0.4, got %v", n2.Score)
	}
	if n1.Score <= n2.Score {
		t.Fatalf("query-aligned n1 must outrank orthogonal n2: n1=%v n2=%v", n1.Score, n2.Score)
	}
	// And both must outrank the tiny seed (0.01) — pre-fix behaviour was the inverse.
	if !(n1.Score > seeds[0].Score && n2.Score > seeds[0].Score) {
		t.Fatalf("expansion items must beat tiny RRF seed: seed=%v n1=%v n2=%v",
			seeds[0].Score, n1.Score, n2.Score)
	}
}

func TestExpandViaGraph_CosineScoring_HopDecayApplied(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	queryVec := orthogonalEmbedding(0)
	// Same neighbor embedding (cos=1.0) at hop 1 vs hop 2 → score 0.8 vs 0.64.
	pg := &mockExpansionPG{returnExpansions: []db.GraphExpansion{
		{ID: "h1", Hop: 1, SeedID: "seed-0", Properties: `{"id":"h1"}`, Embedding: orthogonalEmbedding(0)},
		{ID: "h2", Hop: 2, SeedID: "seed-0", Properties: `{"id":"h2"}`, Embedding: orthogonalEmbedding(0)},
	}}
	seeds := []MergedResult{{ID: "seed-0", Score: 0.01}}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, queryVec, "cube", "user", "")
	var h1, h2 *MergedResult
	for i := range got {
		switch got[i].ID {
		case "h1":
			h1 = &got[i]
		case "h2":
			h2 = &got[i]
		}
	}
	if h1 == nil {
		t.Fatalf("h1 missing (cap evicted higher-scoring): %+v", got)
	}
	const eps = 1e-6
	if math.Abs(h1.Score-0.8) > eps {
		t.Fatalf("h1 hop-1 score: want 0.8, got %v", h1.Score)
	}
	// h2 may be capped out at cap2x=2 (1 seed + 1 expansion), but if present
	// its score must be 0.64.
	if h2 != nil && math.Abs(h2.Score-0.64) > eps {
		t.Fatalf("h2 hop-2 score: want 0.64, got %v", h2.Score)
	}
}

func TestExpandViaGraph_FallsBackWhenEmbeddingMissing(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	queryVec := orthogonalEmbedding(0)
	// No Embedding on the expansion → fallback to parent_score × decay.
	pg := &mockExpansionPG{returnExpansions: []db.GraphExpansion{
		{ID: "x", Hop: 1, SeedID: "seed-0", Properties: `{"id":"x"}`},
	}}
	seeds := []MergedResult{{ID: "seed-0", Score: 0.5}}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, queryVec, "cube", "user", "")
	var x *MergedResult
	for i := range got {
		if got[i].ID == "x" {
			x = &got[i]
		}
	}
	if x == nil {
		t.Fatalf("x missing from result: %+v", got)
	}
	const eps = 1e-9
	if math.Abs(x.Score-0.4) > eps {
		t.Fatalf("fallback score: want parent(0.5)*decay(0.8)=0.4, got %v", x.Score)
	}
}
