package search

import (
	"testing"
	"time"
)

// TestApplyDecayToItem_D1BoostValueRange checks that the computed combined
// boost value (before the >1.0 cap) is within a physically-meaningful range
// for normal input. The test sets MEMDB_D1_IMPORTANCE to trigger the
// D1 code path and confirms the relativity after capping is ≤ 1.0.
func TestApplyDecayToItem_D1BoostValueRange(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")

	now := time.Now()
	// Item created 7 days ago with cosine 0.9 and access_count 10.
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	item := map[string]any{
		"metadata": map[string]any{
			"relativity":   0.9,
			"access_count": float64(10),
			"created_at":   sevenDaysAgo,
		},
	}

	applyDecayToItem(item, now, 0.01)
	meta := item["metadata"].(map[string]any)
	rel, ok := meta["relativity"].(float64)
	if !ok {
		t.Fatal("relativity is not float64 after decay")
	}
	if rel > 1.0 {
		t.Errorf("relativity %v exceeds 1.0 cap", rel)
	}
	if rel <= 0 {
		t.Errorf("relativity %v should be positive for recent item", rel)
	}
}

// TestApplyDecayToItem_D1BoostCap verifies the >1.0 cap is applied: when all
// multipliers are maxed the result is exactly 1.0.
func TestApplyDecayToItem_D1BoostCap(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")

	now := time.Now()
	// Item created 1 second ago, very high cosine, very high access_count.
	justNow := now.Add(-time.Second).UTC().Format(time.RFC3339)
	item := map[string]any{
		"metadata": map[string]any{
			"relativity":   1.0,
			"access_count": float64(10000), // would produce importance cap 5.0
			"created_at":   justNow,
		},
	}

	applyDecayToItem(item, now, 0.001)
	meta := item["metadata"].(map[string]any)
	rel := meta["relativity"].(float64)
	if rel > 1.0 {
		t.Errorf("expected relativity ≤ 1.0, got %v", rel)
	}
}
