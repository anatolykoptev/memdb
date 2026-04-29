package rerank

import "sort"

// DefaultRRFK is the standard Reciprocal Rank Fusion constant from
// Cormack-Clarke (2009): "Reciprocal Rank Fusion outperforms Condorcet and
// individual Rank Learning Methods" (SIGIR '09). LangChain4j and most modern
// RAG stacks use the same default. K smooths large rank differences so that
// the gap between rank 1 and rank 2 does not dominate the gap between rank 50
// and rank 51.
const DefaultRRFK = 60

// Fused is a single (id, score) pair produced by RRF. Score is the summed
// reciprocal rank across all input lists where id appeared.
type Fused struct {
	ID    string
	Score float64
}

// RRF fuses N ranked lists of string IDs using Reciprocal Rank Fusion
// (Cormack-Clarke 2009). The score for each id d is:
//
//	score(d) = Σ 1 / (k + rank_i(d))
//
// where rank_i is the 1-based rank of d in the i-th input list (omitted from
// the sum when d is absent from list i). The result is sorted desc by score;
// ties keep the order in which ids were first seen across the input lists
// (stable). Unlike score-based fusion, RRF is immune to differing score
// scales (BM25 vs cosine vs cross-encoder) — it operates on ranks only.
//
// k controls smoothing: smaller k weights the top of each list more strongly,
// larger k flattens the contribution curve. k <= 0 falls back to DefaultRRFK.
//
// Edge cases:
//   - Zero lists or all-empty lists → empty result, no panic.
//   - Single list → pass-through in original order with RRF scores assigned.
//   - Duplicate ids inside one list → only the first occurrence (best rank)
//     contributes from that list. Later occurrences are ignored.
//
// RRF emits one increment per call to the rerank_rrf_lists_fused_total
// counter so operators can observe fusion frequency in production.
func RRF(k int, lists ...[]string) []Fused {
	if k <= 0 {
		k = DefaultRRFK
	}

	scores := make(map[string]float64)
	order := make([]string, 0)

	for _, list := range lists {
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
			scores[id] += 1.0 / float64(k+i+1)
		}
	}

	recordRRFListsFused(len(lists))

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
