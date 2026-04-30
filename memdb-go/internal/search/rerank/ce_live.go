package rerank

// ce_live.go — HTTP transport half of the CrossEncoder strategy.
//
// runLive scores every item in one batched call. runLiveSubset scores the
// uncached portion of a partial precompute hit. Both:
//
//   - build []rerank.Doc from item.EmbeddingText() + ce.EmbeddingsByID
//   - fire OnLiveCall (legacy memdb.search.ce_live_call_total)
//   - delegate scoring to rerankWithMathFallback so degraded / low-quality
//     responses route through MathReranker (cosine on Doc.EmbedVector)
//
// The orchestration (precompute lookup vs live, partial vs full) lives in
// ce.go::rerank. Math fallback details live in ce_math_fallback.go.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/go-kit/rerank"
)

// runLive performs the live HTTP rerank call for the full batch. Mirrors
// rerankMemoryItems from the legacy adapter.
func (ce CrossEncoder) runLive(ctx context.Context, query string, items []Item) []Item {
	if len(items) == 0 {
		return items
	}
	docs, idxByID := buildLiveDocs(items, ce.EmbeddingsByID)
	if len(docs) == 0 {
		return items
	}

	if ce.OnLiveCall != nil {
		ce.OnLiveCall(ctx)
	}
	scored := ce.rerankWithMathFallback(ctx, query, docs)

	seen := make(map[int]bool, len(scored))
	out := make([]Item, 0, len(items))
	for _, s := range scored {
		origIdx, ok := idxByID[s.ID]
		if !ok {
			continue
		}
		items[origIdx].SetScore(float64(s.Score))
		items[origIdx].SetMeta("cross_encoder_reranked", true)
		out = append(out, items[origIdx])
		seen[origIdx] = true
	}
	for i, it := range items {
		if !seen[i] {
			out = append(out, it)
		}
	}
	return out
}

// runLiveSubset calls the live HTTP rerank backend for a subset of items
// (the uncached portion in a partial-hit). Returns items in the order the
// CE backend scored them, with SetScore applied. Items without text are
// returned with score 0 and no mutation — mirrors runLive behaviour for
// no-text items. Fires OnLiveCall exactly once.
func (ce CrossEncoder) runLiveSubset(ctx context.Context, query string, items []Item) []Item {
	if len(items) == 0 {
		return items
	}
	docs, idxByID := buildLiveDocs(items, ce.EmbeddingsByID)

	out := make([]Item, len(items))
	copy(out, items)

	if len(docs) == 0 {
		return out
	}

	if ce.OnLiveCall != nil {
		ce.OnLiveCall(ctx)
	}
	scored := ce.rerankWithMathFallback(ctx, query, docs)

	for _, s := range scored {
		origIdx, ok := idxByID[s.ID]
		if !ok {
			continue
		}
		out[origIdx].SetScore(float64(s.Score))
	}
	return out
}

// buildLiveDocs converts items into the rerank.Doc shape the go-kit client
// expects. Each Doc carries an EmbedVector when EmbeddingsByID has one for
// the item — required by the math fallback path; nil is acceptable
// (fallback skips cosine for that doc and lets it sink). idxByID maps the
// stringified position back to the original items[] index for the score
// merge.
//
// Items with empty EmbeddingText() are skipped — runLive's "untouched
// passthrough at the bottom" loop relies on those being absent from
// idxByID.
func buildLiveDocs(items []Item, embByID map[string][]float32) ([]rerank.Doc, map[string]int) {
	docs := make([]rerank.Doc, 0, len(items))
	idxByID := make(map[string]int, len(items))
	for i, it := range items {
		text := it.EmbeddingText()
		if text == "" {
			continue
		}
		id := fmt.Sprintf("%d", i)
		var vec []float32
		if embByID != nil {
			vec = embByID[it.ID()]
		}
		docs = append(docs, rerank.Doc{ID: id, Text: text, EmbedVector: vec})
		idxByID[id] = i
	}
	return docs, idxByID
}
