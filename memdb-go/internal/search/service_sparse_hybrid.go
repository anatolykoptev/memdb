// Package search — service_sparse_hybrid.go: Step D hybrid retrieval (SPLADE
// sparse leg + dense leg fused via RRF).
//
// Wiring:
//
//  1. spawnTextSearches schedules a sparse-leg goroutine alongside the dense
//     vector + fulltext searches when SearchService.SparseEmbedder != nil
//     and MEMDB_HYBRID_SPARSE != "0" (default ON when embedder wired).
//  2. After the parallel errgroup returns, runParallelSearches calls
//     fuseSparseIntoTextVec which RRF-fuses dense + sparse into a single
//     ranked list and writes it back to psr.textVec.
//  3. Downstream merge (vec ⊕ ft) runs unchanged — the existing two-input
//     RRF/fusion sees the already-fused dense+sparse list as its "vec" leg.
//
// Why fuse sparse into textVec instead of threading a third input through
// merge_rrf.go: gokit/rerank.RRF is variadic so 3-input fusion is trivial,
// but every fusion strategy (merge_fusion.go, MergeVectorAndFulltext custom)
// would need a parallel 3-input variant. Pre-fusing keeps the sparse pipeline
// surgical and lets us A/B the sparse leg via env without touching merge.
package search

import (
	"context"
	"log/slog"
	"os"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
	"golang.org/x/sync/errgroup"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
)

// hybridSparseEnvVar gates the sparse retrieval leg. Default ON when
// SparseEmbedder is wired; set to "0" to force-disable for A/B comparison.
const hybridSparseEnvVar = "MEMDB_HYBRID_SPARSE"

// hybridSparseEnabled reports whether the sparse retrieval leg should run.
// Disabled when the embedder is nil OR the env explicitly opts out.
func (s *SearchService) hybridSparseEnabled() bool {
	if s.SparseEmbedder == nil {
		return false
	}
	return os.Getenv(hybridSparseEnvVar) != "0"
}

// spawnSparseTextSearch schedules the SPLADE leg of hybrid retrieval against
// the text scope. Mirrors spawnTextSearches' filter shape (CubeID/CubeIDs,
// AgentID, TextScopes). cutoff variant is intentionally NOT supported in v1
// — temporal-cutoff retrieval is rare; sparse without cutoff still
// participates via the fuse step below.
//
// Best-effort: errors log and leave psr.textSparse empty so the dense leg
// carries the result. Never propagates an error to errgroup.
func (s *SearchService) spawnSparseTextSearch(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	query string, p SearchParams, budget searchBudget,
) {
	if !s.hybridSparseEnabled() {
		return
	}
	g.Go(func() error {
		sv, err := s.SparseEmbedder.EmbedSparseQuery(ctx, query)
		if err != nil {
			s.logger.Debug("sparse query embed failed",
				slog.Any("error", err), slog.String("query", truncForLog(query, 80)))
			return nil
		}
		if sv.IsEmpty() {
			// All tokens were OOV / stopwords — sparse leg yields no
			// candidates. Dense leg already covers this case.
			return nil
		}
		dim := s.SparseEmbedder.VocabSize()
		if dim == 0 {
			dim = 30522 // SPLADE-v3-distilbert vocab; matches migration 0030
		}
		litVec := embedder.FormatSparseVector(sv, dim)

		var (
			results []db.VectorSearchResult
			sErr    error
		)
		if len(p.CubeIDs) > 1 {
			// MultiCube sparse search not exposed yet — fall back to single-cube.
			// Acceptable: sparse is complementary; the multi-cube dense leg
			// already covers cross-domain. Future PR can mirror VectorSearchMultiCube.
			results, sErr = s.postgres.SparseVectorSearch(ctx, litVec, p.CubeID, p.UserName, TextScopes, p.AgentID, budget.textK)
		} else {
			results, sErr = s.postgres.SparseVectorSearch(ctx, litVec, p.CubeID, p.UserName, TextScopes, p.AgentID, budget.textK)
		}
		if sErr != nil {
			s.logger.Debug("sparse vector search failed", slog.Any("error", sErr))
			return nil
		}
		psr.textSparse = results
		return nil
	})
}

// fuseSparseIntoTextVec fuses the dense (psr.textVec) and sparse
// (psr.textSparse) rankings via RRF and writes the result back to
// psr.textVec, leaving psr.textSparse empty for the next call.
//
// Score semantics shift from cosine similarity to RRF rank-sum, but
// downstream cross-encoder rerank (step 6.05) overwrites scores anyway.
// What matters here is candidate ordering entering Stage-2 — and RRF's
// rank-only fusion is robust to the dense/sparse score-scale mismatch.
//
// No-op when sparse is empty (e.g. embed failed, or all-OOV query).
func fuseSparseIntoTextVec(psr *parallelSearchResults) {
	if len(psr.textSparse) == 0 {
		return
	}
	if len(psr.textVec) == 0 {
		// Dense leg returned nothing — promote sparse as-is.
		psr.textVec = psr.textSparse
		psr.textSparse = nil
		return
	}
	k := rrfKValue() // honours MEMDB_RRF_K override
	denseIDs := extractIDs(psr.textVec)
	sparseIDs := extractIDs(psr.textSparse)
	fused := gokitrerank.RRF(k, denseIDs, sparseIDs)

	// Build a props/embedding lookup across both legs so the fused output
	// retains the dense embedding when an ID came only from sparse.
	props := buildPropsIndex(psr.textVec, psr.textSparse)
	out := make([]db.VectorSearchResult, 0, len(fused))
	for _, f := range fused {
		r, ok := props[f.ID]
		if !ok {
			continue
		}
		r.Score = f.Score
		out = append(out, r)
	}
	psr.textVec = out
	psr.textSparse = nil
}

// truncForLog clips a string for safe inclusion in slog values without
// flooding the journal on long-document queries.
func truncForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
