package rerank

import (
	"context"
	"math"
	"os"
	"testing"
)

// makeVec2 returns a 2-D unit vector at angle theta (radians) so tests can
// construct arbitrary cosine similarities without hand-computing coordinates.
// cos(theta) between two such vectors equals cos(angle between them).
func makeVec2(x, y float32) []float32 {
	norm := float32(math.Sqrt(float64(x*x + y*y)))
	if norm == 0 {
		return []float32{0, 0}
	}
	return []float32{x / norm, y / norm}
}

// cosApprox returns the cosine similarity of two 2-D vectors for test assertions.
func cosApprox(a, b []float32) float64 {
	return float64(cosineSim(a, b))
}

func TestMMR_DropsDuplicate_Cosine095(t *testing.T) {
	// Two nearly-identical vectors (cos ≈ 0.95) — second must be dropped
	// since 0.95 ≥ default bar 0.8.
	v1 := makeVec2(1.0, 0.0)
	v2 := makeVec2(0.95, 0.31) // cos(v1,v2) ≈ 0.95

	// Verify the test assumption before relying on it.
	if cosApprox(v1, v2) < 0.9 {
		t.Fatalf("precondition: expected cos≥0.9, got %v", cosApprox(v1, v2))
	}

	a := newStub("a", 0.9)
	b := newStub("b", 0.8)
	m := MMR{
		EmbeddingsByID: map[string][]float32{"a": v1, "b": v2},
		Bar:            0.8,
	}
	out, err := m.Rerank(context.Background(), "q", []Item{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 item after dropping duplicate, got %d", len(out))
	}
	if out[0].ID() != "a" {
		t.Errorf("expected 'a' to be kept (first in rank), got %s", out[0].ID())
	}
	// Verify meta flags
	if v, _ := a.GetMeta("mmr_kept"); v != true {
		t.Error("item 'a' should have mmr_kept=true")
	}
	if v, _ := b.GetMeta("mmr_dropped"); v != true {
		t.Error("item 'b' should have mmr_dropped=true")
	}
}

func TestMMR_KeepsDistinct_Cosine04(t *testing.T) {
	// Two orthogonal-ish vectors (cos ≈ 0.4) — both must be kept.
	v1 := makeVec2(1.0, 0.0)
	v2 := makeVec2(0.4, 0.9) // cos(v1,v2) ≈ 0.4

	if cosApprox(v1, v2) >= 0.8 {
		t.Fatalf("precondition: expected cos<0.8, got %v", cosApprox(v1, v2))
	}

	a := newStub("a", 0.9)
	b := newStub("b", 0.7)
	m := MMR{
		EmbeddingsByID: map[string][]float32{"a": v1, "b": v2},
		Bar:            0.8,
	}
	out, err := m.Rerank(context.Background(), "q", []Item{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected both items kept, got %d", len(out))
	}
}

func TestMMR_TopKLimit(t *testing.T) {
	// 5 items all distinct — TopK=3 should return exactly 3.
	items := make([]Item, 5)
	embByID := make(map[string][]float32)
	ids := []string{"a", "b", "c", "d", "e"}
	// 5 orthogonal unit vectors (all cos = 0 pairwise)
	vecs := [][]float32{
		{1, 0, 0, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 0, 1, 0},
		{0, 0, 0, 0, 1},
	}
	for i, id := range ids {
		items[i] = newStub(id, float64(5-i)*0.1)
		embByID[id] = vecs[i]
	}
	m := MMR{EmbeddingsByID: embByID, Bar: 0.8, TopK: 3}
	out, err := m.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected TopK=3 to cap output, got %d", len(out))
	}
}

func TestMMR_NoEmbedding_KeptByDefault(t *testing.T) {
	// Items without embeddings should always pass through.
	a := newStub("a", 0.9) // no embedding
	b := newStub("b", 0.8) // no embedding
	m := MMR{
		EmbeddingsByID: map[string][]float32{}, // empty — no vectors for a or b
		Bar:            0.8,
	}
	out, err := m.Rerank(context.Background(), "q", []Item{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected both items kept (no embeddings → unconditional keep), got %d", len(out))
	}
}

func TestMMR_BarFromEnv(t *testing.T) {
	// MEMDB_MMR_BAR=0.5 → stricter filter; vectors with cos ≈ 0.6 should be dropped.
	t.Setenv("MEMDB_MMR_BAR", "0.5")

	v1 := makeVec2(1.0, 0.0)
	v2 := makeVec2(0.6, 0.8) // cos(v1,v2) ≈ 0.6 → ≥0.5 → dropped

	if cosApprox(v1, v2) < 0.5 {
		t.Fatalf("precondition: expected cos≥0.5, got %v", cosApprox(v1, v2))
	}

	a := newStub("a", 0.9)
	b := newStub("b", 0.8)

	// Use NewMMRFromEnv to pick up the env override.
	m, _ := NewMMRFromEnv(map[string][]float32{"a": v1, "b": v2})
	out, err := m.Rerank(context.Background(), "q", []Item{a, b})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 item with bar=0.5, got %d (cos was %v)", len(out), cosApprox(v1, v2))
	}
}

func TestMMR_BarFromEnv_DefaultFallback(t *testing.T) {
	// Unset env should fall back to 0.8 default.
	os.Unsetenv("MEMDB_MMR_BAR")
	m, _ := NewMMRFromEnv(nil)
	if m.Bar != 0.8 {
		t.Errorf("expected default bar=0.8, got %v", m.Bar)
	}
}

func TestMMR_LengthOne_Passthrough(t *testing.T) {
	a := newStub("a", 0.9)
	m := MMR{EmbeddingsByID: map[string][]float32{"a": {1, 0}}, Bar: 0.8}
	out, err := m.Rerank(context.Background(), "q", []Item{a})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID() != "a" {
		t.Errorf("single-item input must pass through unchanged")
	}
}

func TestMMR_EmptyInput(t *testing.T) {
	m := MMR{EmbeddingsByID: map[string][]float32{}, Bar: 0.8}
	out, err := m.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("empty input must return empty output, got %d", len(out))
	}
}

func TestMMR_NameIsMMR(t *testing.T) {
	m := MMR{}
	if m.Name() != "mmr" {
		t.Errorf("MMR.Name() must be 'mmr', got %q", m.Name())
	}
}

func TestMMR_ImplementsReranker(t *testing.T) {
	// Compile-time interface check surfaced as a runtime no-op test.
	var _ Reranker = MMR{}
	_ = t
}
