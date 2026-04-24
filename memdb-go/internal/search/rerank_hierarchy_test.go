package search

import (
	"math"
	"testing"
	"time"
)

// rerank_hierarchy_test.go — D3 hierarchy boost verification.
//
// Boost multiplier shape (semantic 1.15, episodic 1.08, raw 1.0) is only
// applied inside the D1 combined-formula branch, gated additionally by
// MEMDB_REORG_HIERARCHY=true. These tests lock in both behaviours.

func TestHierarchyBoost_LevelMapping(t *testing.T) {
	cases := map[string]float64{
		"semantic": 1.15,
		"episodic": 1.08,
		"raw":      1.0,
		"":         1.0, // missing level → no boost
		"unknown":  1.0, // unknown string → no boost
	}
	for lvl, want := range cases {
		meta := map[string]any{"hierarchy_level": lvl}
		got := hierarchyBoost(meta)
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("hierarchyBoost(%q) = %v, want %v", lvl, got, want)
		}
	}
}

func TestHierarchyBoost_DisabledWhenEnvOff(t *testing.T) {
	t.Setenv("MEMDB_REORG_HIERARCHY", "")
	if hierarchyBoostEnabled() {
		t.Error("hierarchyBoostEnabled should be false when env unset")
	}
	t.Setenv("MEMDB_REORG_HIERARCHY", "true")
	if !hierarchyBoostEnabled() {
		t.Error("hierarchyBoostEnabled should be true when env=true")
	}
}

func TestApplyDecayToItem_D1_HierarchyBoostApplied(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")
	t.Setenv("MEMDB_REORG_HIERARCHY", "true")

	now := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	createdAt := now.Format(time.RFC3339) // fresh → recency=1

	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{
			"relativity":      0.5,
			"created_at":      createdAt,
			"memory_type":     "EpisodicMemory",
			"access_count":    float64(0),    // multiplier 1.0
			"hierarchy_level": "semantic",    // boost 1.15
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)

	// 0.5 * 1.0 * 1.0 * 1.15 = 0.575
	want := 0.5 * 1.15
	if math.Abs(score-want) > 1e-6 {
		t.Fatalf("semantic boost mismatch: got %v, want %v", score, want)
	}
}

func TestApplyDecayToItem_D1_HierarchyIgnoredWhenEnvOff(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")
	t.Setenv("MEMDB_REORG_HIERARCHY", "")

	now := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	createdAt := now.Format(time.RFC3339)

	items := []map[string]any{
		{"id": "a", "metadata": map[string]any{
			"relativity":      0.5,
			"created_at":      createdAt,
			"memory_type":     "LongTermMemory",
			"access_count":    float64(0),
			"hierarchy_level": "semantic",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)

	// Hierarchy boost disabled → 0.5 * 1.0 * 1.0 * 1.0 = 0.5.
	if math.Abs(score-0.5) > 1e-6 {
		t.Fatalf("boost must be off when env=false: got %v, want 0.5", score)
	}
}
