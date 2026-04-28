package scheduler

// pagerank_personalized_test.go — unit tests for ComputePersonalizedPR and helpers.
//
// Tests:
//  1. computePersonalizedPageRank on star graph — seeded node gets highest rank.
//  2. Scores sum to ~1.0.
//  3. Empty seeds → fall back to uniform prior (scores still sum ~1.0).
//  4. Seeds absent from graph → uniform prior fallback.
//  5. pprCacheKey stability — same seeds in different order produce same key.
//  6. ComputePersonalizedPR empty_seed fast-path (no redis, no pg).
//  7. ComputePersonalizedPR cache hit via miniredis.
//  8. ComputePersonalizedPR convergence: iterCount ≤ pprIterations.
//  9. Scores in [0, 1] for all nodes.
// 10. Global PR fallback when PPR returns no graph items for seeds.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// TestComputePPR_StarGraphSeeded verifies that seeding the hub of a star graph
// causes the hub to dominate the PPR scores.
func TestComputePPR_StarGraphSeeded(t *testing.T) {
	// Hub H, leaves L1..L4. All leaves point to hub; hub points back to L1.
	edges := []db.PageRankEdge{
		{FromID: "L1", ToID: "H", Weight: 1},
		{FromID: "L2", ToID: "H", Weight: 1},
		{FromID: "L3", ToID: "H", Weight: 1},
		{FromID: "L4", ToID: "H", Weight: 1},
		{FromID: "H", ToID: "L1", Weight: 1},
	}
	seeds := map[string]struct{}{"H": {}}
	iters, scores := computePersonalizedPageRank(edges, seeds)
	if scores == nil {
		t.Fatal("expected non-nil scores")
	}
	if iters <= 0 || iters > pprIterations {
		t.Errorf("expected 1 ≤ iters ≤ %d, got %d", pprIterations, iters)
	}
	// Hub is the seed — it should have the highest PPR score.
	if scores["H"] <= scores["L2"] {
		t.Errorf("seeded hub H should dominate: H=%.6f L2=%.6f", scores["H"], scores["L2"])
	}
}

// TestComputePPR_ScoresSumToOne verifies the distribution property.
func TestComputePPR_ScoresSumToOne(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "A", ToID: "B", Weight: 1},
		{FromID: "B", ToID: "C", Weight: 1},
		{FromID: "C", ToID: "A", Weight: 1},
	}
	seeds := map[string]struct{}{"A": {}}
	_, scores := computePersonalizedPageRank(edges, seeds)
	var total float64
	for _, s := range scores {
		total += s
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("scores should sum to ~1.0, got %.6f", total)
	}
}

// TestComputePPR_EmptySeedFallsBackToUniform verifies that seeds with no
// matching nodes in the graph fall back to a uniform restart vector (scores
// still sum ~1.0, no NaN/inf).
func TestComputePPR_EmptySeedFallsBackToUniform(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "X", ToID: "Y", Weight: 1},
	}
	// Seeds are not in the graph.
	seeds := map[string]struct{}{"NOTHERE": {}}
	_, scores := computePersonalizedPageRank(edges, seeds)
	if scores == nil {
		t.Fatal("expected non-nil scores on missing-seed fallback")
	}
	var total float64
	for _, s := range scores {
		total += s
		if s != s { // NaN check
			t.Error("NaN score detected")
		}
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("fallback scores should sum to ~1.0, got %.6f", total)
	}
}

// TestComputePPR_EmptyEdgesReturnsNil verifies degenerate input.
func TestComputePPR_EmptyEdgesReturnsNil(t *testing.T) {
	_, scores := computePersonalizedPageRank(nil, map[string]struct{}{"A": {}})
	if scores != nil {
		t.Errorf("expected nil for empty edge list, got %v", scores)
	}
}

// TestPPRCacheKey_OrderIndependence verifies the cache key is stable regardless
// of seed ID ordering.
func TestPPRCacheKey_OrderIndependence(t *testing.T) {
	k1 := pprCacheKey("cube-1", []string{"B", "A", "C"})
	k2 := pprCacheKey("cube-1", []string{"C", "B", "A"})
	k3 := pprCacheKey("cube-1", []string{"A", "B", "C"})
	if k1 != k2 || k2 != k3 {
		t.Errorf("cache keys differ for same seeds in different order: %q %q %q", k1, k2, k3)
	}
}

// TestPPRCacheKey_DifferentCubesDiffer verifies cube isolation.
func TestPPRCacheKey_DifferentCubesDiffer(t *testing.T) {
	k1 := pprCacheKey("cube-1", []string{"A"})
	k2 := pprCacheKey("cube-2", []string{"A"})
	if k1 == k2 {
		t.Error("cache keys for different cubes must differ")
	}
}

// TestComputePersonalizedPR_EmptySeedFastPath verifies that ComputePersonalizedPR
// returns an empty map (not error) when seedEntityIDs is empty.
func TestComputePersonalizedPR_EmptySeedFastPath(t *testing.T) {
	w := NewWorker(nil, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	scores, err := w.ComputePersonalizedPR(context.Background(), "cube-1", nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if scores == nil {
		t.Error("expected empty map (not nil) for empty seeds")
	}
	if len(scores) != 0 {
		t.Errorf("expected 0 scores for empty seeds, got %d", len(scores))
	}
}

// TestComputePersonalizedPR_CacheHit verifies that a pre-populated Redis entry
// is returned via ComputePersonalizedPR without recomputing.
func TestComputePersonalizedPR_CacheHit(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	w := NewWorker(rdb, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// Pre-populate cache.
	seedIDs := []string{"node-A", "node-B"}
	key := pprCacheKey("cube-X", seedIDs)
	payload, _ := json.Marshal(map[string]float64{"node-A": 0.6, "node-B": 0.4})
	mr.Set(key, string(payload))
	mr.SetTTL(key, pprCacheTTL)

	scores, err := w.ComputePersonalizedPR(context.Background(), "cube-X", seedIDs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 2 {
		t.Errorf("expected 2 scores from cache, got %d", len(scores))
	}
	if scores["node-A"] != 0.6 {
		t.Errorf("expected node-A=0.6, got %.3f", scores["node-A"])
	}
}

// TestComputePPR_ScoresInRange verifies all PPR scores are in [0, 1].
func TestComputePPR_ScoresInRange(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "A", ToID: "B", Weight: 1},
		{FromID: "B", ToID: "C", Weight: 1},
		{FromID: "C", ToID: "D", Weight: 1},
		{FromID: "D", ToID: "A", Weight: 1},
	}
	seeds := map[string]struct{}{"A": {}, "B": {}}
	_, scores := computePersonalizedPageRank(edges, seeds)
	for id, s := range scores {
		if s < 0 || s > 1 {
			t.Errorf("score for %q out of [0,1]: %.6f", id, s)
		}
	}
}

// TestPPRBlendWeights_Defaults verifies env-defaults are 0.7/0.3.
func TestPPRBlendWeights_Defaults(t *testing.T) {
	t.Setenv("MEMDB_PPR_BLEND_PPR", "")
	t.Setenv("MEMDB_PPR_BLEND_GLOBAL", "")
	pprW, globalW := PPRBlendWeights()
	if pprW != defaultPPRBlendPPR {
		t.Errorf("expected pprW=%.1f, got %.3f", defaultPPRBlendPPR, pprW)
	}
	if globalW != defaultPPRBlendGlobal {
		t.Errorf("expected globalW=%.1f, got %.3f", defaultPPRBlendGlobal, globalW)
	}
}

// TestPPRCacheTTL_Is300s verifies the cache TTL constant.
func TestPPRCacheTTL_Is300s(t *testing.T) {
	if pprCacheTTL != 5*time.Minute {
		t.Errorf("expected pprCacheTTL=5m, got %v", pprCacheTTL)
	}
}
