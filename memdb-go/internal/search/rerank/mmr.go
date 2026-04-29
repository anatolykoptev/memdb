// Package rerank — MMR diversification strategy (Q4).
//
// Ports find_best_unrelated_subgroup from MemOS retrieve_utils.py:450-465.
// Algorithm: greedy diversity filter. Keep the first item unconditionally;
// for each subsequent item (in rank order), keep only if its cosine
// similarity to every already-kept item is strictly below the diversity
// bar. Default bar = 0.8 (verbatim from MemOS).
//
// This runs AFTER CE + LLMJudge and BEFORE Staged — it prunes near-
// duplicates from the top of the reranked list before the final cut so
// the user sees diverse memories even when the corpus has many paraphrases.
//
// Items without an embedding in EmbeddingsByID are kept unconditionally
// (graceful degrade: no embedding ≈ can't measure similarity).
//
// Metrics: mmr_kept_total and mmr_dropped_total (per search call) so
// operators can measure how aggressively the filter fires.
package rerank

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

var (
	mmrKeptCounter    metric.Int64Counter
	mmrDroppedCounter metric.Int64Counter
)

func init() {
	m := otel.Meter("memdb-go/search/rerank")
	mmrKeptCounter, _ = m.Int64Counter("memdb.search.mmr_kept_total",
		metric.WithDescription("Number of items kept by MMR diversification filter per search call"))
	mmrDroppedCounter, _ = m.Int64Counter("memdb.search.mmr_dropped_total",
		metric.WithDescription("Number of items dropped by MMR diversification filter per search call"))
}

// MMR diversification filter: greedy cosine-threshold subgroup selection.
//
// EmbeddingsByID supplies the per-item embedding vectors. Items not present
// in the map are always kept (no embedding → can't measure → safe to include).
//
// Bar is the cosine similarity ceiling; any candidate whose cosine to any
// already-kept item reaches or exceeds Bar is dropped. Default 0.8.
//
// TopK caps the output slice size (0 = no cap — keep all that pass the bar).
type MMR struct {
	EmbeddingsByID map[string][]float32
	Bar            float64 // default 0.8; override via MEMDB_MMR_BAR
	TopK           int     // 0 = no cap; override via MEMDB_MMR_TOPK
}

// NewMMRFromEnv reads MEMDB_MMR_BAR (float, default 0.8) and
// MEMDB_MMR_TOPK (int, default 0 = no cap) and returns a configured MMR.
// EmbeddingsByID must be set by the caller before use.
func NewMMRFromEnv(embByID map[string][]float32) MMR {
	return MMR{
		EmbeddingsByID: embByID,
		Bar:            envcfg.Float("MEMDB_MMR_BAR", 0.8),
		TopK:           envcfg.Int("MEMDB_MMR_TOPK", 0),
	}
}

// Name implements Reranker.
func (MMR) Name() string { return "mmr" }

// Rerank implements Reranker. Greedy pass: keep item i iff cosine(i, j) < Bar
// for every already-kept j. Items without embeddings are kept unconditionally.
// Mutates each item's metadata to stamp mmr_kept or mmr_dropped.
func (m MMR) Rerank(ctx context.Context, _ string, items []Item) ([]Item, error) {
	if len(items) <= 1 {
		RecordSuccess(ctx, m.Name())
		return items, nil
	}

	bar := m.Bar
	if bar <= 0 {
		bar = 0.8
	}

	kept := make([]Item, 0, len(items))
	keptVecs := make([][]float32, 0, len(items))

	var keptCount, droppedCount int64

	for _, it := range items {
		vec := m.embeddingFor(it)
		if vec == nil {
			// No embedding — keep unconditionally (graceful degrade).
			it.SetMeta("mmr_kept", true)
			kept = append(kept, it)
			keptVecs = append(keptVecs, nil)
			keptCount++
		} else {
			if m.tooSimilarToKept(vec, keptVecs, bar) {
				it.SetMeta("mmr_dropped", true)
				droppedCount++
			} else {
				it.SetMeta("mmr_kept", true)
				kept = append(kept, it)
				keptVecs = append(keptVecs, vec)
				keptCount++
			}
		}

		if m.TopK > 0 && len(kept) >= m.TopK {
			break
		}
	}

	if mmrKeptCounter != nil {
		mmrKeptCounter.Add(ctx, keptCount)
	}
	if mmrDroppedCounter != nil {
		mmrDroppedCounter.Add(ctx, droppedCount)
	}

	RecordSuccess(ctx, m.Name())
	return kept, nil
}

// embeddingFor returns the embedding vector for an item from EmbeddingsByID.
// Returns nil when the map is empty or the ID is not present.
func (m MMR) embeddingFor(it Item) []float32 {
	if len(m.EmbeddingsByID) == 0 {
		return nil
	}
	return m.EmbeddingsByID[it.ID()]
}

// tooSimilarToKept returns true if vec has cosine similarity ≥ bar to any
// vector in keptVecs (nil entries in keptVecs are skipped — they represent
// items kept without an embedding).
func (m MMR) tooSimilarToKept(vec []float32, keptVecs [][]float32, bar float64) bool {
	for _, kv := range keptVecs {
		if kv == nil {
			continue
		}
		// Note: MemOS uses strict > (cos == bar keeps); we use >= (cos == bar drops).
		// Functionally equivalent at float64 precision; chosen to match Go idiom for "above threshold".
		if float64(cosineSim(vec, kv)) >= bar {
			return true
		}
	}
	return false
}
