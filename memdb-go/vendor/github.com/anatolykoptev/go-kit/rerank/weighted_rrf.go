package rerank

import (
	"fmt"
	"sort"
)

// WeightedRRF fuses N ranked lists using Reciprocal Rank Fusion with per-list
// weights:
//
//	score(d) = Σ w_i / (k + rank_i(d))
//
// where rank_i is the 1-based rank of d in the i-th list (omitted from the sum
// when d is absent). Use this variant when retrievers have known reliability
// differences (e.g. a high-precision dense retriever weighted higher than a
// recall-oriented BM25). TREC iKAT 2025 reported +5% nDCG@10 vs plain RRF when
// per-list weights were grid-searched on a held-out dev set.
//
// k controls smoothing identically to plain RRF (Cormack-Clarke 2009): smaller
// k weights the top of each list more strongly, larger k flattens the curve.
// k <= 0 falls back to DefaultRRFK.
//
// Weights must have the same length as lists. weight=0 makes a list contribute
// nothing (effectively skipped). Weights must be ≥ 0; to suppress a retriever,
// omit it instead of using a negative weight.
//
// All weights == 1.0 is mathematically equivalent to plain RRF(k, lists...);
// callers can use that property to migrate gradually.
//
// Tie-breaking: same as RRF (stable, first-seen order across lists).
//
// Panics if len(weights) != len(lists), or if any weight is negative. These
// are programmer errors, not runtime errors: weights and lists are nearly
// always specified together at config-parse time, and silent fallback would
// mask bugs.
func WeightedRRF(k int, weights []float64, lists ...[]string) []Fused {
	for i, w := range weights {
		if w < 0 {
			panic(fmt.Sprintf("rerank.WeightedRRF: weights[%d]=%g, weights must be ≥ 0; remove the retriever rather than negating it", i, w))
		}
	}
	if len(weights) != len(lists) {
		panic(fmt.Sprintf("rerank.WeightedRRF: len(weights)=%d != len(lists)=%d", len(weights), len(lists)))
	}
	if k <= 0 {
		k = DefaultRRFK
	}

	scores := make(map[string]float64)
	order := make([]string, 0)

	for li, list := range lists {
		w := weights[li]
		if w == 0 {
			// Zero-weight list contributes nothing; skip entirely.
			continue
		}
		seen := make(map[string]struct{}, len(list))
		for i, id := range list {
			if _, dup := seen[id]; dup {
				// Only the best (first) rank in this list contributes.
				continue
			}
			seen[id] = struct{}{}
			if _, ok := scores[id]; !ok {
				order = append(order, id)
			}
			scores[id] += w / float64(k+i+1)
		}
	}

	recordWeightedRRFListsFused(len(lists))

	out := make([]Fused, len(order))
	for i, id := range order {
		out[i] = Fused{ID: id, Score: scores[id]}
	}
	// Stable sort preserves first-seen order on score ties.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}
