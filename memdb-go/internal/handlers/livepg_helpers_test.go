//go:build livepg

// livepg_helpers_test.go — shared helpers for livepg-tagged handler tests.
//
// stubEmbedder (add_test.go) returns 1024-dim zero vectors. With zero
// embeddings, pgvector cosine similarity returns NaN, and isDuplicate
// treats NaN >= threshold as a match — silently dropping every insert
// after the first. livepgEmbedder returns deterministic unit-norm
// vectors so each distinct text gets a unique direction and dedup
// only fires for true duplicates.

package handlers

import (
	"context"
)

const livepgEmbedDim = 1024

// livepgEmbedder is a deterministic embedder for livepg tests — each
// text gets a stable unit-norm vector derived from its content, so
// distinct texts produce distinct directions and cosine similarity is
// well-defined (no NaN).
type livepgEmbedder struct{}

func (e *livepgEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = deterministicUnitVecHandlers(t, livepgEmbedDim)
	}
	return out, nil
}

func (e *livepgEmbedder) EmbedQuery(_ context.Context, t string) ([]float32, error) {
	return deterministicUnitVecHandlers(t, livepgEmbedDim), nil
}

func (e *livepgEmbedder) Dimension() int { return livepgEmbedDim }
func (e *livepgEmbedder) Close() error   { return nil }

// deterministicUnitVecHandlers returns a unit-norm vector whose direction
// is a stable function of the input string. Uses djb2-like hashing to seed
// a trivial PRNG, avoiding any external dependency.
func deterministicUnitVecHandlers(seed string, dim int) []float32 {
	v := make([]float32, dim)
	h := uint64(5381)
	for i := 0; i < len(seed); i++ {
		h = h*33 ^ uint64(seed[i])
	}
	if h == 0 {
		h = 1
	}
	for i := 0; i < dim; i++ {
		h = h*1103515245 + 12345
		v[i] = float32(int64(h>>16)%2001-1000) / 1000.0
	}
	// normalise to unit length
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		v[0] = 1
		return v
	}
	norm := float32(1.0)
	// sqrtf via float64 to avoid import
	norm = float32(1.0 / sqrtFloat64(sum))
	for i := range v {
		v[i] *= norm
	}
	return v
}

func sqrtFloat64(x float64) float64 {
	// Newton's method — avoids importing math in test helpers
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = z - (z*z-x)/(2*z)
	}
	return z
}
