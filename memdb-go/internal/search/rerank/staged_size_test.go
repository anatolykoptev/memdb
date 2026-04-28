package rerank

import (
	"context"
	"testing"
)

// TestStagedFireStageSize_OnStageSizeCallbackFired verifies that the
// OnStageSize hook fires when the Staged strategy is env-disabled (quick
// path). When MEMDB_SEARCH_STAGED is unset the strategy returns skipped
// immediately — no stage hooks fire. When enabled but no APIURL is set
// the same skipped path is taken. The test focuses on the hook wiring
// itself by calling fireStageSize directly.
func TestStagedFireStageSize_HookWired(t *testing.T) {
	called := map[string]int{}
	s := Staged{
		OnStageSize: func(_ context.Context, stage string, size int) {
			called[stage] += size
		},
	}
	ctx := context.Background()
	s.fireStageSize(ctx, "1_cosine", 10)
	s.fireStageSize(ctx, "2_refine", 8)
	s.fireStageSize(ctx, "3_justify", 5)

	if called["1_cosine"] != 10 {
		t.Errorf("1_cosine: want 10 got %d", called["1_cosine"])
	}
	if called["2_refine"] != 8 {
		t.Errorf("2_refine: want 8 got %d", called["2_refine"])
	}
	if called["3_justify"] != 5 {
		t.Errorf("3_justify: want 5 got %d", called["3_justify"])
	}
}

// TestStagedFireStageSize_NilSafe verifies that a nil OnStageSize does not panic.
func TestStagedFireStageSize_NilSafe(t *testing.T) {
	s := Staged{} // OnStageSize is nil
	s.fireStageSize(context.Background(), "1_cosine", 42)
	// no panic = pass
}
