package search

// rerank_pagerank_test.go — unit tests for the M10 S7 PageRank boost in D1 rerank.

import (
	"math"
	"testing"
	"time"
)

// TestPageRankMultiplier_PresentFloat verifies the basic formula:
// multiplier = 1 + pagerank * weight, when pagerank is stored as float64.
func TestPageRankMultiplier_PresentFloat(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "0.1")

	meta := map[string]any{"pagerank": float64(0.5)}
	got := pageRankMultiplier(meta)
	want := 1.0 + 0.5*0.1 // 1.05
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("expected %.6f, got %.6f", want, got)
	}
}

// TestPageRankMultiplier_PresentString verifies the multiplier when pagerank
// is stored as a text string (agtype TEXT round-trip from BulkSetPageRank).
func TestPageRankMultiplier_PresentString(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "0.1")

	meta := map[string]any{"pagerank": "0.5"}
	got := pageRankMultiplier(meta)
	want := 1.0 + 0.5*0.1
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("expected %.6f, got %.6f", want, got)
	}
}

// TestPageRankMultiplier_Absent verifies identity (1.0) when pagerank is missing.
func TestPageRankMultiplier_Absent(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	meta := map[string]any{}
	got := pageRankMultiplier(meta)
	if got != 1.0 {
		t.Errorf("expected 1.0 for absent pagerank, got %.6f", got)
	}
}

// TestPageRankMultiplier_Zero verifies identity (1.0) when pagerank == 0.
func TestPageRankMultiplier_Zero(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	meta := map[string]any{"pagerank": float64(0)}
	got := pageRankMultiplier(meta)
	if got != 1.0 {
		t.Errorf("expected 1.0 for zero pagerank, got %.6f", got)
	}
}

// TestPageRankMultiplier_GateOff verifies that when MEMDB_PAGERANK_ENABLED=false
// the multiplier is always 1.0 regardless of the stored score.
func TestPageRankMultiplier_GateOff(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_ENABLED", "false")
	meta := map[string]any{"pagerank": float64(0.9)}
	got := pageRankMultiplier(meta)
	if got != 1.0 {
		t.Errorf("expected 1.0 when gate is off, got %.6f", got)
	}
}

// TestApplyDecayToItem_D1WithPageRankBoost verifies end-to-end that the D1
// formula applies the PageRank multiplier when both MEMDB_D1_IMPORTANCE and
// MEMDB_PAGERANK_ENABLED are true.
//
// Setup: cosine=0.8, recency≈1.0 (recent memory), access_count=0 (imp=1.0),
//        no hierarchy, pagerank=0.5 → multiplier=1.05.
//
// Expected: relativity = 0.8 * ≈1.0 * 1.0 * 1.0 * 1.05 ≈ 0.84, capped at 1.0.
func TestApplyDecayToItem_D1WithPageRankBoost(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "0.1")
	t.Setenv("MEMDB_REORG_HIERARCHY", "false")

	now := time.Now()
	item := map[string]any{
		"metadata": map[string]any{
			"memory_type":  "LongTermMemory",
			"created_at":   now.UTC().Format(time.RFC3339),
			"access_count": float64(0),
			"relativity":   float64(0.8),
			"pagerank":     float64(0.5),
		},
	}
	applyDecayToItem(item, now, 0.0039)

	meta := item["metadata"].(map[string]any)
	got, _ := meta["relativity"].(float64)

	// recency = exp(-0.0039 * 0) = 1.0 (same-day), imp = 1.0, hier = 1.0, pr = 1.05
	// combined = 0.8 * 1.0 * 1.0 * 1.0 * 1.05 = 0.84
	want := 0.84
	if math.Abs(got-want) > 0.01 { // allow 1% tolerance for recency rounding
		t.Errorf("D1+pagerank: expected relativity≈%.3f, got %.6f", want, got)
	}
}

// TestApplyDecayToItem_D1NoPageRank verifies that when pagerank is absent the
// D1 formula produces the same result as before M10 S7.
func TestApplyDecayToItem_D1NoPageRank(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")
	t.Setenv("MEMDB_PAGERANK_ENABLED", "true")
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "0.1")
	t.Setenv("MEMDB_REORG_HIERARCHY", "false")

	now := time.Now()
	item := map[string]any{
		"metadata": map[string]any{
			"memory_type":  "LongTermMemory",
			"created_at":   now.UTC().Format(time.RFC3339),
			"access_count": float64(0),
			"relativity":   float64(0.8),
			// pagerank intentionally absent
		},
	}
	applyDecayToItem(item, now, 0.0039)

	meta := item["metadata"].(map[string]any)
	got, _ := meta["relativity"].(float64)

	// pagerank absent → multiplier 1.0 → same as pre-M10 result: 0.8
	want := 0.8
	if math.Abs(got-want) > 0.01 {
		t.Errorf("D1 without pagerank: expected relativity≈%.3f, got %.6f", want, got)
	}
}

// TestPageRankBoostWeight_Default verifies default weight is 0.1.
func TestPageRankBoostWeight_DefaultSearch(t *testing.T) {
	t.Setenv("MEMDB_PAGERANK_BOOST_WEIGHT", "")
	if pageRankBoostWeight() != defaultPageRankBoostWeight {
		t.Errorf("expected default %.2f, got %.2f", defaultPageRankBoostWeight, pageRankBoostWeight())
	}
}
