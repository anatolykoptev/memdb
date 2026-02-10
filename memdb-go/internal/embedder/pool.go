package embedder

import "math"

// meanPool computes mean pooling over the hidden states using the attention
// mask, then L2-normalizes each resulting vector.
//
// hidden is a flat [batchSize * seqLen * dim] float32 slice.
// mask is a flat [batchSize * seqLen] int64 slice.
func meanPool(hidden []float32, mask []int64, batchSize, seqLen, dim int) [][]float32 {
	result := make([][]float32, batchSize)

	for b := 0; b < batchSize; b++ {
		vec := make([]float32, dim)
		var maskSum float64

		batchTokenOffset := b * seqLen
		batchHiddenOffset := b * seqLen * dim

		for s := 0; s < seqLen; s++ {
			if mask[batchTokenOffset+s] == 0 {
				continue
			}
			maskSum++
			hiddenStart := batchHiddenOffset + s*dim
			for d := 0; d < dim; d++ {
				vec[d] += hidden[hiddenStart+d]
			}
		}

		if maskSum > 0 {
			invMaskSum := float32(1.0 / maskSum)
			for d := 0; d < dim; d++ {
				vec[d] *= invMaskSum
			}
		}

		l2Normalize(vec)
		result[b] = vec
	}

	return result
}

// l2Normalize normalizes the vector in-place to unit L2 norm.
func l2Normalize(vec []float32) {
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if norm > 0 {
		invNorm := float32(1.0 / norm)
		for i := range vec {
			vec[i] *= invNorm
		}
	}
}
