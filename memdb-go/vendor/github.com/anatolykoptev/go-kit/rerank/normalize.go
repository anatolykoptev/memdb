package rerank

import "math"

// NormalizeMode selects a client-side score normalization strategy applied
// after server scoring and before SourceWeights.
//
// NOTE: Sigmoid is intentionally NOT a NormalizeMode here. Use
// WithServerNormalize("sigmoid") to push sigmoid to embed-server (one impl,
// all callers benefit). Client-side Sigmoid is reserved for cases where the
// server lacks the option — not implemented to avoid diverging from server.
type NormalizeMode uint8

const (
	// NormalizeNone is the default — scores pass through unchanged (v1 compat).
	NormalizeNone NormalizeMode = iota
	// NormalizeMinMax maps scores to [0,1] via (x-min)/(max-min).
	// All-equal input → all 0.5 (avoids division by zero).
	NormalizeMinMax
	// NormalizeZScore standardises scores to zero mean and unit variance.
	// Useful for relative ranking only — output is not bounded.
	// Stddev=0 (all-equal) → all 0.
	NormalizeZScore
)

// String returns the human-readable mode name for logging and metrics.
func (m NormalizeMode) String() string {
	switch m {
	case NormalizeNone:
		return "none"
	case NormalizeMinMax:
		return "minmax"
	case NormalizeZScore:
		return "zscore"
	default:
		return "unknown"
	}
}

// Normalize transforms scores in-place per mode and returns the same slice.
// Sort order is preserved for both MinMax and ZScore (monotonic transforms).
//
// MinMax edge cases:
//   - len(scores)==0 → no-op
//   - all equal → all 0.5 (avoid div-by-zero)
//
// ZScore edge cases:
//   - len(scores)==0 → no-op
//   - stddev==0 → all 0
func Normalize(scores []float32, mode NormalizeMode) []float32 {
	if mode == NormalizeNone || len(scores) == 0 {
		return scores
	}
	switch mode {
	case NormalizeMinMax:
		return normalizeMinMax(scores)
	case NormalizeZScore:
		return normalizeZScore(scores)
	}
	return scores
}

func normalizeMinMax(scores []float32) []float32 {
	min, max := scores[0], scores[0]
	for _, v := range scores[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	rng := max - min
	if rng == 0 {
		for i := range scores {
			scores[i] = 0.5
		}
		return scores
	}
	for i, v := range scores {
		scores[i] = (v - min) / rng
	}
	return scores
}

func normalizeZScore(scores []float32) []float32 {
	n := float64(len(scores))
	var sum float64
	for _, v := range scores {
		sum += float64(v)
	}
	mean := sum / n

	var variance float64
	for _, v := range scores {
		d := float64(v) - mean
		variance += d * d
	}
	variance /= n
	stddev := math.Sqrt(variance)

	if stddev == 0 {
		for i := range scores {
			scores[i] = 0
		}
		return scores
	}
	for i, v := range scores {
		scores[i] = float32((float64(v) - mean) / stddev)
	}
	return scores
}
