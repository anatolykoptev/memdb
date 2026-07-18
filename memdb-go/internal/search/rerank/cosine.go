// Package rerank — cosine strategy.
//
// Ports ReRankByCosine from the parent search pkg into a typed Reranker.
// Behaviour parity is bit-for-bit: same normalisation `(cosine + 1) / 2`,
// same metadata.relativity write, same stable sort by descending score,
// same fast-path for empty query / empty items / missing embeddings.
//
// Embeddings live outside the Item (they were never embedded in the map
// shape — the search pipeline carries `embeddingsByID` alongside).
// The strategy is constructed per-search with the query vector + the lookup
// map; it is NOT safe for concurrent reuse.
package rerank

import (
	"context"
	"math"
	"sort"
)

// Cosine reranks items by cosine similarity between QueryVec and each
// item's stored embedding. Items without an embedding entry in
// EmbeddingsByID keep their existing score (matches legacy behaviour —
// see internal/search/rerank.go::ReRankByCosine).
type Cosine struct {
	QueryVec       []float32
	EmbeddingsByID map[string][]float32
}

// Name implements Reranker.
func (Cosine) Name() string { return "cosine" }

// Rerank implements Reranker. Mutates each item's score in place and
// returns the slice in stable-sort order. Empty inputs return early
// (matching legacy fast-path; no metric outcome — chain treats nil as
// skipped, but we explicitly emit success for the empty case so dashboards
// still see the strategy fire).
func (c Cosine) Rerank(ctx context.Context, _ string, items []Item) ([]Item, error) {
	if len(c.QueryVec) == 0 || len(items) == 0 {
		RecordSkipped(ctx, c.Name())
		return items, nil
	}

	for _, it := range items {
		emb := c.EmbeddingsByID[it.ID()]
		if len(emb) == 0 {
			continue
		}
		// Normalize to [0,1] range matching PolarDB's score: (raw_cosine + 1) / 2
		sim := (float64(cosineSim(c.QueryVec, emb)) + 1.0) / 2.0
		it.SetScore(sim)
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Score() > items[j].Score()
	})

	RecordSuccess(ctx, c.Name())
	return items, nil
}

// cosineSim is the local equivalent of search.CosineSimilarity. Inlined
// here to keep the rerank package self-contained (boundary rule: rerank
// MUST NOT import internal/search). Returns 0 on length mismatch / zero
// norms — matches the parent package's helper exactly.
func cosineSim(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
