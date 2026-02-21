package search

import (
	"math"
	"testing"
	"time"
)

// --- Temporal decay v2 tests ---

func TestApplyTemporalDecay_Basic(t *testing.T) {
	now := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	// Memory created 180 days ago → recency = exp(-0.0039*180) ≈ 0.496
	// final = 0.75*0.8 + 0.25*0.496 = 0.600 + 0.124 = 0.724
	createdAt := now.AddDate(0, 0, -180).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "old", "metadata": map[string]any{
			"relativity":  0.8,
			"created_at":  createdAt,
			"memory_type": "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)
	// Score must be lower than original 0.8 (recency < 1.0 pulls it down)
	if score >= 0.8 {
		t.Errorf("expected score < 0.8 after decay, got %f", score)
	}
	// Score must be above 0.75*0.8=0.6 (semantic floor)
	if score < 0.6 {
		t.Errorf("expected score >= 0.6 (semantic floor), got %f", score)
	}
	// Expected: 0.75*0.8 + 0.25*exp(-0.0039*180) ≈ 0.724
	expected := DecaySemanticWeight*0.8 + DecayRecencyWeight*math.Exp(-DefaultDecayAlpha*180)
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("expected score ~%.4f, got %f", expected, score)
	}
}

func TestApplyTemporalDecay_FreshMemory(t *testing.T) {
	now := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	// Memory created today → recency = exp(0) = 1.0
	// final = 0.75*0.9 + 0.25*1.0 = 0.675 + 0.25 = 0.925
	createdAt := now.Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "fresh", "metadata": map[string]any{
			"relativity":  0.9,
			"created_at":  createdAt,
			"memory_type": "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)
	expected := DecaySemanticWeight*0.9 + DecayRecencyWeight*1.0
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("fresh memory score should be ~%.4f, got %f", expected, score)
	}
}

func TestApplyTemporalDecay_LastAccessedAtPriority(t *testing.T) {
	now := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	// created_at is old (365 days), but last_accessed_at is recent (1 day)
	// Should use last_accessed_at → high recency score
	oldCreated := now.AddDate(0, 0, -365).Format(time.RFC3339)
	recentAccess := now.AddDate(0, 0, -1).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "accessed recently", "metadata": map[string]any{
			"relativity":       0.8,
			"created_at":       oldCreated,
			"last_accessed_at": recentAccess,
			"memory_type":      "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)

	// With last_accessed_at=1 day: recency = exp(-0.0039*1) ≈ 0.996 (high)
	// final ≈ 0.75*0.8 + 0.25*0.996 ≈ 0.849
	// Without priority (using created_at=365d): recency = exp(-0.0039*365) ≈ 0.24
	// final ≈ 0.75*0.8 + 0.25*0.24 ≈ 0.660
	// Score should be high (>0.82) proving last_accessed_at was used
	if score < 0.82 {
		t.Errorf("last_accessed_at priority not used: score %f too low (expected >0.82)", score)
	}
}

func TestApplyTemporalDecay_MaxAgeCutoff(t *testing.T) {
	now := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	// Memory older than MaxDecayAgeDays → recency=0, semantic still counts
	veryOld := now.AddDate(0, 0, -(MaxDecayAgeDays + 10)).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "ancient", "metadata": map[string]any{
			"relativity":  0.8,
			"created_at":  veryOld,
			"memory_type": "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)
	// recency=0 → final = 0.75*0.8 + 0.25*0 = 0.6
	expected := DecaySemanticWeight * 0.8
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("beyond MaxDecayAgeDays: expected %.4f (semantic floor), got %f", expected, score)
	}
}

func TestApplyTemporalDecay_WorkingMemoryExempt(t *testing.T) {
	now := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	createdAt := now.AddDate(0, 0, -365).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "wm", "metadata": map[string]any{
			"relativity":  0.8,
			"created_at":  createdAt,
			"memory_type": "WorkingMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	if meta["relativity"] != 0.8 {
		t.Errorf("WorkingMemory should be exempt from decay, got %v", meta["relativity"])
	}
}

func TestApplyTemporalDecay_DisabledAlpha(t *testing.T) {
	now := time.Now()
	createdAt := now.AddDate(-1, 0, 0).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "old", "metadata": map[string]any{
			"relativity":  0.7,
			"created_at":  createdAt,
			"memory_type": "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, 0)
	meta := result[0]["metadata"].(map[string]any)
	if meta["relativity"] != 0.7 {
		t.Errorf("alpha=0 should disable decay, got %v", meta["relativity"])
	}
}

func TestApplyTemporalDecay_MissingTimestamp(t *testing.T) {
	now := time.Now()
	items := []map[string]any{
		{"id": "a", "memory": "no date", "metadata": map[string]any{
			"relativity":  0.75,
			"memory_type": "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	if meta["relativity"] != 0.75 {
		t.Errorf("missing timestamp should leave score unchanged, got %v", meta["relativity"])
	}
}

func TestApplyTemporalDecay_UpdatedAtFallback(t *testing.T) {
	now := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	// No last_accessed_at, but updated_at is recent (2 days)
	oldCreated := now.AddDate(0, 0, -300).Format(time.RFC3339)
	recentUpdated := now.AddDate(0, 0, -2).Format(time.RFC3339)
	items := []map[string]any{
		{"id": "a", "memory": "updated recently", "metadata": map[string]any{
			"relativity":  0.7,
			"created_at":  oldCreated,
			"updated_at":  recentUpdated,
			"memory_type": "LongTermMemory",
		}},
	}
	result := ApplyTemporalDecay(items, now, DefaultDecayAlpha)
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)
	// updated_at=2 days: recency ≈ exp(-0.0039*2) ≈ 0.992 (high)
	// final ≈ 0.75*0.7 + 0.25*0.992 ≈ 0.773
	// If created_at was used (300 days): recency ≈ exp(-0.0039*300) ≈ 0.31
	// final ≈ 0.75*0.7 + 0.25*0.31 ≈ 0.603
	if score < 0.75 {
		t.Errorf("updated_at fallback not used: score %f too low (expected >0.75)", score)
	}
}

func TestReRankByCosine_Basic(t *testing.T) {
	queryVec := []float32{1, 0, 0}
	items := []map[string]any{
		{
			"id":       "a",
			"memory":   "aligned",
			"metadata": map[string]any{"relativity": 0.5}, // low PolarDB score
		},
		{
			"id":       "b",
			"memory":   "orthogonal",
			"metadata": map[string]any{"relativity": 0.9}, // high PolarDB score
		},
	}
	embByID := map[string][]float32{
		"a": {1, 0, 0}, // identical to query → cosine 1.0
		"b": {0, 1, 0}, // orthogonal → cosine 0.0
	}

	result := ReRankByCosine(queryVec, items, embByID)
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}

	// After reranking, "a" (cosine 1.0) should be first, "b" (cosine 0.0) second
	if result[0]["id"] != "a" {
		t.Errorf("expected 'a' first, got %v", result[0]["id"])
	}

	// Check that the score was updated
	meta := result[0]["metadata"].(map[string]any)
	score := meta["relativity"].(float64)
	if math.Abs(score-1.0) > 0.01 {
		t.Errorf("expected score ~1.0, got %f", score)
	}
}

func TestReRankByCosine_NoEmbeddings(t *testing.T) {
	queryVec := []float32{1, 0, 0}
	items := []map[string]any{
		{
			"id":       "a",
			"memory":   "no embedding",
			"metadata": map[string]any{"relativity": 0.9},
		},
	}
	// No embeddings available
	result := ReRankByCosine(queryVec, items, map[string][]float32{})
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	// Score should be unchanged
	meta := result[0]["metadata"].(map[string]any)
	if meta["relativity"] != 0.9 {
		t.Errorf("score changed to %v, expected 0.9", meta["relativity"])
	}
}

func TestReRankByCosine_EmptyQueryVec(t *testing.T) {
	items := []map[string]any{
		{"id": "a", "memory": "test", "metadata": map[string]any{"relativity": 0.8}},
	}
	result := ReRankByCosine(nil, items, map[string][]float32{"a": {1, 0, 0}})
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
}

func TestReRankByCosine_Empty(t *testing.T) {
	result := ReRankByCosine([]float32{1, 0}, nil, nil)
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

func TestReRankByCosine_MixedEmbeddings(t *testing.T) {
	queryVec := []float32{1, 0, 0}
	items := []map[string]any{
		{"id": "a", "memory": "has emb", "metadata": map[string]any{"relativity": 0.5}},
		{"id": "b", "memory": "no emb", "metadata": map[string]any{"relativity": 0.8}},
		{"id": "c", "memory": "has emb2", "metadata": map[string]any{"relativity": 0.3}},
	}
	embByID := map[string][]float32{
		"a": {0.9, 0.1, 0.0}, // high similarity
		"c": {0.0, 0.0, 1.0}, // low similarity
	}

	result := ReRankByCosine(queryVec, items, embByID)
	// "a" should be reranked high (cosine ~0.99), "b" keeps 0.8, "c" reranked low (~0)
	// Order: a (~0.99), b (0.8), c (~0)
	if result[0]["id"] != "a" {
		t.Errorf("expected 'a' first, got %v", result[0]["id"])
	}
	if result[1]["id"] != "b" {
		t.Errorf("expected 'b' second, got %v", result[1]["id"])
	}
	if result[2]["id"] != "c" {
		t.Errorf("expected 'c' third, got %v", result[2]["id"])
	}
}
