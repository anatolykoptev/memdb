package onnx

import "github.com/anatolykoptev/go-kit/embed"

// meanPool computes mean pooling over the hidden states using the attention
// mask, then L2-normalizes each resulting vector via embed.L2Normalize.
//
// hidden is a flat [batchSize * seqLen * dim] float32 slice.
// mask is a flat [batchSize * seqLen] int64 slice.
//
// Lives in this subpackage rather than the parent because mean pooling is
// only required by the ONNX backend; the HTTP / Ollama / Voyage backends
// receive ready-pooled vectors from their respective servers.
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

		embed.L2Normalize(vec)
		result[b] = vec
	}

	return result
}
