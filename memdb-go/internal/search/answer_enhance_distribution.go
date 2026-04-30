package search

// answer_enhance_distribution.go — D10 soft-routing: pass the full
// classifier distribution to the LLM rather than hard-switching on top-1.
//
// Why soft over hard:
//   The PR #252 hint block (categoryHintBlock) gates on top-1 confidence ≥
//   threshold and otherwise emits nothing — a binary switch. That throws
//   away the rest of the distribution: a 60/35 split between two close
//   categories collapses to one, even though both are plausible.
//
//   Soft routing keeps every category's probability and lets the LLM weigh
//   the shape rules itself. Calibration-aware prompting: the model sees the
//   classifier's honest uncertainty and picks an answer shape that fits
//   the dominant category, but is willing to fall back to the runner-up
//   when the memories pull that direction.
//
// Hybrid contract:
//   When the classifier reports top-1 ≥ MEMDB_D10_HARD_ROUTING_THRESHOLD
//   (default 0.97), we hard-switch to a category-specific full system
//   prompt (see answer_enhance_hard_prompts.go). At that confidence the
//   distribution is essentially one-hot — preserving it costs ~80 tokens
//   per request for zero information gain. A hard prompt is shorter AND
//   more focused, so the win is double-sided.
//
//   Below the hard threshold the distribution is informative; we keep the
//   base extractor prompt and append a distribution block.

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// classifyAndDistribute returns a length-5 distribution over every category,
// normalised so the confidences sum to 1 (softmax over the cosine
// similarities). The returned slice is sorted desc by confidence so callers
// can take the head as top-1.
//
// On no-signal / error / empty embedding the returned slice has a single
// open_domain entry with confidence 0 (matches ClassifyTopN's noSignal
// shape — callers can branch on `len(dist) == 1 && dist[0].Confidence == 0`).
//
// Softmax is applied with an inverse-temperature read from
// MEMDB_D10_SOFTMAX_TEMPERATURE (default 10) so that cosine differences in
// the natural [0, 0.8] range produce a sharp but not collapsed distribution.
// At T=10 the modal-category mass is 0.6-0.95 for confident questions and
// 0.25-0.45 for ambiguous ones — meaningful hard-routing gate without
// saturating to one-hot. Operators can sweep the knob during calibration.
func (c *lazyEmbedClassifier) classifyAndDistribute(ctx context.Context, query string) ([]CategoryConfidence, error) {
	// Pull the full top-N (== all categories), then softmax across them.
	all, err := c.ClassifyTopN(ctx, query, len(classifierCategoryOrder))
	if err != nil {
		return all, err
	}
	if len(all) <= 1 {
		// no-signal sentinel: return as-is so the caller falls through to
		// the base prompt path.
		return all, nil
	}
	temperature := d10SoftmaxTemperature()
	maxScore := all[0].Confidence
	var sum float64
	exps := make([]float64, len(all))
	for i, cc := range all {
		// Subtract max for numerical stability — standard softmax trick.
		exps[i] = math.Exp((cc.Confidence - maxScore) * temperature)
		sum += exps[i]
	}
	if sum == 0 {
		return all, nil
	}
	out := make([]CategoryConfidence, len(all))
	for i, cc := range all {
		out[i] = CategoryConfidence{
			Category:   cc.Category,
			Confidence: exps[i] / sum,
		}
	}
	return out, nil
}

// distributionBlock formats the top-N classifier distribution as a one-line
// guidance hint appended to the base D10 extractor prompt.
//
// Format (compact — under 50 tokens):
//
//	Likely question type: temporal 62%, single_hop 28%. Pick the fitting
//	answer shape; ignore if mixed.
//
// Returns "" when dist is empty / no-signal sentinel — caller then keeps
// the base prompt unchanged.
func distributionBlock(dist []CategoryConfidence, topN int) string {
	if len(dist) == 0 {
		return ""
	}
	if len(dist) == 1 && dist[0].Confidence == 0 {
		return ""
	}
	if topN < 1 || topN > len(dist) {
		topN = len(dist)
	}
	// Trim long-tail entries that contribute < 10% — they're noise to the
	// LLM and bloat the prompt.
	parts := make([]string, 0, topN)
	for i := 0; i < topN; i++ {
		p := percentRound(dist[i].Confidence)
		if p < 10 && len(parts) >= 2 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d%%", dist[i].Category, p))
	}
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nLikely question type: ")
	b.WriteString(strings.Join(parts, ", "))
	b.WriteString(". Pick the fitting answer shape; ignore if mixed.")
	return b.String()
}

// percentRound converts a probability in [0, 1] to an integer percentage,
// rounding half-up. Saturates at 100. Negative inputs (cannot happen given
// the softmax above, but defensive) clamp to 0.
func percentRound(p float64) int {
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return 100
	}
	return int(math.Round(p * 100))
}
