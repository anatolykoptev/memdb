package handlers

// add_structural_edges_cosine.go — SIMILAR_COSINE_HIGH branch of the M8
// Stream 10 structural-edge emitter, split out so add_structural_edges.go
// stays under the 300-line cap. Both files share package-level constants
// and types with add_structural_edges.go.

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// buildSimilarCosineEdgesForBatch runs one cosine-similarity scan per new
// memory to harvest "topically similar but not duplicate" partners. Skipped
// for any new memory whose embedding is empty (raw mode without embedder, or
// a degraded path). Uses the existing VectorSearch — same code path that
// powers dedup, so the cost is at most one extra query per new memory.
//
// Method on Handler so it can call h.postgres + h.logger; the pure
// transformation (results → edges) is buildSimilarCosineEdgesFromResults
// for unit testability.
func (h *Handler) buildSimilarCosineEdgesForBatch(ctx context.Context, fac fastAddContext, newMems []newMemoryRef) []db.MemoryEdgeRow {
	if h.postgres == nil {
		return nil
	}
	var edges []db.MemoryEdgeRow
	for _, m := range newMems {
		if len(m.Embedding) == 0 || m.ID == "" {
			continue
		}
		// Pull a few extra so we can skip the new memory itself + sub-threshold hits.
		results, err := h.postgres.VectorSearch(ctx, m.Embedding, fac.cubeID, fac.cubeID,
			[]string{"LongTermMemory", "UserMemory"}, fac.agentID, similarCosineMaxPartners+5)
		if err != nil {
			h.logger.Debug("structural edges: cosine scan failed",
				slog.String("id", m.ID), slog.Any("error", err))
			continue
		}
		edges = append(edges, buildSimilarCosineEdgesFromResults(
			m.ID, results, similarCosineMinScore, dedupThreshold, similarCosineMaxPartners,
		)...)
	}
	return edges
}

// buildSimilarCosineEdgesFromResults filters a VectorSearch result set for
// scores in (lo, hi), drops self-matches, and trims to maxPartners. Pure
// function — kept separate from the Handler method so unit tests can feed
// hand-crafted result slices without standing up Postgres.
func buildSimilarCosineEdgesFromResults(newID string, results []db.VectorSearchResult, lo, hi float64, maxPartners int) []db.MemoryEdgeRow {
	if newID == "" || len(results) == 0 || maxPartners <= 0 {
		return nil
	}
	edges := make([]db.MemoryEdgeRow, 0, maxPartners)
	for _, r := range results {
		if len(edges) >= maxPartners {
			break
		}
		if r.ID == "" || r.ID == newID {
			continue
		}
		// Half-open interval: skip near-duplicates that the dedup pass already
		// rejected, and skip uninteresting low-similarity neighbours.
		if r.Score <= lo || r.Score >= hi {
			continue
		}
		edges = append(edges, db.MemoryEdgeRow{
			FromID:     newID,
			ToID:       r.ID,
			Relation:   db.EdgeSimilarCosineHigh,
			Confidence: r.Score,
		})
	}
	return edges
}
