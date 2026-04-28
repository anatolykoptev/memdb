package rerank

import (
	"context"
	"math"
	"testing"
)

func TestCosine_BasicSortByScore(t *testing.T) {
	a := newStub("a", 0.5)
	b := newStub("b", 0.9)
	items := []Item{a, b}
	c := Cosine{
		QueryVec: []float32{1, 0, 0},
		EmbeddingsByID: map[string][]float32{
			"a": {1, 0, 0}, // identical → cosine 1.0 → score 1.0
			"b": {0, 1, 0}, // orthogonal → cosine 0.0 → score 0.5
		},
	}
	out, err := c.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].ID() != "a" {
		t.Errorf("a should sort first after cosine rerank, got %s", out[0].ID())
	}
	if math.Abs(out[0].Score()-1.0) > 0.01 {
		t.Errorf("a score expected ~1.0, got %v", out[0].Score())
	}
}

func TestCosine_NoEmbeddingsKeepsScore(t *testing.T) {
	a := newStub("a", 0.9)
	c := Cosine{
		QueryVec:       []float32{1, 0, 0},
		EmbeddingsByID: map[string][]float32{},
	}
	out, _ := c.Rerank(context.Background(), "q", []Item{a})
	if out[0].Score() != 0.9 {
		t.Errorf("score should be unchanged when no embedding present, got %v", out[0].Score())
	}
}

func TestCosine_EmptyQueryVecSkips(t *testing.T) {
	a := newStub("a", 0.5)
	c := Cosine{QueryVec: nil, EmbeddingsByID: map[string][]float32{"a": {1}}}
	out, err := c.Rerank(context.Background(), "q", []Item{a})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected passthrough")
	}
	if out[0].Score() != 0.5 {
		t.Errorf("score must be untouched on empty QueryVec, got %v", out[0].Score())
	}
}

func TestCosine_EmptyItems(t *testing.T) {
	c := Cosine{QueryVec: []float32{1, 0}}
	out, err := c.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty result, got %d items", len(out))
	}
}

func TestCosine_NameStable(t *testing.T) {
	c := Cosine{}
	if c.Name() != "cosine" {
		t.Errorf("Cosine.Name() must be 'cosine'")
	}
}
