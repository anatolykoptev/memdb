// Package search — cosine reranking for search results.
// Port of Python's CosineLocalReranker from cosine_local.py.
package search

import "sort"

// ReRankByCosine re-scores items using cosine similarity between queryVec
// and each item's stored embedding. Items without embeddings keep their
// original score.
//
// This replaces the compressed PolarDB scores (0.88-0.92 range) with direct
// cosine similarity (0.2-0.95 range), giving much better discrimination
// for relativity threshold filtering.
func ReRankByCosine(queryVec []float32, items []map[string]any, embeddingsByID map[string][]float32) []map[string]any {
	if len(queryVec) == 0 || len(items) == 0 {
		return items
	}

	for _, item := range items {
		id, _ := item["id"].(string)
		emb := embeddingsByID[id]
		if len(emb) == 0 {
			continue
		}

		// Normalize to [0,1] range matching PolarDB's score: (raw_cosine + 1) / 2
		cosineSim := (float64(CosineSimilarity(queryVec, emb)) + 1.0) / 2.0
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["relativity"] = cosineSim
		}
	}

	// Sort by new score descending
	sort.SliceStable(items, func(i, j int) bool {
		scoreI := getRelativity(items[i])
		scoreJ := getRelativity(items[j])
		return scoreI > scoreJ
	})

	return items
}

// getRelativity extracts metadata.relativity from a formatted memory item.
func getRelativity(item map[string]any) float64 {
	meta, _ := item["metadata"].(map[string]any)
	if meta == nil {
		return 0
	}
	score, _ := meta["relativity"].(float64)
	return score
}
