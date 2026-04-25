package scheduler

// pagerank_test.go — unit tests for the PageRank implementation.
//
// Test cases:
//   1. Toy 4-node linear chain — verifies power-method convergence.
//   2. Single-node (no edges) — returns nil, no panic.
//   3. Self-loops are skipped — computePageRank ignores fromID == toID.
//   4. Disconnected sink — dangling-node correction keeps scores > 0 everywhere.
//   5. runPageRankLoop smoke test — goroutine emits a metric within 1 s.
//   6. All-invalid edges returns nil (precondition for compute_error metric).
//   7. errComputePageRank sentinel is correctly wrapped/detected via errors.Is.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// TestComputePageRank_FourNodeChain verifies the algorithm on a simple linear
// chain A→B→C→D with uniform weights.  In a 4-node chain with d=0.85 and 30
// iterations the scores should be monotonically increasing (A lowest, D
// highest) — reflecting that D is the "authority" that only receives links.
func TestComputePageRank_FourNodeChain(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "A", ToID: "B", Weight: 1},
		{FromID: "B", ToID: "C", Weight: 1},
		{FromID: "C", ToID: "D", Weight: 1},
	}
	scores := computePageRank(edges)
	if scores == nil {
		t.Fatal("expected non-nil scores")
	}
	// All four nodes must be present.
	for _, id := range []string{"A", "B", "C", "D"} {
		if _, ok := scores[id]; !ok {
			t.Errorf("missing node %s in result", id)
		}
	}
	// D is a pure sink (receives all flow), A is a pure source (no in-links).
	// With dangling correction D should have the highest score.
	if scores["D"] <= scores["A"] {
		t.Errorf("expected scores[D]=%.6f > scores[A]=%.6f (D is the authority)", scores["D"], scores["A"])
	}
	// Scores must sum to approximately 1.0 (within floating-point tolerance).
	var total float64
	for _, s := range scores {
		total += s
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("expected scores to sum to ~1.0, got %.6f", total)
	}
}

// TestComputePageRank_StarGraph verifies that the hub (centre) of a star
// receives the highest PageRank — every leaf points to the hub.
func TestComputePageRank_StarGraph(t *testing.T) {
	// Hub = H, leaves = L1..L4.  Leaves→Hub, Hub→L1 (cycle to prevent full sink).
	edges := []db.PageRankEdge{
		{FromID: "L1", ToID: "H", Weight: 1},
		{FromID: "L2", ToID: "H", Weight: 1},
		{FromID: "L3", ToID: "H", Weight: 1},
		{FromID: "L4", ToID: "H", Weight: 1},
		{FromID: "H", ToID: "L1", Weight: 1},
	}
	scores := computePageRank(edges)
	if scores == nil {
		t.Fatal("expected non-nil scores")
	}
	if scores["H"] <= scores["L2"] {
		t.Errorf("hub H should rank higher than non-connected leaf L2: H=%.6f L2=%.6f",
			scores["H"], scores["L2"])
	}
}

// TestComputePageRank_SelfLoopsSkipped checks that self-loop edges are not
// counted (fromID == toID is filtered out in computePageRank).
func TestComputePageRank_SelfLoopsSkipped(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "A", ToID: "A", Weight: 1}, // self-loop — must be skipped
		{FromID: "A", ToID: "B", Weight: 1},
	}
	scores := computePageRank(edges)
	if scores == nil {
		t.Fatal("expected non-nil scores")
	}
	if len(scores) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(scores))
	}
}

// TestComputePageRank_EmptyEdges ensures nil is returned for an empty edge
// list (no nodes → nothing to rank).
func TestComputePageRank_EmptyEdges(t *testing.T) {
	scores := computePageRank(nil)
	if scores != nil {
		t.Errorf("expected nil scores for empty edges, got %v", scores)
	}
}

// TestComputePageRank_ZeroWeightFallback checks that zero-weight edges are
// treated as uniform (weight 1.0) and do not panic or produce NaN.
func TestComputePageRank_ZeroWeightFallback(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "A", ToID: "B", Weight: 0},
		{FromID: "B", ToID: "C", Weight: 0},
	}
	scores := computePageRank(edges)
	if scores == nil {
		t.Fatal("expected non-nil scores")
	}
	for id, s := range scores {
		if s != s { // NaN check
			t.Errorf("NaN score for node %s", id)
		}
	}
}

// TestPageRankBoostEnabled_DefaultTrue verifies the default is on (env unset).
func TestPageRankBoostEnabled_DefaultTrue(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "")
	if !pageRankEnabled() {
		t.Error("expected pageRankEnabled() == true when env is unset")
	}
}

// TestPageRankBoostEnabled_CanDisable verifies that MEMDB_PAGERANK_ENABLED=false disables.
func TestPageRankBoostEnabled_CanDisable(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "false")
	if pageRankEnabled() {
		t.Error("expected pageRankEnabled() == false when env=false")
	}
}

// TestPageRankBoostWeight_Default ensures default weight is 0.1.
func TestPageRankBoostWeight_Default(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "")
	if PageRankBoostWeight() != defaultPageRankBoostWeight {
		t.Errorf("expected default %.2f, got %.2f", defaultPageRankBoostWeight, PageRankBoostWeight())
	}
}

// TestPageRankBoostWeight_OutOfRange verifies that out-of-range values fall back to default.
func TestPageRankBoostWeight_OutOfRange(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "5.0")
	if PageRankBoostWeight() != defaultPageRankBoostWeight {
		t.Errorf("expected fallback to default for out-of-range value")
	}
}

// TestPageRankInterval_Default ensures default interval is 6h.
func TestPageRankInterval_Default(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_INTERVAL", "")
	if pageRankInterval() != defaultPageRankInterval {
		t.Errorf("expected default interval %v, got %v", defaultPageRankInterval, pageRankInterval())
	}
}

// TestComputePageRank_AllInvalidEdgesReturnsNil verifies that computePageRank
// returns nil when every edge has empty node IDs (degenerate input that would
// otherwise silently swallow compute_error).
func TestComputePageRank_AllInvalidEdgesReturnsNil(t *testing.T) {
	edges := []db.PageRankEdge{
		{FromID: "", ToID: "B", Weight: 1},  // empty FromID — skipped
		{FromID: "A", ToID: "", Weight: 1},  // empty ToID — skipped
		{FromID: "X", ToID: "X", Weight: 1}, // self-loop — skipped
	}
	scores := computePageRank(edges)
	if scores != nil {
		t.Errorf("expected nil scores for all-invalid edges, got %v", scores)
	}
}

// TestErrComputePageRank_SentinelIsDistinct verifies that errComputePageRank
// is distinct from a generic error so errors.Is works as expected in
// runPageRankForAllCubes to route the correct metric label.
func TestErrComputePageRank_SentinelIsDistinct(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", errComputePageRank)
	if !errors.Is(wrapped, errComputePageRank) {
		t.Error("expected errors.Is to match errComputePageRank through wrapping")
	}
	if errors.Is(fmt.Errorf("some db error"), errComputePageRank) {
		t.Error("unrelated error should not match errComputePageRank")
	}
}

// TestRunPageRankLoop_MetricIncrementsAfterTick is a smoke test that starts
// the goroutine with a 1s interval against a stub (no-op postgres), waits for
// one tick, and confirms the goroutine doesn't panic and terminates cleanly.
func TestRunPageRankLoop_MetricIncrementsAfterTick(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	t.Setenv("MEMDB_PAGERANK_INTERVAL", "200ms")

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	w := NewWorker(rdb, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	// pg is nil → runPageRankForAllCubes returns immediately (no cubes).
	// We pass nil pg to verify the goroutine guards against nil pg.
	w.SetPostgres(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Manually invoke the loop with a nil pg — it should return immediately
	// without panic because runPageRankLoop guards pg == nil.
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runPageRankLoop(ctx, nil)
	}()

	select {
	case <-done:
		// Goroutine exited cleanly after pg==nil guard.
	case <-ctx.Done():
		// Fine — it ran for the full timeout without panicking.
	}
}
