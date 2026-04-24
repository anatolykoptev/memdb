package search

import (
	"math"
	"testing"
	"time"
)

// --- D1 importance multiplier tests ---

func TestImportanceMultiplier_Zero(t *testing.T) {
	meta := map[string]any{"access_count": float64(0)}
	got := importanceMultiplier(meta)
	want := 1.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("access_count=0: got %v, want %v", got, want)
	}
}

func TestImportanceMultiplier_Typical(t *testing.T) {
	meta := map[string]any{"access_count": float64(10)}
	got := importanceMultiplier(meta)
	// 1 + ln(11) ≈ 3.3979
	want := 1.0 + math.Log(11)
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("access_count=10: got %v, want %v", got, want)
	}
}

func TestImportanceMultiplier_Capped(t *testing.T) {
	meta := map[string]any{"access_count": float64(1000)}
	got := importanceMultiplier(meta)
	if got != d1ImportanceCap {
		t.Fatalf("cap not enforced: got %v, want %v", got, d1ImportanceCap)
	}
}

func TestImportanceMultiplier_Missing(t *testing.T) {
	// No access_count key at all — multiplier must degrade to 1.0 (no boost).
	meta := map[string]any{}
	got := importanceMultiplier(meta)
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("missing access_count: got %v, want 1.0", got)
	}
}

func TestImportanceMultiplier_NegativeClamped(t *testing.T) {
	// Defensive: negative value should clamp to 0 → multiplier 1.0.
	meta := map[string]any{"access_count": float64(-5)}
	got := importanceMultiplier(meta)
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("negative clamp failed: got %v, want 1.0", got)
	}
}

// --- D1 combined formula end-to-end (env-gated) ---

func TestApplyDecayToItem_D1Combined_Enabled(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")

	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	// Memory created 180 days ago → recency = exp(-0.0039*180) ≈ 0.4962
	// access_count = 10 → importance = 1 + ln(11) ≈ 3.398
	// final = 0.5 * 0.4962 * 3.398 ≈ 0.843 (capped at 1.0, but 0.843 < 1.0)
	createdAt := now.AddDate(0, 0, -180).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "old but popular", "metadata": map[string]any{
			"relativity":   0.5,
			"created_at":   createdAt,
			"memory_type":  "LongTermMemory",
			"access_count": float64(10),
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)

	expectedRecency := math.Exp(-DefaultDecayAlpha * 180)
	expectedImp := 1.0 + math.Log(11)
	expected := 0.5 * expectedRecency * expectedImp
	if math.Abs(score-expected) > 1e-6 {
		t.Fatalf("D1 combined mismatch: got %f, want %f", score, expected)
	}
}

func TestApplyDecayToItem_D1Combined_CappedAtOne(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")

	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	// Fresh memory (recency=1), cosine=0.9, access_count high → multiplier near cap.
	// 0.9 * 1.0 * 5.0 = 4.5 → must cap to 1.0.
	createdAt := now.Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "hot fresh hit", "metadata": map[string]any{
			"relativity":   0.9,
			"created_at":   createdAt,
			"memory_type":  "LongTermMemory",
			"access_count": float64(10000),
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)
	if score != 1.0 {
		t.Fatalf("expected cap at 1.0, got %f", score)
	}
}

func TestApplyDecayToItem_D1Combined_DisabledFallsBackToLegacy(t *testing.T) {
	// Default: MEMDB_D1_IMPORTANCE unset → legacy weighted sum branch.
	// t.Setenv with "" would still set the var; to be explicit we ensure
	// the value is not "true".
	t.Setenv("MEMDB_D1_IMPORTANCE", "false")

	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	createdAt := now.AddDate(0, 0, -180).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "legacy path", "metadata": map[string]any{
			"relativity":   0.8,
			"created_at":   createdAt,
			"memory_type":  "LongTermMemory",
			"access_count": float64(10), // should be ignored in legacy branch
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)

	// Legacy formula: 0.75*0.8 + 0.25*exp(-0.0039*180).
	expected := DecaySemanticWeight*0.8 + DecayRecencyWeight*math.Exp(-DefaultDecayAlpha*180)
	if math.Abs(score-expected) > 1e-6 {
		t.Fatalf("legacy branch mismatch: got %f, want %f", score, expected)
	}
}

func TestApplyDecayToItem_D1Combined_WorkingMemoryStillExempt(t *testing.T) {
	t.Setenv("MEMDB_D1_IMPORTANCE", "true")

	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	createdAt := now.AddDate(0, 0, -365).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "wm", "metadata": map[string]any{
			"relativity":   0.8,
			"created_at":   createdAt,
			"memory_type":  "WorkingMemory",
			"access_count": float64(50),
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	if meta["relativity"] != 0.8 {
		t.Fatalf("WorkingMemory must be exempt from D1 decay/importance, got %v", meta["relativity"])
	}
}
