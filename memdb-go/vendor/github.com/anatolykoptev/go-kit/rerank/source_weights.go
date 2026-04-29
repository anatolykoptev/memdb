package rerank

// applySourceWeights multiplies scores[i] by weights[head[i].Source].
// Sources not present in the map default to weight 1.0 (no change).
// A nil weights map is a no-op.
//
// Pure: does NOT sort, does NOT change ordering — only scales values.
// Apply AFTER Normalize and BEFORE sort so boosted/deboosted docs land in the
// correct final rank position.
func applySourceWeights(scores []float32, head []Doc, weights map[string]float32) []float32 {
	if len(weights) == 0 {
		return scores
	}
	for i, d := range head {
		if i >= len(scores) {
			break
		}
		if w, ok := weights[d.Source]; ok {
			scores[i] *= w
		}
		// Missing source → weight stays 1.0 (score unchanged).
	}
	return scores
}

// WithSourceWeights configures a per-source score multiplier applied AFTER
// Normalize and BEFORE the final sort.
//
// Conventions:
//   - weight >= 1.0: boost trusted sources (raw authoritative observations)
//   - weight < 1.0:  deboost lower-quality sources (auto-generated summaries)
//   - weight == 0:   effectively suppresses the source (score becomes 0)
//
// Sources whose name is not in the map keep weight 1.0.
// A nil map disables source weighting.
func WithSourceWeights(weights map[string]float32) Opt {
	return func(c *cfgInternal) { c.sourceWeights = weights }
}
