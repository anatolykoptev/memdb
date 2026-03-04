package search

import (
	"sort"

	"github.com/anatolykoptev/go-engine/sources"
)

const rrfK = 60 // standard Reciprocal Rank Fusion constant

// FuseWRR merges multiple result sets using Weighted Reciprocal Rank.
// Each result's fused score = sum of weight[i] / (k + rank) across sources.
// Results are grouped by URL — duplicates accumulate score.
// Returns results sorted by fused score descending.
func FuseWRR(resultSets [][]sources.Result, weights []float64) []sources.Result {
	if len(resultSets) == 0 {
		return nil
	}

	type entry struct {
		result sources.Result
		score  float64
	}

	byURL := make(map[string]*entry)
	var order []string // preserve first-seen order for stable sort tiebreaker

	for i, set := range resultSets {
		w := 1.0
		if i < len(weights) {
			w = weights[i]
		}
		for rank, r := range set {
			if r.URL == "" {
				continue
			}
			rrf := w / float64(rrfK+rank)
			if e, ok := byURL[r.URL]; ok {
				e.score += rrf
			} else {
				byURL[r.URL] = &entry{result: r, score: rrf}
				order = append(order, r.URL)
			}
		}
	}

	merged := make([]sources.Result, 0, len(byURL))
	for _, u := range order {
		e := byURL[u]
		e.result.Score = e.score
		merged = append(merged, e.result)
	}

	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})

	return merged
}
