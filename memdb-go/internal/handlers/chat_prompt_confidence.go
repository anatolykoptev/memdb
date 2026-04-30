package handlers

// chat_prompt_confidence.go — multi-feature retrieval confidence scoring.
//
// Replaces the single-cosine gate (`top1 >= threshold`) with a weighted
// combination that captures distribution shape, not just the leader. Five
// real-world failure modes the top-1-only gate gets wrong:
//
//   * "0.50, 0.49, 0.48, 0.47, 0.46" — five corroborating memories, top-1
//     below threshold → routed Low, even though density and median scream
//     High confidence.
//   * "0.51, 0.10, 0.05" — single weak leader → routed High, even though
//     spread and density imply the leader is unreliable.
//
// The combined score adds three terms to top1:
//   - spread  = clamp((top1 - top2) / 0.2, 0, 1)
//               "uncontested leader" signal. Saturates at a 0.2-cosine gap,
//               which empirically separates "clear winner" from "tie".
//   - density = sigmoid(count_above_min - 2)
//               "many strong corroborators" signal. >=3 memories above the
//               density floor saturate this term toward 1.
//   - median  = median(relativities)
//               overall pool quality. Catches "everything is mediocre".
//
// Each weight is env-tunable; defaults sum to 1, keeping the output in [0,1]
// for plausible inputs (top1 ∈ [0,1], spread ∈ [0,1], density ∈ [0,1],
// median ∈ [0,1] — convex combination ⇒ [0,1]).
//
// The threshold env (MEMDB_FACTUAL_CONFIDENCE_THRESHOLD) is unchanged; only
// the value compared to it changes. Operators can A/B between the legacy
// top1-only path and the multi-feature path via MEMDB_FACTUAL_CONFIDENCE_FORMULA.

import (
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// confidenceFormula picks how decideFactualPrompt computes the comparison
// value against the threshold. "top1" preserves M12.2 behaviour (legacy,
// safe-rollback). "multifeature" enables the new weighted formula.
type confidenceFormula string

const (
	confidenceFormulaTop1         confidenceFormula = "top1"
	confidenceFormulaMultifeature confidenceFormula = "multifeature"
)

// defaultConfidenceFormula picks multi-feature scoring out of the box.
// Operators flip back to top1 via MEMDB_FACTUAL_CONFIDENCE_FORMULA=top1
// without redeploy if the new path regresses.
const defaultConfidenceFormula = confidenceFormulaMultifeature

// confidenceWeights holds the four convex-combination coefficients used by
// computeRetrievalConfidence. Defaults sum to 1.0 — chosen so the output
// remains a calibrated probability-like score in [0, 1] for plausible
// inputs. Top1 dominates because it's still the strongest single signal;
// the other terms refine it.
type confidenceWeights struct {
	Top1    float64
	Spread  float64
	Density float64
	Median  float64
}

// defaultConfidenceWeights — top1 dominates (it's the strongest single
// signal), spread is the second-most-informative (uncontested leader),
// density catches "many corroborators", median catches overall pool quality.
// Sum = 1.0 by design.
var defaultConfidenceWeights = confidenceWeights{
	Top1:    0.55,
	Spread:  0.20,
	Density: 0.15,
	Median:  0.10,
}

// defaultDensityFloor — relativity below 0.30 contributes ~nothing in the
// rerank stage, so we use the same cutoff for "useful hit" counting in the
// density term. Tunable via MEMDB_FACTUAL_DENSITY_FLOOR ∈ [0, 1].
const defaultDensityFloor = 0.30

// spreadNormaliser controls how fast the spread term saturates. 0.2 means
// a (top1 - top2) gap of 0.2 cosine fully saturates the term to 1.0. Picked
// from M11 RCA: gold-doc winners typically lead the runner-up by 0.15–0.25;
// a smaller normaliser (0.1) over-weights tiny leads, a larger one (0.3)
// under-rewards genuine separations. NOT env-tunable on purpose — adding a
// fifth knob bloats the surface; tune via the spread weight if needed.
const spreadNormaliser = 0.2

// confidenceFormulaFromEnv reads MEMDB_FACTUAL_CONFIDENCE_FORMULA. Unknown
// or empty value → defaultConfidenceFormula. Recognises only "top1" and
// "multifeature" (case-insensitive); any other string falls back silently
// so a typo does not silently disable the multi-feature path.
func confidenceFormulaFromEnv() confidenceFormula {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MEMDB_FACTUAL_CONFIDENCE_FORMULA"))) {
	case string(confidenceFormulaTop1):
		return confidenceFormulaTop1
	case string(confidenceFormulaMultifeature):
		return confidenceFormulaMultifeature
	default:
		return defaultConfidenceFormula
	}
}

// densityFloorFromEnv reads MEMDB_FACTUAL_DENSITY_FLOOR. Out of [0, 1] /
// unparseable / NaN / Inf → defaultDensityFloor. Mirrors the validation
// shape of factualConfidenceThreshold to keep operator surprise minimal.
func densityFloorFromEnv() float64 {
	raw := os.Getenv("MEMDB_FACTUAL_DENSITY_FLOOR")
	if raw == "" {
		return defaultDensityFloor
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return defaultDensityFloor
	}
	return v
}

// confidenceWeightsFromEnv reads MEMDB_FACTUAL_CONFIDENCE_WEIGHTS, a
// comma-separated `key=value` list (e.g.
// `top1=0.55,spread=0.20,density=0.15,median=0.10`). Unknown keys are
// ignored. Missing keys keep their default. Any parse error in any segment
// → fall back to defaults wholesale (no partial overrides — keeps the
// invariant "all weights came from one validated source").
//
// Negative weights are rejected (would invert the signal). Weights are not
// renormalised — operators can deliberately raise the sum above 1 to make
// the score more aggressive, or lower it for a softer gate. The clamp at
// the end of computeRetrievalConfidence keeps the output in [0, 1].
func confidenceWeightsFromEnv() confidenceWeights {
	raw := os.Getenv("MEMDB_FACTUAL_CONFIDENCE_WEIGHTS")
	if raw == "" {
		return defaultConfidenceWeights
	}
	w := defaultConfidenceWeights
	out := w
	parsed := parseConfidenceWeights(raw, &out)
	if !parsed {
		return defaultConfidenceWeights
	}
	return out
}

// parseConfidenceWeights mutates out with key=value entries from raw. Returns
// false on the first parse error so the caller can fall back to defaults
// wholesale. Exposed at package level for direct testing.
func parseConfidenceWeights(raw string, out *confidenceWeights) bool {
	for _, seg := range strings.Split(raw, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		eq := strings.IndexByte(seg, '=')
		if eq <= 0 || eq == len(seg)-1 {
			return false
		}
		key := strings.ToLower(strings.TrimSpace(seg[:eq]))
		valRaw := strings.TrimSpace(seg[eq+1:])
		v, err := strconv.ParseFloat(valRaw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return false
		}
		switch key {
		case "top1":
			out.Top1 = v
		case "spread":
			out.Spread = v
		case "density":
			out.Density = v
		case "median":
			out.Median = v
		default:
			// Unknown keys ignored (forward-compat with future weights).
		}
	}
	return true
}

// confidenceComponents holds the per-term contributions returned alongside
// the combined score. Populated even when formula=top1 so operators can
// compare A/B without code change (spread/density/median still computed,
// just unused by the gate).
type confidenceComponents struct {
	Top1     float64
	Spread   float64
	Density  float64
	Median   float64
	Combined float64
}

// computeRetrievalConfidence returns the multi-feature confidence score in
// [0, 1] for a memory pool. See package doc-comment for the formula.
//
// Edge cases:
//   - 0 memories → returns 0 (no signal).
//   - 1 memory  → spread = 0 (no runner-up), density evaluated on count=1.
//   - missing relativity → that memory contributes 0; if all are missing the
//     output is 0.
//
// The result is clamped to [0, 1] to defend against operator-tuned weights
// that sum above 1 (the formula stays a probability-like score).
func computeRetrievalConfidence(memories []map[string]any) float64 {
	c := computeRetrievalConfidenceWithComponents(memories)
	return c.Combined
}

// computeRetrievalConfidenceWithComponents returns the score AND the per-term
// contributions, used by the metric path so dashboards can attribute swings
// to a specific term (top1 vs spread vs density vs median).
func computeRetrievalConfidenceWithComponents(memories []map[string]any) confidenceComponents {
	if len(memories) == 0 {
		return confidenceComponents{}
	}

	scores := make([]float64, 0, len(memories))
	for _, m := range memories {
		scores = append(scores, relativity(m))
	}
	// Sort descending so scores[0]=top1, scores[1]=top2 (when present).
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))

	top1 := scores[0]
	// Spread requires a runner-up to compare against. With a single memory
	// there IS no "uncontested leader" signal (no contender exists), so the
	// spread term is forced to 0 rather than spuriously saturating against
	// the implicit top2=0 baseline. This matches the spec contract:
	// "1 memory → spread=0, density rule on count=1".
	spread := 0.0
	if len(scores) >= 2 {
		spread = (top1 - scores[1]) / spreadNormaliser
		if spread < 0 {
			spread = 0
		}
		if spread > 1 {
			spread = 1
		}
	}

	floor := densityFloorFromEnv()
	above := 0
	for _, s := range scores {
		if s >= floor {
			above++
		}
	}
	// sigmoid(count_above_min - 2): saturates toward 1 once we have 3+
	// memories above the floor. count=2 → 0.5, count=1 → ~0.27, count=0
	// → ~0.12. That residual on count=0 is a deliberate floor: even a
	// completely-below-density-floor pool sometimes contains the answer.
	density := sigmoid(float64(above) - 2)

	median := medianFloat64Sorted(scores)

	w := confidenceWeightsFromEnv()
	combined := w.Top1*top1 + w.Spread*spread + w.Density*density + w.Median*median
	if combined < 0 {
		combined = 0
	}
	if combined > 1 {
		combined = 1
	}

	return confidenceComponents{
		Top1:     top1,
		Spread:   spread,
		Density:  density,
		Median:   median,
		Combined: combined,
	}
}

// sigmoid returns 1 / (1 + e^-x). Stable for the small |x| range we care
// about (|x| <= len(memories), typically <= 12). Uses math.Exp directly
// rather than log1p because the input domain is bounded and overflow is not
// a practical concern.
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// medianFloat64Sorted assumes input is sorted descending (the caller's
// canonical layout) and returns the median in O(1). Empty slice → 0.
//
// For odd n the middle element is at index n/2 (descending order does not
// affect the median value). For even n the median is the mean of the two
// middle elements.
func medianFloat64Sorted(sortedDesc []float64) float64 {
	n := len(sortedDesc)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sortedDesc[n/2]
	}
	return (sortedDesc[n/2-1] + sortedDesc[n/2]) / 2
}
