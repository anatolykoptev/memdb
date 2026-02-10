package embedder

import (
	"math"
	"testing"
)

// Compile-time interface check for VoyageClient (pure Go, no CGO needed).
var _ Embedder = (*VoyageClient)(nil)

func TestMeanPool_SingleBatch(t *testing.T) {
	// batch=1, seqLen=3, dim=2
	// hidden: [[1,2], [3,4], [5,6]]
	// mask: [1, 1, 0]  (third token masked)
	// Expected: mean of [1,2] and [3,4] = [2,3], then L2-normalized
	hidden := []float32{1, 2, 3, 4, 5, 6}
	mask := []int64{1, 1, 0}
	result := meanPool(hidden, mask, 1, 3, 2)

	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 2 {
		t.Fatalf("expected dim 2, got %d", len(result[0]))
	}

	// After mean: [2, 3]. L2 norm = sqrt(4+9) = sqrt(13)
	norm := math.Sqrt(4.0 + 9.0)
	want0 := float32(2.0 / norm)
	want1 := float32(3.0 / norm)

	if math.Abs(float64(result[0][0]-want0)) > 1e-5 {
		t.Errorf("result[0][0] = %f, want %f", result[0][0], want0)
	}
	if math.Abs(float64(result[0][1]-want1)) > 1e-5 {
		t.Errorf("result[0][1] = %f, want %f", result[0][1], want1)
	}
}

func TestMeanPool_MultiBatch(t *testing.T) {
	// batch=2, seqLen=2, dim=2
	// Batch 0: hidden [[1,0], [0,1]], mask [1,1] -> mean [0.5, 0.5], normalized [1/sqrt(2), 1/sqrt(2)]
	// Batch 1: hidden [[1,0], [1,0]], mask [1,0] -> mean [1, 0], normalized [1, 0]
	hidden := []float32{1, 0, 0, 1, 1, 0, 1, 0}
	mask := []int64{1, 1, 1, 0}
	result := meanPool(hidden, mask, 2, 2, 2)

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}

	// Batch 0: [0.5, 0.5] -> [1/sqrt(2), 1/sqrt(2)] ~ [0.7071, 0.7071]
	invSqrt2 := float32(1.0 / math.Sqrt(2))
	if math.Abs(float64(result[0][0]-invSqrt2)) > 1e-5 {
		t.Errorf("batch0[0] = %f, want %f", result[0][0], invSqrt2)
	}
	if math.Abs(float64(result[0][1]-invSqrt2)) > 1e-5 {
		t.Errorf("batch0[1] = %f, want %f", result[0][1], invSqrt2)
	}

	// Batch 1: [1, 0] -> [1, 0]
	if math.Abs(float64(result[1][0]-1.0)) > 1e-5 {
		t.Errorf("batch1[0] = %f, want 1.0", result[1][0])
	}
	if math.Abs(float64(result[1][1])) > 1e-5 {
		t.Errorf("batch1[1] = %f, want 0.0", result[1][1])
	}
}

func TestMeanPool_AllMasked(t *testing.T) {
	// All tokens masked -> zero vector (no normalization possible)
	hidden := []float32{1, 2, 3, 4}
	mask := []int64{0, 0}
	result := meanPool(hidden, mask, 1, 2, 2)

	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	for i, v := range result[0] {
		if v != 0 {
			t.Errorf("result[0][%d] = %f, want 0", i, v)
		}
	}
}

func TestMeanPool_SingleToken(t *testing.T) {
	// batch=1, seqLen=1, dim=3
	// hidden: [3, 4, 0]
	// mask: [1]
	// mean = [3, 4, 0], norm = 5, normalized = [0.6, 0.8, 0]
	hidden := []float32{3, 4, 0}
	mask := []int64{1}
	result := meanPool(hidden, mask, 1, 1, 3)

	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != 3 {
		t.Fatalf("expected dim 3, got %d", len(result[0]))
	}

	if math.Abs(float64(result[0][0]-0.6)) > 1e-5 {
		t.Errorf("result[0][0] = %f, want 0.6", result[0][0])
	}
	if math.Abs(float64(result[0][1]-0.8)) > 1e-5 {
		t.Errorf("result[0][1] = %f, want 0.8", result[0][1])
	}
	if math.Abs(float64(result[0][2])) > 1e-5 {
		t.Errorf("result[0][2] = %f, want 0.0", result[0][2])
	}
}

func TestMeanPool_UnitNorm(t *testing.T) {
	// Verify that the output vectors have unit L2 norm.
	hidden := []float32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	mask := []int64{1, 1, 1, 1, 1, 1}
	// batch=2, seqLen=3, dim=2
	result := meanPool(hidden, mask, 2, 3, 2)

	for b, vec := range result {
		var sumSq float64
		for _, v := range vec {
			sumSq += float64(v) * float64(v)
		}
		norm := math.Sqrt(sumSq)
		if math.Abs(norm-1.0) > 1e-5 {
			t.Errorf("batch %d: L2 norm = %f, want 1.0", b, norm)
		}
	}
}

func TestL2Normalize(t *testing.T) {
	vec := []float32{3, 4}
	l2Normalize(vec)
	// norm = 5, so [0.6, 0.8]
	if math.Abs(float64(vec[0]-0.6)) > 1e-5 {
		t.Errorf("vec[0] = %f, want 0.6", vec[0])
	}
	if math.Abs(float64(vec[1]-0.8)) > 1e-5 {
		t.Errorf("vec[1] = %f, want 0.8", vec[1])
	}
}

func TestL2Normalize_ZeroVector(t *testing.T) {
	vec := []float32{0, 0, 0}
	l2Normalize(vec) // should not panic
	for i, v := range vec {
		if v != 0 {
			t.Errorf("vec[%d] = %f, want 0", i, v)
		}
	}
}

func TestL2Normalize_AlreadyUnit(t *testing.T) {
	// Input is already unit norm: [1, 0, 0]
	vec := []float32{1, 0, 0}
	l2Normalize(vec)
	if math.Abs(float64(vec[0]-1.0)) > 1e-5 {
		t.Errorf("vec[0] = %f, want 1.0", vec[0])
	}
	if vec[1] != 0 || vec[2] != 0 {
		t.Errorf("vec = %v, want [1, 0, 0]", vec)
	}
}

func TestL2Normalize_NegativeValues(t *testing.T) {
	vec := []float32{-3, 4}
	l2Normalize(vec)
	// norm = 5, so [-0.6, 0.8]
	if math.Abs(float64(vec[0]-(-0.6))) > 1e-5 {
		t.Errorf("vec[0] = %f, want -0.6", vec[0])
	}
	if math.Abs(float64(vec[1]-0.8)) > 1e-5 {
		t.Errorf("vec[1] = %f, want 0.8", vec[1])
	}
}

func TestL2Normalize_HighDim(t *testing.T) {
	// 1024-dimensional vector (matching e5 output dimension)
	vec := make([]float32, 1024)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}
	l2Normalize(vec)

	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if math.Abs(norm-1.0) > 1e-4 {
		t.Errorf("L2 norm = %f, want 1.0", norm)
	}
}
