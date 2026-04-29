package rerank

import "fmt"

// linearMinMaxFlatScore is the per-list contribution applied when a list has
// max == min (all identical scores, or a single item). When max==min in a
// list (all scores equal), the normalized contribution from that list is 0.
// This matches Elasticsearch's linear_retriever precedent. Returning 0.5 was
// incorrect — it injects a synthetic mid-range score that competes with real
// signals from other lists. Returning 0 means "this list contains no usable
// ranking signal for these docs".
const linearMinMaxFlatScore = 0.0

// LinearMinMax fuses N score-bearing lists by MinMax-normalizing each list's
// scores into [0, 1] and summing per ID with caller-supplied weights:
//
//	score(d) = Σ w_i * (raw_i(d) - min_i) / (max_i - min_i)
//
// This is the Elasticsearch Linear Retriever convention. Use with calibrated
// weights (e.g. from grid search on a held-out dev set) when you want
// transparent, easily-debugged fused scores in the [0, Σw] range.
//
// Edge cases:
//   - max == min in a list → all entries normalize to 0 (no usable ranking
//     signal from that list for these docs). Matches Elasticsearch's
//     linear_retriever precedent.
//   - Empty list → contributes nothing.
//   - Single-item list → falls into the max==min branch above (contributes 0).
//   - weight=0 → list skipped entirely (no normalization performed; saves work).
//   - Duplicate IDs inside one list: only the FIRST occurrence in that list
//     contributes (mirrors RRF/DBSF "best first" rule).
//
// Tie-breaking: stable, first-seen order across lists.
//
// Weights must be ≥ 0; to suppress a retriever, omit it instead of using a
// negative weight.
//
// Panics if len(weights) != len(lists), or if any weight is negative. These
// are programmer errors: weights and lists are nearly always specified
// together at config-parse time.
func LinearMinMax(weights []float64, lists ...ScoredIDList) []Fused {
	for i, w := range weights {
		if w < 0 {
			panic(fmt.Sprintf("rerank.LinearMinMax: weights[%d]=%g, weights must be ≥ 0; remove the retriever rather than negating it", i, w))
		}
	}
	if len(weights) != len(lists) {
		panic(fmt.Sprintf("rerank.LinearMinMax: len(weights)=%d != len(lists)=%d", len(weights), len(lists)))
	}

	scores := make(map[string]float64)
	order := make([]string, 0)

	for li, list := range lists {
		linearMinMaxAccumulateList(weights[li], list, scores, &order)
	}

	recordLinearMinMaxListsFused(len(lists))
	return sortFused(scores, order)
}

// linearMinMaxAccumulateList MinMax-normalizes one list and accumulates the
// weighted contribution into scores. weight=0 or empty list are no-ops.
func linearMinMaxAccumulateList(w float64, list ScoredIDList, scores map[string]float64, order *[]string) {
	if w == 0 || len(list) == 0 {
		return
	}
	dedup := dedupByFirstID(list)
	minS, maxS := minMaxScore(dedup)
	span := maxS - minS
	for _, item := range dedup {
		if _, ok := scores[item.ID]; !ok {
			*order = append(*order, item.ID)
		}
		norm := linearMinMaxFlatScore
		if span != 0 {
			norm = (item.Score - minS) / span
		}
		scores[item.ID] += w * norm
	}
}

// minMaxScore returns the min and max Score across list. Caller guarantees
// len(list) > 0.
func minMaxScore(list ScoredIDList) (minS, maxS float64) {
	minS, maxS = list[0].Score, list[0].Score
	for _, item := range list[1:] {
		if item.Score < minS {
			minS = item.Score
		}
		if item.Score > maxS {
			maxS = item.Score
		}
	}
	return minS, maxS
}
