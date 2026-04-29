// Package rerank — MathPrefilter: Stage-0 pre-CE diversity prefilter (M14 A1).
//
// Wraps go-kit/rerank.MathReranker to satisfy the local Reranker interface.
// Runs before CrossEncoder in the suffix chain when MEMDB_RERANK_MATH_PREFILTER=1.
// Default OFF — disabled env flag produces byte-identical output to pre-A1 baseline.
//
// Items without an embedding in EmbeddingsByID fall through to the bottom of
// the output slice (same graceful-degrade pattern as mmr.go:120).
package rerank

import (
	"context"
	"os"
	"strconv"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
)

// MathPrefilter is a Stage-0 diversity-aware prefilter. It wraps
// gokitrerank.MathReranker to reorder items by the Carbonell-Goldstein
// λ-balanced score before they reach CrossEncoder, giving CE a more
// diverse top-K input and improving cat-1 (multi-fact aggregation) quality.
//
// EmbeddingsByID supplies the per-item embedding vectors; QueryVector is the
// query embedding. Both are built upstream by postProcessResults and passed in
// via NewMathPrefilterFromEnv.
type MathPrefilter struct {
	EmbeddingsByID map[string][]float32
	QueryVector    []float32
	Lambda         float32
}

// Name implements Reranker.
func (MathPrefilter) Name() string { return "math_prefilter" }

// Rerank implements Reranker. Builds gokitrerank.Doc slice from local Items,
// calls MathReranker.RerankWithResult, reorders local Items to match the
// scored output order. Items without an embedding fall through to the bottom.
func (m MathPrefilter) Rerank(ctx context.Context, _ string, items []Item) ([]Item, error) {
	if len(m.QueryVector) == 0 {
		recordMathPrefilterSkipped(ctx, "no_query_vec")
		RecordSkipped(ctx, m.Name())
		return items, nil
	}
	if len(items) == 0 {
		recordMathPrefilterSkipped(ctx, "no_items")
		RecordSkipped(ctx, m.Name())
		return items, nil
	}

	// Separate items with embeddings from those without.
	docs := make([]gokitrerank.Doc, 0, len(items))
	// noEmbed holds items that have no embedding — they go to the bottom.
	var noEmbed []Item

	for _, it := range items {
		emb := m.EmbeddingsByID[it.ID()]
		if emb == nil {
			noEmbed = append(noEmbed, it)
			continue
		}
		docs = append(docs, gokitrerank.Doc{
			ID:          it.ID(),
			Text:        it.EmbeddingText(),
			EmbedVector: emb,
		})
	}

	if len(docs) == 0 {
		recordMathPrefilterSkipped(ctx, "empty_embeddings")
		RecordSkipped(ctx, m.Name())
		return items, nil
	}

	mr := gokitrerank.MathReranker{
		QueryVector: m.QueryVector,
		Lambda:      m.Lambda,
	}
	res, err := mr.RerankWithResult(ctx, "", docs)
	if err != nil || res == nil || res.Status != gokitrerank.StatusOk {
		RecordSkipped(ctx, m.Name())
		return items, nil
	}

	// Build a fast lookup by ID.
	byID := make(map[string]Item, len(items))
	for _, it := range items {
		byID[it.ID()] = it
	}

	out := make([]Item, 0, len(items))
	for _, s := range res.Scored {
		it, ok := byID[s.ID]
		if !ok {
			continue
		}
		it.SetMeta("math_prefilter_score", float64(s.Score))
		out = append(out, it)
		delete(byID, s.ID)
	}
	// Items without an embedding (skipped when building docs) fall to the bottom.
	// They were never in docs, so they're still present in byID only if they
	// had an embedding assigned to them. The noEmbed slice is the canonical list.
	out = append(out, noEmbed...)

	recordMathPrefilterEngaged(ctx)
	RecordSuccess(ctx, m.Name())
	return out, nil
}

// NewMathPrefilterFromEnv reads MEMDB_RERANK_MATH_PREFILTER (gate, "1" = enabled)
// and MEMDB_RERANK_MATH_LAMBDA (float32, default 0.5) from the environment and
// returns a configured MathPrefilter.
//
// Returns (MathPrefilter{}, false) when the gate is unset or "0".
func NewMathPrefilterFromEnv(embByID map[string][]float32, queryVec []float32) (MathPrefilter, bool) {
	if os.Getenv("MEMDB_RERANK_MATH_PREFILTER") != "1" {
		return MathPrefilter{}, false
	}
	lambda := envFloatMathPrefilter("MEMDB_RERANK_MATH_LAMBDA", 0.5)
	return MathPrefilter{
		EmbeddingsByID: embByID,
		QueryVector:    queryVec,
		Lambda:         float32(lambda),
	}, true
}

// envFloatMathPrefilter reads an env variable and parses it as float64.
// Returns def when the variable is unset or unparseable.
func envFloatMathPrefilter(key string, def float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}
