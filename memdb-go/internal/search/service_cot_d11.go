// Package search — service_cot_d11.go: D11 CoT decomposer wiring.
//
// Runs the D11 decomposer (sibling of D7 augmentWithSubqueries, but with a
// different gate, prompt, and cache) BEFORE D2 multi-hop. Each sub-query
// gets its own VectorSearch on the text scope; results are unioned by id
// into psr.textVec so the downstream expandViaGraph step sees the enlarged
// seed set and walks edges from each sub-query's hits independently.
//
// Why a sibling of augmentWithSubqueries instead of replacing it:
//   - D7 (cot_decompose.go) is generic conjunction splitting; D11 targets
//     temporal+multihop questions. They can be enabled independently for
//     ablation and one improving cat-2 doesn't preclude the other improving
//     cat-3.
//   - Both are env-gated, default OFF — zero regression for existing clients.
//
// Failure mode: any decomposition error / single-element result / disabled
// config → no-op. Pipeline runs identically to no-CoT path.
package search

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// applyCoTDecomposition runs the D11 decomposer and (when it expanded the
// query) fans each sub-query[1:] through an extra VectorSearch on the text
// scope, unioning results into psr.textVec by id (max-score). The original
// query at index 0 is the one that already produced psr — we don't re-embed
// it. Returns the sub-queries used (always [original] when no decomposition
// happened) for downstream logging / metrics correlation.
//
// Skill / tool scopes are intentionally NOT augmented here — D11 targets the
// text-scope multi-hop weakness (cat-2 / cat-3); skill/tool augmentation is
// already covered by D7's augmentWithSubqueries when MEMDB_SEARCH_COT=true.
func (s *SearchService) applyCoTDecomposition(
	ctx context.Context,
	psr *parallelSearchResults,
	query string,
	p SearchParams,
	budget searchBudget,
) []string {
	if s.CoTDecomposer == nil {
		return []string{query}
	}
	subs := s.CoTDecomposer.Decompose(ctx, s.logger, query)
	if len(subs) <= 1 {
		return subs
	}
	// Ensure original is at index 0 (the decomposer already guarantees this,
	// but defend the invariant for downstream consumers).
	if subs[0] != query {
		subs = append([]string{query}, subs...)
	}
	for _, sq := range subs[1:] {
		s.fanoutSubqueryToText(ctx, psr, sq, p, budget)
	}
	return subs
}

// fanoutSubqueryToText embeds one sub-query and unions its text-scope vector
// results into psr.textVec. Errors are debug-logged and swallowed: D11 is
// best-effort, never blocks the pipeline.
func (s *SearchService) fanoutSubqueryToText(
	ctx context.Context,
	psr *parallelSearchResults,
	subQuery string,
	p SearchParams,
	budget searchBudget,
) {
	vec, err := s.embedder.EmbedQuery(ctx, subQuery)
	if err != nil {
		s.logger.Debug("d11 subquery embed failed",
			slog.String("sub", subQuery), slog.Any("error", err))
		return
	}
	var extra []db.VectorSearchResult
	switch {
	case len(p.CubeIDs) > 1:
		extra, err = s.postgres.VectorSearchMultiCube(ctx, vec, p.CubeIDs, p.UserName, TextScopes, p.AgentID, budget.textK)
	default:
		extra, err = s.postgres.VectorSearch(ctx, vec, p.CubeID, p.UserName, TextScopes, p.AgentID, budget.textK)
	}
	if err != nil {
		s.logger.Debug("d11 subquery vector search failed",
			slog.String("sub", subQuery), slog.Any("error", err))
		return
	}
	psr.textVec = unionVectorResults(psr.textVec, extra)
}
