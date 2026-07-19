package embed

import "math"

// Cosine returns the cosine similarity between two float32 vectors.
// Range: [-1, 1] for non-zero inputs.
// Returns 0 for zero-length inputs, mismatched lengths, or zero-norm vectors
// (defensive: no panic, caller treats as "no similarity").
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / float32(math.Sqrt(float64(na))*math.Sqrt(float64(nb)))
}
