package rerank

import (
	"math"
	"sort"
)

// dbsfClipSigma is the symmetric per-list clip applied to z-scored values
// before summation, in standard-deviation units. ±3σ is the Qdrant 1.11
// convention: outliers from any single list cannot dominate the fused score.
const dbsfClipSigma = 3.0

// DBSF fuses N score-bearing lists using Distribution-Based Score Fusion
// (Qdrant 1.11+). For each list it computes mean μ and standard deviation σ
// of the scores, normalizes each entry to z = (s - μ) / σ, clips z to
// [-3σ, +3σ], then sums normalized scores per ID across all lists:
//
//	score(d) = Σ clip(±3, (raw_i(d) - μ_i) / σ_i)
//
// Implements Qdrant's DBSF, which is z-score with ±3σ clip; differs from
// textbook unclipped z-score fusion (Bruch et al. 2023). The clip prevents
// a single outlier in one list from dominating the fused score.
//
// Use when score magnitudes carry meaning (e.g. BM25 confidence, calibrated
// cross-encoder logits).
//
// Recommendation: ≥10 items per list for stable σ. With 2 items, all
// z-scores degrade to ±1 and DBSF becomes worse than RRF. Prefer RRF when
// lists are short.
//
// Edge cases:
//   - Empty list → contributes nothing.
//   - Single-item list → σ undefined; that list contributes 0 for its sole
//     entry (we cannot z-score a single point). The ID is still introduced
//     into the output set so cross-list overlaps can accumulate.
//   - All identical scores in a list (σ = 0) → contributes 0 from that list,
//     same reasoning. Never produces NaN.
//   - Duplicate IDs inside one list: only the FIRST occurrence in that list
//     contributes (mirrors RRF's "best rank only" rule, applied to score).
//
// Tie-breaking: stable, first-seen order across lists.
//
// DBSF is immune to differing score scales by construction — z-scoring puts
// every list on a unit-variance axis before summation. A list with raw
// scores in [50, 100] and a list with raw scores in [0.5, 0.9] contribute
// equally to the fused score (verified by test).
func DBSF(lists ...ScoredIDList) []Fused {
	scores := make(map[string]float64)
	order := make([]string, 0)

	for _, list := range lists {
		dbsfAccumulateList(list, scores, &order)
	}

	recordDBSFListsFused(len(lists))
	return sortFused(scores, order)
}

// dbsfAccumulateList z-scores one list's items (with ±dbsfClipSigma clip)
// and accumulates them into the running scores map. The order slice is
// extended whenever a never-before-seen ID is encountered.
func dbsfAccumulateList(list ScoredIDList, scores map[string]float64, order *[]string) {
	if len(list) == 0 {
		return
	}
	dedup := dedupByFirstID(list)
	mean, sigma := meanStd(dedup)
	for _, item := range dedup {
		if _, ok := scores[item.ID]; !ok {
			*order = append(*order, item.ID)
		}
		if sigma == 0 {
			// Single-item list or all-identical: contribute 0. ID still
			// registered so other lists can accumulate against it.
			continue
		}
		scores[item.ID] += clip((item.Score-mean)/sigma, dbsfClipSigma)
	}
}

// clip returns x clamped to the symmetric interval [-bound, +bound].
func clip(x, bound float64) float64 {
	if x > bound {
		return bound
	}
	if x < -bound {
		return -bound
	}
	return x
}

// dedupByFirstID drops repeated IDs, preserving the first occurrence in the
// input. Mirrors the "best (first) only" rule used by RRF/WeightedRRF.
func dedupByFirstID(list ScoredIDList) ScoredIDList {
	seen := make(map[string]struct{}, len(list))
	out := make(ScoredIDList, 0, len(list))
	for _, item := range list {
		if _, dup := seen[item.ID]; dup {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	return out
}

// sortFused materializes scores+order into a []Fused sorted desc by score
// with stable first-seen tie-breaking. Shared by DBSF and LinearMinMax.
func sortFused(scores map[string]float64, order []string) []Fused {
	out := make([]Fused, len(order))
	for i, id := range order {
		out[i] = Fused{ID: id, Score: scores[id]}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// meanStd returns the arithmetic mean and population standard deviation of
// the Scores in list. For len(list) < 2, sigma == 0 (caller treats as the
// "cannot z-score" path).
//
// Population stddev (divide by N) is correct here — the per-query result
// list IS the sample, not a draw from a population. Sample stddev (N-1) is
// for inferential statistics about an unobserved population.
func meanStd(list ScoredIDList) (mean, sigma float64) {
	if len(list) == 0 {
		return 0, 0
	}
	var sum float64
	for _, item := range list {
		sum += item.Score
	}
	mean = sum / float64(len(list))
	if len(list) < 2 {
		return mean, 0
	}
	var sq float64
	for _, item := range list {
		d := item.Score - mean
		sq += d * d
	}
	// Population stddev (divide by N), matching the Qdrant DBSF reference.
	sigma = math.Sqrt(sq / float64(len(list)))
	return mean, sigma
}
