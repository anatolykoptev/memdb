package rerank

import (
	"context"
	"math"
	"testing"
)

// ---- helper ----------------------------------------------------------------

func stubWithMeta(id string, score float64, meta map[string]any) *stubItem {
	it := newStub(id, score)
	for k, v := range meta {
		it.meta[k] = v
	}
	return it
}

// ---- TestApplyMetadataBoost_NoMatch_NoChange --------------------------------

func TestApplyMetadataBoost_NoMatch_NoChange(t *testing.T) {
	it := stubWithMeta("x", 0.8, map[string]any{
		"user_id":    "other-user",
		"session_id": "other-session",
		"tags":       []string{"alpha", "beta"},
	})
	w := BoostWeights{UserID: 0.5, SessionID: 0.3, TagsEach: 0.2, TagsCap: 0.6}
	applyMetadataBoost(context.Background(), []Item{it}, "my-user", "my-session", []string{"gamma"}, w)

	if it.Score() != 0.8 {
		t.Errorf("score should be unchanged, got %v", it.Score())
	}
	if _, ok := it.GetMeta("metadata_boost"); ok {
		t.Error("metadata_boost should not be set when no factors matched")
	}
}

// ---- TestApplyMetadataBoost_UserIDMatch ------------------------------------

func TestApplyMetadataBoost_UserIDMatch(t *testing.T) {
	const baseScore = 0.6
	it := stubWithMeta("x", baseScore, map[string]any{"user_id": "alice"})
	w := BoostWeights{UserID: 0.5}
	applyMetadataBoost(context.Background(), []Item{it}, "alice", "", nil, w)

	want := baseScore * 1.5
	if math.Abs(it.Score()-want) > 1e-9 {
		t.Errorf("score: want %.4f, got %.4f", want, it.Score())
	}
	if v, ok := it.GetMeta("metadata_boost"); !ok || math.Abs(v.(float64)-0.5) > 1e-9 {
		t.Errorf("metadata_boost: want 0.5, got %v", v)
	}
}

// ---- TestApplyMetadataBoost_SessionIDMatch ----------------------------------

func TestApplyMetadataBoost_SessionIDMatch(t *testing.T) {
	const baseScore = 0.5
	it := stubWithMeta("x", baseScore, map[string]any{"session_id": "sess42"})
	w := BoostWeights{SessionID: 0.3}
	applyMetadataBoost(context.Background(), []Item{it}, "", "sess42", nil, w)

	want := baseScore * 1.3
	if math.Abs(it.Score()-want) > 1e-9 {
		t.Errorf("score: want %.4f, got %.4f", want, it.Score())
	}
	if v, ok := it.GetMeta("metadata_boost"); !ok || math.Abs(v.(float64)-0.3) > 1e-9 {
		t.Errorf("metadata_boost: want 0.3, got %v", v)
	}
}

// ---- TestApplyMetadataBoost_TagsOverlap_3Tags_Capped -----------------------

// 4 overlapping tags × 0.2 = 0.8, but cap is 0.6 → boost = 0.6 → score × 1.6.
func TestApplyMetadataBoost_TagsOverlap_3Tags_Capped(t *testing.T) {
	const baseScore = 1.0
	it := stubWithMeta("x", baseScore, map[string]any{
		"tags": []string{"a", "b", "c", "d", "z"},
	})
	w := BoostWeights{TagsEach: 0.2, TagsCap: 0.6}
	queryTags := []string{"a", "b", "c", "d"} // 4 overlap → 4×0.2=0.8 → capped 0.6

	applyMetadataBoost(context.Background(), []Item{it}, "", "", queryTags, w)

	want := baseScore * 1.6
	if math.Abs(it.Score()-want) > 1e-9 {
		t.Errorf("score: want %.4f, got %.4f", want, it.Score())
	}
	if v, ok := it.GetMeta("metadata_boost"); !ok || math.Abs(v.(float64)-0.6) > 1e-9 {
		t.Errorf("metadata_boost: want 0.6 (capped), got %v", v)
	}
}

// ---- TestApplyMetadataBoost_AllThree ----------------------------------------

// user_id(0.5) + session_id(0.3) + tags×2(0.4) = 1.2 → score × 2.2
func TestApplyMetadataBoost_AllThree(t *testing.T) {
	const baseScore = 0.5
	it := stubWithMeta("x", baseScore, map[string]any{
		"user_id":    "bob",
		"session_id": "s99",
		"tags":       []string{"x", "y", "z"},
	})
	w := BoostWeights{UserID: 0.5, SessionID: 0.3, TagsEach: 0.2, TagsCap: 0.6}
	queryTags := []string{"x", "y"} // 2 overlap → 2×0.2 = 0.4 (under cap)

	applyMetadataBoost(context.Background(), []Item{it}, "bob", "s99", queryTags, w)

	wantBoost := 0.5 + 0.3 + 0.4 // = 1.2
	want := baseScore * (1.0 + wantBoost)
	if math.Abs(it.Score()-want) > 1e-9 {
		t.Errorf("score: want %.4f, got %.4f", want, it.Score())
	}
	if v, ok := it.GetMeta("metadata_boost"); !ok || math.Abs(v.(float64)-wantBoost) > 1e-9 {
		t.Errorf("metadata_boost: want %.4f, got %v", wantBoost, v)
	}
}

// ---- TestApplyMetadataBoost_EmptyQueryUserID_NoBoost -----------------------

func TestApplyMetadataBoost_EmptyQueryUserID_NoBoost(t *testing.T) {
	it := stubWithMeta("x", 0.7, map[string]any{"user_id": ""})
	w := BoostWeights{UserID: 0.5}
	// Empty queryUserID must not match even when item also has empty user_id.
	applyMetadataBoost(context.Background(), []Item{it}, "", "", nil, w)

	if it.Score() != 0.7 {
		t.Errorf("empty queryUserID is not a wildcard: score should be 0.7, got %v", it.Score())
	}
	if _, ok := it.GetMeta("metadata_boost"); ok {
		t.Error("metadata_boost should not be set")
	}
}

// ---- TestDefaultBoostWeights_EnvOverride -----------------------------------

func TestDefaultBoostWeights_EnvOverride(t *testing.T) {
	t.Setenv("MEMDB_RERANK_BOOST_USER_ID", "0.9")
	t.Setenv("MEMDB_RERANK_BOOST_SESSION_ID", "0.1")
	t.Setenv("MEMDB_RERANK_BOOST_TAGS", "0.05")
	t.Setenv("MEMDB_RERANK_BOOST_TAGS_CAP", "0.15")

	w := DefaultBoostWeights()
	if w.UserID != 0.9 {
		t.Errorf("UserID: want 0.9, got %v", w.UserID)
	}
	if w.SessionID != 0.1 {
		t.Errorf("SessionID: want 0.1, got %v", w.SessionID)
	}
	if w.TagsEach != 0.05 {
		t.Errorf("TagsEach: want 0.05, got %v", w.TagsEach)
	}
	if w.TagsCap != 0.15 {
		t.Errorf("TagsCap: want 0.15, got %v", w.TagsCap)
	}
}

// ---- TestDefaultBoostWeights_Defaults --------------------------------------

func TestDefaultBoostWeights_Defaults(t *testing.T) {
	// Ensure env vars are not set (they might be set from a previous test
	// but t.Setenv restores on cleanup — no cross-test contamination).
	t.Setenv("MEMDB_RERANK_BOOST_USER_ID", "")
	t.Setenv("MEMDB_RERANK_BOOST_SESSION_ID", "")
	t.Setenv("MEMDB_RERANK_BOOST_TAGS", "")
	t.Setenv("MEMDB_RERANK_BOOST_TAGS_CAP", "")

	w := DefaultBoostWeights()
	if w.UserID != 0.5 {
		t.Errorf("UserID default: want 0.5, got %v", w.UserID)
	}
	if w.SessionID != 0.3 {
		t.Errorf("SessionID default: want 0.3, got %v", w.SessionID)
	}
	if w.TagsEach != 0.2 {
		t.Errorf("TagsEach default: want 0.2, got %v", w.TagsEach)
	}
	if w.TagsCap != 0.6 {
		t.Errorf("TagsCap default: want 0.6, got %v", w.TagsCap)
	}
}

// ---- TestApplyMetadataBoost_TagsSliceAny -----------------------------------

// Item tags stored as []any (JSON-decoded shape) must still match.
func TestApplyMetadataBoost_TagsSliceAny(t *testing.T) {
	it := stubWithMeta("x", 1.0, map[string]any{
		"tags": []any{"go", "search", "memory"},
	})
	w := BoostWeights{TagsEach: 0.2, TagsCap: 0.6}
	applyMetadataBoost(context.Background(), []Item{it}, "", "", []string{"go", "memory"}, w)

	wantBoost := 0.4 // 2 tags × 0.2
	want := 1.0 * (1.0 + wantBoost)
	if math.Abs(it.Score()-want) > 1e-9 {
		t.Errorf("score: want %.4f, got %.4f", want, it.Score())
	}
}
