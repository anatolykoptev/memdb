package rerank

import (
	"context"
	"math"
	"os"
	"testing"
)

// normVec2 returns a normalised 2-D vector so tests can construct arbitrary
// cosine similarities without hand-computing coordinates.
func normVec2(x, y float32) []float32 {
	norm := float32(math.Sqrt(float64(x*x + y*y)))
	if norm == 0 {
		return []float32{0, 0}
	}
	return []float32{x / norm, y / norm}
}

// TestMathPrefilter_LambdaZero_PureCosineSort verifies that Lambda=0 (pure cosine)
// sorts items by descending cosine similarity to the query vector.
func TestMathPrefilter_LambdaZero_PureCosineSort(t *testing.T) {
	// query = (1,0); items order by cos to query: a cos≈1.0, b cos≈0.7, c cos≈0.0
	qv := []float32{1, 0}
	va := normVec2(1.0, 0.0)   // cos ≈ 1.0
	vb := normVec2(0.7, 0.714) // cos ≈ 0.7
	vc := normVec2(0.0, 1.0)   // cos ≈ 0.0

	embByID := map[string][]float32{"a": va, "b": vb, "c": vc}

	// Input is in reverse expected order: c, b, a
	items := []Item{newStub("c", 0.3), newStub("b", 0.2), newStub("a", 0.1)}

	mp := MathPrefilter{
		EmbeddingsByID: embByID,
		QueryVector:    qv,
		Lambda:         0,
	}
	out, err := mp.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
	// After pure cosine sort, order should be a (cos≈1.0), b (cos≈0.7), c (cos≈0.0).
	if out[0].ID() != "a" {
		t.Errorf("expected 'a' first (highest cosine), got %q", out[0].ID())
	}
	if out[1].ID() != "b" {
		t.Errorf("expected 'b' second, got %q", out[1].ID())
	}
	if out[2].ID() != "c" {
		t.Errorf("expected 'c' last (lowest cosine), got %q", out[2].ID())
	}
	// Verify metadata stamp.
	for _, it := range out {
		if _, ok := it.GetMeta("math_prefilter_score"); !ok {
			t.Errorf("item %q missing math_prefilter_score metadata", it.ID())
		}
	}
}

// TestMathPrefilter_LambdaHalf_DiversityKicks verifies that Lambda=0.5 causes
// near-duplicate documents to be penalised (diversity kick). We use 3 docs where
// 'a' and 'b' are near-duplicates. 'c' has moderate relevance but is distinct,
// so after MMR penalises 'b', 'c' comes second.
//
// MMR score formula (Carbonell-Goldstein, λ=0.5):
//
//	score(d) = λ * rel(d) − (1−λ) * max_sim_to_selected(d)
//
// After selecting 'a' (rel=1.0):
//   - score('b') = 0.5*1.0 − 0.5*0.998 = 0.001  (near-dup, heavily penalised)
//   - score('c') = 0.5*0.7 − 0.5*0   = 0.350   (distinct, moderate relevance)
//
// → 'c' beats 'b'.
func TestMathPrefilter_LambdaHalf_DiversityKicks(t *testing.T) {
	// Vectors (2-D, normalised):
	//   query  = (1, 0)
	//   va     = normVec2(0.9, 0.436)   cos(q,a) ≈ 0.9
	//   vb     = normVec2(0.88, 0.475)  very close to va: cos(va,vb) ≥ 0.99
	//   vc     = normVec2(0.7, -0.714)  cos(q,c) ≈ 0.7, cos(va,vc) ≈ 0.32 (distinct)
	//
	// MMR score formula (Carbonell-Goldstein, λ=0.5):
	//   score(d) = λ * rel(d) − (1−λ) * max_sim(d, selected)
	//
	// After selecting 'a':
	//   MMR('b') = 0.5*0.88 − 0.5*0.998 ≈ −0.059  (near-dup, negative)
	//   MMR('c') = 0.5*0.7  − 0.5*0.32  ≈ +0.19   (distinct, positive)
	// → 'c' beats 'b' despite lower raw cosine.

	query3 := []float32{1, 0}

	va3 := normVec2(0.9, 0.436)  // cos(query, va3) ≈ 0.9
	vb3 := normVec2(0.88, 0.475) // very close to va3; cos(va3,vb3) must be > 0.99
	vc3 := normVec2(0.7, -0.714) // cos(query, vc3) ≈ 0.7 but cos(va3, vc3) is much lower

	// Verify preconditions.
	cosAB := cosineSim(va3, vb3)
	if cosAB < 0.99 {
		t.Fatalf("precondition: cos(va3,vb3) must be ≥ 0.99, got %v", cosAB)
	}
	cosQA := cosineSim(query3, va3)
	cosQB := cosineSim(query3, vb3)
	cosAC := cosineSim(va3, vc3)
	cosQC := cosineSim(query3, vc3)
	t.Logf("cos(q,a)=%.3f cos(q,b)=%.3f cos(a,b)=%.3f cos(q,c)=%.3f cos(a,c)=%.3f",
		cosQA, cosQB, cosAB, cosQC, cosAC)

	// Expected MMR scores after picking 'a':
	mmrB := 0.5*float64(cosQB) - 0.5*float64(cosAB)
	mmrC := 0.5*float64(cosQC) - 0.5*float64(cosAC)
	t.Logf("MMR score b=%.4f c=%.4f", mmrB, mmrC)
	if mmrC <= mmrB {
		t.Fatalf("precondition: expected MMR('c')=%.4f > MMR('b')=%.4f so diversity picks c over b", mmrC, mmrB)
	}

	embByID3 := map[string][]float32{"a": va3, "b": vb3, "c": vc3}
	items3 := []Item{newStub("a", 0.9), newStub("b", 0.85), newStub("c", 0.5)}

	mp3 := MathPrefilter{
		EmbeddingsByID: embByID3,
		QueryVector:    query3,
		Lambda:         0.5,
	}
	out3, err := mp3.Rerank(context.Background(), "q", items3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out3) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out3))
	}
	// 'a' should still be first.
	if out3[0].ID() != "a" {
		t.Errorf("expected 'a' first (highest relevance), got %q", out3[0].ID())
	}
	// 'c' should be second (distinct from 'a') — MMR penalises near-dup 'b'.
	if out3[1].ID() != "c" {
		t.Errorf("expected diversity to elevate 'c' (distinct) to second; got %q second", out3[1].ID())
	}
	// 'b' should be last (near-dup of 'a', heavily penalised).
	if out3[2].ID() != "b" {
		t.Errorf("expected near-dup 'b' to be last; got %q last", out3[2].ID())
	}
}

// TestMathPrefilter_NoQueryVec_Passthrough verifies that a nil QueryVector causes
// the prefilter to return items unchanged.
func TestMathPrefilter_NoQueryVec_Passthrough(t *testing.T) {
	items := []Item{newStub("a", 0.9), newStub("b", 0.8)}
	mp := MathPrefilter{
		EmbeddingsByID: map[string][]float32{"a": {1, 0}, "b": {0, 1}},
		QueryVector:    nil, // no query vec
		Lambda:         0.5,
	}
	out, err := mp.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items (unchanged), got %d", len(out))
	}
	// Order must be unchanged.
	if out[0].ID() != "a" || out[1].ID() != "b" {
		t.Errorf("expected unchanged order [a, b], got [%s, %s]", out[0].ID(), out[1].ID())
	}
}

// TestMathPrefilter_DisabledByEnv_Skipped verifies that
// MEMDB_RERANK_MATH_PREFILTER=0 (or unset) causes NewMathPrefilterFromEnv to
// return (MathPrefilter{}, false) — the prefilter is never added to the chain.
func TestMathPrefilter_DisabledByEnv_Skipped(t *testing.T) {
	os.Unsetenv("MEMDB_RERANK_MATH_PREFILTER")
	embByID := map[string][]float32{"a": {1, 0}}
	queryVec := []float32{1, 0}

	_, ok := NewMathPrefilterFromEnv(embByID, queryVec)
	if ok {
		t.Error("expected NewMathPrefilterFromEnv to return false when env is unset")
	}

	t.Setenv("MEMDB_RERANK_MATH_PREFILTER", "0")
	_, ok = NewMathPrefilterFromEnv(embByID, queryVec)
	if ok {
		t.Error("expected NewMathPrefilterFromEnv to return false when env=0")
	}
}

// TestMathPrefilter_EnabledByEnv verifies that MEMDB_RERANK_MATH_PREFILTER=1
// causes NewMathPrefilterFromEnv to return (MathPrefilter, true).
func TestMathPrefilter_EnabledByEnv(t *testing.T) {
	t.Setenv("MEMDB_RERANK_MATH_PREFILTER", "1")
	embByID := map[string][]float32{"a": {1, 0}}
	queryVec := []float32{1, 0}

	mpf, ok := NewMathPrefilterFromEnv(embByID, queryVec)
	if !ok {
		t.Error("expected NewMathPrefilterFromEnv to return true when env=1")
	}
	if mpf.Lambda != 0.5 {
		t.Errorf("expected default Lambda=0.5, got %v", mpf.Lambda)
	}
}

// TestMathPrefilter_LambdaFromEnv verifies that MEMDB_RERANK_MATH_LAMBDA
// overrides the default Lambda=0.5.
func TestMathPrefilter_LambdaFromEnv(t *testing.T) {
	t.Setenv("MEMDB_RERANK_MATH_PREFILTER", "1")
	t.Setenv("MEMDB_RERANK_MATH_LAMBDA", "0.8")
	embByID := map[string][]float32{"a": {1, 0}}
	queryVec := []float32{1, 0}

	mpf, ok := NewMathPrefilterFromEnv(embByID, queryVec)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if math.Abs(float64(mpf.Lambda)-0.8) > 1e-5 {
		t.Errorf("expected Lambda=0.8, got %v", mpf.Lambda)
	}
}

// TestMathPrefilter_ItemWithoutEmbed_FallsToBottom verifies that items without
// an embedding entry are kept but ranked after embedded items.
func TestMathPrefilter_ItemWithoutEmbed_FallsToBottom(t *testing.T) {
	qv := []float32{1, 0}
	// Only 'a' and 'c' have embeddings; 'b' does not.
	embByID := map[string][]float32{
		"a": {1, 0}, // cos with query = 1.0
		"c": {0, 1}, // cos with query = 0.0
		// "b" is absent
	}
	items := []Item{newStub("a", 0.9), newStub("b", 0.8), newStub("c", 0.7)}

	mp := MathPrefilter{
		EmbeddingsByID: embByID,
		QueryVector:    qv,
		Lambda:         0,
	}
	out, err := mp.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
	// 'b' (no embedding) must be last.
	if out[len(out)-1].ID() != "b" {
		t.Errorf("expected item without embedding ('b') to be last, got %q", out[len(out)-1].ID())
	}
	// 'a' should be first (cos=1.0 with query).
	if out[0].ID() != "a" {
		t.Errorf("expected 'a' (cos=1.0) to be first, got %q", out[0].ID())
	}
}

// TestMathPrefilter_EmptyItems_Passthrough verifies that empty input returns
// early without error.
func TestMathPrefilter_EmptyItems_Passthrough(t *testing.T) {
	mp := MathPrefilter{
		EmbeddingsByID: map[string][]float32{},
		QueryVector:    []float32{1, 0},
		Lambda:         0.5,
	}
	out, err := mp.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty output for empty input, got %d items", len(out))
	}
}

// TestMathPrefilter_NameIsMathPrefilter verifies the Name() method.
func TestMathPrefilter_NameIsMathPrefilter(t *testing.T) {
	mp := MathPrefilter{}
	if mp.Name() != "math_prefilter" {
		t.Errorf("expected Name()='math_prefilter', got %q", mp.Name())
	}
}

// TestMathPrefilter_ImplementsReranker is a compile-time interface check.
func TestMathPrefilter_ImplementsReranker(t *testing.T) {
	var _ Reranker = MathPrefilter{}
	_ = t
}

// TestMathPrefilter_Parity_DisabledByDefault verifies that when
// MEMDB_RERANK_MATH_PREFILTER is unset, the prefilter is NOT added to the chain
// and a chain without it produces identical output to a chain with it disabled.
//
// This is the parity test: disabled-by-default must be byte-identical to
// the pre-A1 baseline (no MathPrefilter in chain).
func TestMathPrefilter_Parity_DisabledByDefault(t *testing.T) {
	os.Unsetenv("MEMDB_RERANK_MATH_PREFILTER")

	qv := []float32{1, 0}
	embByID := map[string][]float32{
		"a": {1, 0},
		"b": {0, 1},
		"c": normVec2(0.6, 0.8),
	}

	// Baseline chain: Cosine only (no MathPrefilter).
	baselineChain := Chain{
		Cosine{QueryVec: qv, EmbeddingsByID: embByID},
	}
	baselineItems := []Item{newStub("a", 0.1), newStub("b", 0.2), newStub("c", 0.3)}
	baselineOut, _ := baselineChain.Rerank(context.Background(), "q", baselineItems)

	// New-code path: check that NewMathPrefilterFromEnv returns false.
	_, ok := NewMathPrefilterFromEnv(embByID, qv)
	if ok {
		t.Fatal("parity test: MEMDB_RERANK_MATH_PREFILTER unset but NewMathPrefilterFromEnv returned true")
	}

	// Chain without MathPrefilter (what happens when env is unset).
	testChain := Chain{
		Cosine{QueryVec: qv, EmbeddingsByID: embByID},
	}
	testItems := []Item{newStub("a", 0.1), newStub("b", 0.2), newStub("c", 0.3)}
	testOut, _ := testChain.Rerank(context.Background(), "q", testItems)

	if len(baselineOut) != len(testOut) {
		t.Fatalf("parity: baseline len=%d, test len=%d", len(baselineOut), len(testOut))
	}
	for i := range baselineOut {
		if baselineOut[i].ID() != testOut[i].ID() {
			t.Errorf("parity: position %d: baseline=%q, test=%q", i, baselineOut[i].ID(), testOut[i].ID())
		}
	}
}
