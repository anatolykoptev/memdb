package rerank

import "math"

// cosineSim returns the cosine similarity between two equal-length vectors.
// Returns 0 if either vector has zero norm or lengths mismatch (defensive,
// no panic; caller treats as "irrelevant"). Range: [-1, 1] for non-zero
// normalized vectors; 0 for degenerate inputs.
func cosineSim(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
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
