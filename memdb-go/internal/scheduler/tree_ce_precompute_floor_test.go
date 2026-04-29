package scheduler

// tree_ce_precompute_floor_test.go — unit tests for the M14 B1.1 unification:
// legacy cosine floor reads MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD and emits
// memdb.scheduler.ce_precompute_floor_dropped_total.
//
// Tests use synthetic embeddings only — no DB, no embed-server, no HTTP.
// All env manipulation is via t.Setenv so state is cleaned up on test exit.

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// resetCEFloorOnce allows test isolation: resets the singleton so the next
// call to ceFloorMx() re-registers the counter on whatever OTel provider is
// set at that point.
func resetCEFloorOnce() {
	ceFloorOnce = sync.Once{}
	ceFloorDropped = nil
}

// TestCEPrecomputeNeighbours_DefaultThreshold_ParityWith030 — with no env
// override, cePrecomputeNeighbours uses threshold=0.3, matching the old
// hardcoded constant. Pairs with cos < 0.3 are excluded; pairs with
// cos >= 0.3 are kept (>= boundary semantics preserved).
func TestCEPrecomputeNeighbours_DefaultThreshold_ParityWith030(t *testing.T) {
	// Ensure env is clean (no override).
	t.Setenv("MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD", "")

	if th := cePrecomputeCosineThreshold(); th != 0.3 {
		t.Fatalf("default threshold = %v, want 0.3", th)
	}

	anchor := makeNeighbour("anchor", unitVec(1, 0, 0))

	// cos values: 0.1, 0.2 are below 0.3 → dropped.
	// 0.3 is at boundary → kept (>= semantics). 0.6 → kept.
	pool := []db.HierarchyMemory{
		anchor,
		makeNeighbour("low1", unitVec(0.1, 0.99, 0)), // cos ≈ 0.10 → drop
		makeNeighbour("low2", unitVec(0.2, 0.97, 0)), // cos ≈ 0.20 → drop
		makeNeighbour("eq30", unitVec(0.3, 0.95, 0)), // cos ≈ 0.30 → keep (boundary)
		makeNeighbour("hi60", unitVec(0.6, 0.80, 0)), // cos ≈ 0.60 → keep
	}

	ctx := context.Background()
	result := cePrecomputeNeighbours(ctx, anchor, pool, 10)

	if len(result) != 2 {
		t.Errorf("got %d neighbours, want 2 (eq30 + hi60); items: %v", len(result), result)
	}
	for _, r := range result {
		if r.ID == "low1" || r.ID == "low2" {
			t.Errorf("neighbour %q should have been dropped by floor", r.ID)
		}
	}
}

// TestCEPrecomputeNeighbours_EnvOverridesFloor — setting
// MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD=0.7 drops pairs with cos in [0.3, 0.7)
// that the default 0.3 floor would have kept.
func TestCEPrecomputeNeighbours_EnvOverridesFloor(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD", "0.7")

	if th := cePrecomputeCosineThreshold(); th != 0.7 {
		t.Fatalf("threshold = %v, want 0.7", th)
	}

	anchor := makeNeighbour("anchor", unitVec(1, 0, 0))

	// 0.5 and 0.6 are ≥0.3 (default) but <0.7 → dropped at threshold=0.7.
	// 0.8 is ≥0.7 → kept.
	pool := []db.HierarchyMemory{
		anchor,
		makeNeighbour("mid50", unitVec(0.5, 0.86, 0)), // cos ≈ 0.50 → drop
		makeNeighbour("mid60", unitVec(0.6, 0.80, 0)), // cos ≈ 0.60 → drop
		makeNeighbour("hi80", unitVec(0.8, 0.60, 0)),  // cos ≈ 0.80 → keep
	}

	ctx := context.Background()
	result := cePrecomputeNeighbours(ctx, anchor, pool, 10)

	if len(result) != 1 {
		t.Errorf("got %d neighbours, want 1 (hi80 only); items: %v", len(result), result)
	}
	if len(result) > 0 && result[0].ID != "hi80" {
		t.Errorf("kept neighbour = %q, want hi80", result[0].ID)
	}
	for _, r := range result {
		if r.ID == "mid50" || r.ID == "mid60" {
			t.Errorf("neighbour %q should be dropped at threshold=0.7 but was kept", r.ID)
		}
	}
}

// TestCEPrecomputeNeighbours_FloorMetricEmitted — verify that ceFloorMx()
// returns a non-nil counter and that cePrecomputeNeighbours increments it
// when pairs are dropped. Uses the OTel SDK ManualReader to observe the
// counter value.
func TestCEPrecomputeNeighbours_FloorMetricEmitted(t *testing.T) {
	// Reset singleton so the counter re-registers on this test's provider.
	resetCEFloorOnce()
	t.Cleanup(resetCEFloorOnce) // leave clean for subsequent tests

	// Wire SDK with a ManualReader so we can collect snapshots.
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(nil) })

	// Default threshold (0.3) — no env override.
	t.Setenv("MEMDB_CE_PRECOMPUTE_COSINE_THRESHOLD", "")

	anchor := makeNeighbour("anchor", unitVec(1, 0, 0))
	// 3 neighbours with cos < 0.3 → all dropped → counter += 3.
	pool := []db.HierarchyMemory{
		anchor,
		makeNeighbour("d1", unitVec(0.05, 0.99, 0)), // cos ≈ 0.05 → drop
		makeNeighbour("d2", unitVec(0.10, 0.99, 0)), // cos ≈ 0.10 → drop
		makeNeighbour("d3", unitVec(0.20, 0.97, 0)), // cos ≈ 0.20 → drop
	}

	ctx := context.Background()
	result := cePrecomputeNeighbours(ctx, anchor, pool, 10)

	if len(result) != 0 {
		t.Errorf("expected no neighbours above floor, got %d", len(result))
	}

	// Collect metrics snapshot.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	// Find the floor_dropped counter in the snapshot.
	const wantName = "memdb.scheduler.ce_precompute_floor_dropped_total"
	var dropped int64
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != wantName {
				continue
			}
			found = true
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %q: unexpected data type %T", wantName, m.Data)
			}
			for _, dp := range sum.DataPoints {
				dropped += dp.Value
			}
		}
	}
	if !found {
		t.Fatalf("metric %q not found in OTel snapshot", wantName)
	}
	if dropped != 3 {
		t.Errorf("floor_dropped_total = %d, want 3", dropped)
	}
}
