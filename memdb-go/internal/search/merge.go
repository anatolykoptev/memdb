package search

// merge.go — result merging: MergedResult type, RRF fusion, graph and working-memory merges.
// Covers: MergedResult, MergeVectorAndFulltext (RRF), MergeGraphIntoResults,
//         MergeWorkingMemIntoResults.

import (
	"sort"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

// MergedResult combines a VectorSearchResult with a merged score.
type MergedResult struct {
	ID         string
	Properties string
	Score      float64
	Embedding  []float32
}

// rrfK is the constant in the Reciprocal Rank Fusion formula.
// Standard value is 60 (Robertson et al., 2009); provides smooth rank-based interpolation.
const rrfK = 60.0

// MergeVectorAndFulltext merges vector and fulltext results using Reciprocal Rank Fusion (RRF).
//
// RRF score = Σ 1 / (rank + k) over all ranked lists that contain the item.
// This is rank-position-based and immune to score scale differences between
// fulltext (normalized tf-idf) and vector (cosine) systems.
//
// Reference: "Reciprocal Rank Fusion outperforms Condorcet and individual
// Rank Learning Methods" - Cormack, Clarke & Buettcher, SIGIR 2009.
func MergeVectorAndFulltext(vector, fulltext []db.VectorSearchResult) []MergedResult {
	type entry struct {
		result MergedResult
		rrf    float64
	}
	byID := make(map[string]*entry, len(vector)+len(fulltext))
	order := make([]string, 0, len(vector)+len(fulltext))

	// Vector list: rank 1-based from position in already-sorted slice
	for rank, r := range vector {
		rrfScore := 1.0 / (float64(rank+1) + rrfK)
		// Preserve embedding for later cosine rerank
		e, ok := byID[r.ID]
		if !ok {
			e = &entry{result: MergedResult{ID: r.ID, Properties: r.Properties, Embedding: r.Embedding}}
			byID[r.ID] = e
			order = append(order, r.ID)
		}
		e.rrf += rrfScore
		if len(r.Embedding) > 0 && len(e.result.Embedding) == 0 {
			e.result.Embedding = r.Embedding
		}
	}

	// Fulltext list: rank 1-based from position in already-sorted slice
	for rank, r := range fulltext {
		rrfScore := 1.0 / (float64(rank+1) + rrfK)
		e, ok := byID[r.ID]
		if !ok {
			e = &entry{result: MergedResult{ID: r.ID, Properties: r.Properties, Embedding: r.Embedding}}
			byID[r.ID] = e
			order = append(order, r.ID)
		}
		e.rrf += rrfScore
		if len(r.Embedding) > 0 && len(e.result.Embedding) == 0 {
			e.result.Embedding = r.Embedding
		}
	}

	// Materialize results with rrf as score (will be replaced by cosine in rerank step)
	results := make([]MergedResult, 0, len(byID))
	for _, id := range order {
		e := byID[id]
		e.result.Score = e.rrf
		results = append(results, e.result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

// MergeGraphIntoResults merges graph recall results into an existing merged slice.
// Graph results get a fixed score. If an ID already exists, the higher score is kept.
func MergeGraphIntoResults(existing []MergedResult, graph []db.GraphRecallResult) []MergedResult {
	byID := make(map[string]int, len(existing))
	for i, r := range existing {
		byID[r.ID] = i
	}

	for _, g := range graph {
		score := GraphKeyScore
		if g.TagOverlap > 0 {
			score = GraphTagBaseScore + GraphTagBonusPerTag*float64(g.TagOverlap)
			if score > GraphKeyScore {
				score = GraphKeyScore
			}
		}
		if idx, ok := byID[g.ID]; ok {
			if score > existing[idx].Score {
				existing[idx].Score = score
			}
		} else {
			existing = append(existing, MergedResult{
				ID:         g.ID,
				Properties: g.Properties,
				Score:      score,
			})
			byID[g.ID] = len(existing) - 1
		}
	}

	// Re-sort by score descending
	sort.SliceStable(existing, func(i, j int) bool {
		return existing[i].Score > existing[j].Score
	})
	return existing
}

// ContradictsPenalty is the score reduction applied to memories that are contradicted
// by a higher-ranked memory in the same result set (via CONTRADICTS edge).
const ContradictsPenalty = 0.30

// PenalizeContradicts lowers the score of any result whose ID appears as the target
// of a CONTRADICTS edge from another result in the same set.
// contradictedIDs is the set of memory IDs that are known to be contradicted
// (obtained via GraphRecallByEdge with EdgeContradicts from the top result IDs).
// Non-destructive: results not in contradictedIDs are unchanged.
func PenalizeContradicts(results []MergedResult, contradictedIDs map[string]bool) []MergedResult {
	if len(contradictedIDs) == 0 {
		return results
	}
	for i := range results {
		if contradictedIDs[results[i].ID] {
			results[i].Score -= ContradictsPenalty
			if results[i].Score < 0 {
				results[i].Score = 0
			}
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

// MergeWorkingMemIntoResults merges WorkingMemory items into an existing merged slice.
// Computes actual cosine similarity against queryVec for proper relevance scoring.
// Items without embeddings get WorkingMemBaseScore as a fallback.
// If an ID already exists, the higher score is kept.
func MergeWorkingMemIntoResults(existing []MergedResult, wm []db.VectorSearchResult, queryVec []float32) []MergedResult {
	byID := make(map[string]int, len(existing))
	for i, r := range existing {
		byID[r.ID] = i
	}

	for _, w := range wm {
		score := WorkingMemBaseScore
		if len(w.Embedding) > 0 && len(queryVec) > 0 {
			rawCosine := CosineSimilarity(queryVec, w.Embedding)
			score = (float64(rawCosine) + 1.0) / 2.0
			if score > WorkingMemMaxScore {
				score = WorkingMemMaxScore
			}
		}
		if idx, ok := byID[w.ID]; ok {
			if score > existing[idx].Score {
				existing[idx].Score = score
			}
			if len(w.Embedding) > 0 && len(existing[idx].Embedding) == 0 {
				existing[idx].Embedding = w.Embedding
			}
		} else {
			existing = append(existing, MergedResult{
				ID:         w.ID,
				Properties: w.Properties,
				Score:      score,
				Embedding:  w.Embedding,
			})
			byID[w.ID] = len(existing) - 1
		}
	}

	sort.SliceStable(existing, func(i, j int) bool {
		return existing[i].Score > existing[j].Score
	})
	return existing
}
