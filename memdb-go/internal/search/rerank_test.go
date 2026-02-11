package search

import (
	"math"
	"testing"
)

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
