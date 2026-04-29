package embed

import "math"

// l2Normalize normalizes the vector in-place to unit L2 norm.
// Used by the Ollama client (WithNormalizeL2 safety net) and by the ONNX
// subpackage's mean pool. Lives at top level so both packages can share it
// without duplicating the math.
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

// L2Normalize is the exported alias of l2Normalize. The ONNX subpackage
// imports this from the parent package so mean pooling stays in one place.
func L2Normalize(vec []float32) { l2Normalize(vec) }
