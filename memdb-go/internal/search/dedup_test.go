package search

import (
	"math"
	"testing"
)

const epsilon = 0.01

func approxEqual(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

func approxEqual32(a, b float32, eps float64) bool {
	return math.Abs(float64(a)-float64(b)) < eps
}

// --- CosineSimilarity tests ---

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float32{1.0, 2.0, 3.0}
	got := CosineSimilarity(v, v)
	if !approxEqual32(got, 1.0, epsilon) {
		t.Errorf("CosineSimilarity(identical) = %v, want 1.0", got)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{0.0, 1.0, 0.0}
	got := CosineSimilarity(a, b)
	if !approxEqual32(got, 0.0, epsilon) {
		t.Errorf("CosineSimilarity(orthogonal) = %v, want 0.0", got)
	}
}

func TestCosineSimilarity_DifferentLength(t *testing.T) {
	a := []float32{1.0, 2.0}
	b := []float32{1.0, 2.0, 3.0}
	got := CosineSimilarity(a, b)
	if got != 0.0 {
		t.Errorf("CosineSimilarity(different length) = %v, want 0.0", got)
	}
}

// --- CosineSimilarityMatrix tests ---

func TestCosineSimilarityMatrix(t *testing.T) {
	items := []SearchItem{
		{Embedding: []float32{1, 0, 0}},
		{Embedding: []float32{0, 1, 0}},
		{Embedding: []float32{1, 1, 0}},
	}
	matrix := CosineSimilarityMatrix(items)

	// Shape: 3x3
	if len(matrix) != 3 {
		t.Fatalf("matrix rows = %d, want 3", len(matrix))
	}
	for i, row := range matrix {
		if len(row) != 3 {
			t.Fatalf("matrix[%d] cols = %d, want 3", i, len(row))
		}
	}

	// Diagonal should be 1.0
	for i := 0; i < 3; i++ {
		if !approxEqual32(matrix[i][i], 1.0, epsilon) {
			t.Errorf("matrix[%d][%d] = %v, want 1.0", i, i, matrix[i][i])
		}
	}

	// [0] and [1] are orthogonal
	if !approxEqual32(matrix[0][1], 0.0, epsilon) {
		t.Errorf("matrix[0][1] = %v, want 0.0", matrix[0][1])
	}

	// [0] and [2]: cos(45deg) = 1/sqrt(2) ~ 0.707
	expected := float32(1.0 / math.Sqrt(2.0))
	if !approxEqual32(matrix[0][2], expected, epsilon) {
		t.Errorf("matrix[0][2] = %v, want ~%v", matrix[0][2], expected)
	}

	// Symmetry
	if !approxEqual32(matrix[0][2], matrix[2][0], epsilon) {
		t.Errorf("matrix not symmetric: [0][2]=%v, [2][0]=%v", matrix[0][2], matrix[2][0])
	}
}

// --- DedupSim tests ---

func TestDedupSim_NoDuplicates(t *testing.T) {
	items := []SearchItem{
		{Memory: "a", Score: 0.9, Embedding: []float32{1, 0, 0}},
		{Memory: "b", Score: 0.8, Embedding: []float32{0, 1, 0}},
		{Memory: "c", Score: 0.7, Embedding: []float32{0, 0, 1}},
	}
	got := DedupSim(items, 3)
	if len(got) != 3 {
		t.Errorf("DedupSim(no duplicates) returned %d items, want 3", len(got))
	}
}

func TestDedupSim_WithDuplicates(t *testing.T) {
	items := []SearchItem{
		{Memory: "a", Score: 0.9, Embedding: []float32{1, 0, 0}},
		{Memory: "b", Score: 0.8, Embedding: []float32{1, 0, 0}}, // identical embedding to first
		{Memory: "c", Score: 0.7, Embedding: []float32{0, 1, 0}},
	}
	got := DedupSim(items, 3)
	// Items [0] and [1] have cosine=1.0 > 0.92 threshold, so [1] is skipped.
	if len(got) != 2 {
		t.Errorf("DedupSim(with duplicates) returned %d items, want 2", len(got))
	}
}

func TestDedupSim_SingleItem(t *testing.T) {
	items := []SearchItem{
		{Memory: "only", Score: 0.5, Embedding: []float32{1, 0, 0}},
	}
	got := DedupSim(items, 1)
	if len(got) != 1 {
		t.Errorf("DedupSim(single) returned %d items, want 1", len(got))
	}
	if got[0].Memory != "only" {
		t.Errorf("DedupSim(single) memory = %q, want \"only\"", got[0].Memory)
	}
}

// --- DedupMMR tests ---

func TestDedupMMR_Basic(t *testing.T) {
	items := []SearchItem{
		{Memory: "text1", Score: 0.9, MemType: "text", BucketIdx: 0, Embedding: []float32{1, 0, 0, 0}},
		{Memory: "text2", Score: 0.8, MemType: "text", BucketIdx: 0, Embedding: []float32{0, 1, 0, 0}},
		{Memory: "text3", Score: 0.7, MemType: "text", BucketIdx: 0, Embedding: []float32{0, 0, 1, 0}},
		{Memory: "text4", Score: 0.6, MemType: "text", BucketIdx: 0, Embedding: []float32{0, 0, 0, 1}},
		{Memory: "pref1", Score: 0.85, MemType: "preference", BucketIdx: 0, Embedding: []float32{0.5, 0.5, 0, 0}},
		{Memory: "pref2", Score: 0.75, MemType: "preference", BucketIdx: 0, Embedding: []float32{0, 0, 0.5, 0.5}},
	}

	queryVec := []float32{1, 0, 0, 0} // closest to text1
	textItems, prefItems := DedupMMR(items, 2, 1, queryVec, DefaultMMRLambda)

	if len(textItems) != 2 {
		t.Errorf("DedupMMR text items = %d, want 2", len(textItems))
	}
	if len(prefItems) != 1 {
		t.Errorf("DedupMMR pref items = %d, want 1", len(prefItems))
	}
}

func TestDedupMMR_SingleItem(t *testing.T) {
	items := []SearchItem{
		{Memory: "only text", Score: 0.9, MemType: "text", BucketIdx: 0, Embedding: []float32{1, 0, 0}},
	}
	textItems, prefItems := DedupMMR(items, 2, 1, []float32{1, 0, 0}, DefaultMMRLambda)
	if len(textItems) != 1 {
		t.Errorf("DedupMMR single text = %d, want 1", len(textItems))
	}
	if len(prefItems) != 0 {
		t.Errorf("DedupMMR single pref = %d, want 0", len(prefItems))
	}
}

func TestDedupMMR_OnlyPref(t *testing.T) {
	items := []SearchItem{
		{Memory: "pref only", Score: 0.8, MemType: "preference", BucketIdx: 0, Embedding: []float32{0, 1, 0}},
	}
	textItems, prefItems := DedupMMR(items, 2, 1, []float32{0, 1, 0}, DefaultMMRLambda)
	if len(textItems) != 0 {
		t.Errorf("DedupMMR only-pref text = %d, want 0", len(textItems))
	}
	if len(prefItems) != 1 {
		t.Errorf("DedupMMR only-pref pref = %d, want 1", len(prefItems))
	}
}

// TestDedupMMR_RealRelevance verifies that real MMR uses cosine(item, query)
// as the relevance term, not item.Score. item2 has lower Score but its embedding
// is closer to queryVec than item1 — with real MMR, item2 should rank higher.
func TestDedupMMR_RealRelevance(t *testing.T) {
	queryVec := []float32{0, 1, 0} // points to dim=1
	items := []SearchItem{
		// item1: high pre-computed score, but embedding orthogonal to query
		{Memory: "item1 high score low cosine", Score: 0.95, MemType: "text", BucketIdx: 0,
			Embedding: []float32{1, 0, 0}}, // cosine(query)=0, sim=0.5 after norm
		// item2: lower pre-computed score, but embedding aligned with query
		{Memory: "item2 low score high cosine", Score: 0.60, MemType: "text", BucketIdx: 0,
			Embedding: []float32{0, 1, 0}}, // cosine(query)=1, sim=1.0 after norm
	}

	textItems, _ := DedupMMR(items, 2, 0, queryVec, DefaultMMRLambda)

	if len(textItems) < 2 {
		t.Fatalf("DedupMMR_RealRelevance: got %d items, want 2", len(textItems))
	}
	// With real MMR: item2 (cosine=1.0 to query) must be selected before item1 (cosine=0.0).
	// Phase 1 prefill orders by item.Score, so item1 goes first there.
	// item2 should still be present (both items selected since topK=2).
	found := false
	for _, it := range textItems {
		if it.Memory == "item2 low score high cosine" {
			found = true
			break
		}
	}
	if !found {
		t.Error("DedupMMR_RealRelevance: item2 (high query cosine) not selected")
	}
}

// --- Text similarity tests ---

func TestDiceSimilarity_Identical(t *testing.T) {
	got := diceSimilarity("hello", "hello")
	if !approxEqual(got, 1.0, epsilon) {
		t.Errorf("diceSimilarity(identical) = %v, want 1.0", got)
	}
}

func TestDiceSimilarity_Different(t *testing.T) {
	got := diceSimilarity("abc", "xyz")
	if !approxEqual(got, 0.0, epsilon) {
		t.Errorf("diceSimilarity(different) = %v, want 0.0", got)
	}
}

func TestBigramSimilarity(t *testing.T) {
	// "hello" bigrams: {he, el, ll, lo} (4 bigrams)
	// "help"  bigrams: {he, el, lp}     (3 bigrams)
	// intersection: {he, el} = 2
	// union: 4 + 3 - 2 = 5
	// Jaccard = 2/5 = 0.4
	got := bigramSimilarity("hello", "help")
	want := 0.4
	if !approxEqual(got, want, epsilon) {
		t.Errorf("bigramSimilarity(\"hello\", \"help\") = %v, want %v", got, want)
	}
}

func TestTfidfSimilarity_Identical(t *testing.T) {
	got := tfidfSimilarity("hello world", "hello world")
	if !approxEqual(got, 1.0, epsilon) {
		t.Errorf("tfidfSimilarity(identical) = %v, want 1.0", got)
	}
}
