// Package search — pipeline_reflect.go: F2 reflection-loop deep-search stage.
//
// Sits AFTER post_process (rerank + filter + dedup + trim) in defaultStages.
// When enabled (MEMDB_F2_REFLECTION) and the query passes the complexity
// gate, the ReflectionAgent inspects the trimmed candidate set and may
// trigger one of two follow-up actions:
//
//   - "needs_raw"    — re-fetch top-K vector candidates without rerank trim,
//     append previously-unseen IDs.
//   - "missing_info" — embed each missing-aspect phrase, run a fresh
//     VectorSearch, append previously-unseen IDs.
//
// Loop is bounded by reflectionMaxIter (=2) so the worst case is
// "first judge + extra fetch + confirm". Each step emits the
// memdb.reflection.* metrics defined in metrics_reflection.go.
//
// Soft-fail contract: any LLM, embed, or DB error is logged and recorded as
// outcome=error; st.TextFormatted is left untouched. Never fatal.
package search

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// stageReflect is the funcStage entrypoint registered in defaultStages.
// All gating + the bounded loop live here; the LLM call itself is in
// ReflectionAgent.Reflect (reflection_agent.go).
func (s *SearchService) stageReflect(ctx context.Context, st *pipelineState) error {
	if !s.reflectGatesPass(ctx, st) {
		return nil
	}
	mx := reflectionMx()

	for iter := 0; iter < reflectionMaxIter; iter++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		dec, err := s.Reflection.Reflect(ctx, st.Params.Query, st.TextFormatted)
		if err != nil {
			s.logger.Warn("reflection agent error",
				slog.Int("iter", iter),
				slog.Any("error", err),
			)
			mx.Iterations.Add(ctx, 1, metric.WithAttributes(
				attribute.String("outcome", reflectionOutcomeError),
			))
			return nil
		}
		mx.Iterations.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", dec.Decision),
		))

		switch dec.Decision {
		case ReflectionDecisionSufficient:
			return nil
		case ReflectionDecisionNeedsRaw:
			s.applyReflectNeedsRaw(ctx, st)
		case ReflectionDecisionMissingInfo:
			s.applyReflectMissingInfo(ctx, st, dec.MissingAspects)
		default:
			// Unreachable — Decision.IsValid() filtered earlier.
			return nil
		}
	}
	return nil
}

// reflectGatesPass evaluates the four pre-loop gates: env enabled, agent
// wired, complex-only heuristic, non-empty candidate set. Each failure
// records the appropriate outcome label and marks the stage skipped.
// Returns true when the loop body should run.
//
// Fast path (F2 I1 followup): when MEMDB_F2_REFLECTION is disabled the
// gate returns immediately without touching reflectionMx() — saves the
// sync.Once init + counter.Add allocation per query on the hot path
// since reflection defaults to off.
func (s *SearchService) reflectGatesPass(ctx context.Context, st *pipelineState) bool {
	if !reflectionEnabled() {
		st.skip("reflect")
		return false
	}
	mx := reflectionMx()
	switch {
	case s.Reflection == nil:
		st.skip("reflect")
		mx.Iterations.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", reflectionOutcomeDisabled),
		))
		return false
	case reflectionOnComplexOnly() && !isCat2Query(st.Params.Query) && !isCat3Query(st.Params.Query):
		st.skip("reflect")
		mx.Iterations.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", reflectionOutcomeDisabled),
		))
		return false
	case len(st.TextFormatted) == 0:
		st.skip("reflect")
		mx.Iterations.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", reflectionOutcomeSufficient),
		))
		return false
	}
	return true
}

// applyReflectNeedsRaw fetches the top-K vector candidates without the
// rerank trim and appends previously-unseen items to st.TextFormatted.
// Soft-fail: errors are logged and the original slice is preserved.
func (s *SearchService) applyReflectNeedsRaw(ctx context.Context, st *pipelineState) {
	if st.QueryVec == nil || s.postgres == nil {
		return
	}
	rawK := st.Params.TopK * InflateFactor
	if rawK <= 0 {
		return
	}
	results, err := s.postgres.VectorSearch(ctx, st.QueryVec, st.Params.CubeID, st.Params.UserName, TextScopes, st.Params.AgentID, rawK)
	if err != nil {
		s.logger.Debug("reflect needs_raw vector search failed", slog.Any("error", err))
		return
	}
	st.TextFormatted = mergeUnseenFormatted(st.TextFormatted, vectorResultsToMerged(results), st.Params.IncludeEmbedding)
}

// applyReflectMissingInfo embeds each missing-aspect phrase, runs a vector
// search per phrase, and appends previously-unseen items. The phrase list
// is pre-capped at reflectionMaxAspects in the agent.
//
// F2 I5 followup: aspect phrases are batched into a single Embed call
// (the embedder interface ships a native batch method). Falls back to
// per-phrase EmbedQuery on batch failure so a single backend hiccup
// doesn't abort the whole missing-info branch.
//
// Soft-fail per phrase: a DB failure on one aspect does not stop the
// others.
func (s *SearchService) applyReflectMissingInfo(ctx context.Context, st *pipelineState, aspects []string) {
	if len(aspects) == 0 || s.embedder == nil || s.postgres == nil {
		return
	}
	mx := reflectionMx()
	subK := st.Params.TopK
	if subK <= 0 {
		return
	}

	// Try batch embed first — single round-trip for up to reflectionMaxAspects.
	vecs, err := s.embedder.Embed(ctx, aspects)
	if err != nil || len(vecs) != len(aspects) {
		// Fallback to per-phrase EmbedQuery.
		if err != nil {
			s.logger.Debug("reflect missing_info batch embed failed, falling back to per-phrase",
				slog.Any("error", err))
		}
		vecs = make([][]float32, len(aspects))
		for i, phrase := range aspects {
			vec, err := s.embedder.EmbedQuery(ctx, phrase)
			if err != nil {
				s.logger.Debug("reflect missing_info embed failed",
					slog.String("phrase", phrase), slog.Any("error", err))
				continue
			}
			vecs[i] = vec
		}
	}

	for i, phrase := range aspects {
		vec := vecs[i]
		if len(vec) == 0 {
			continue
		}
		mx.ExtraEmbeddings.Add(ctx, 1)
		results, err := s.postgres.VectorSearch(ctx, vec, st.Params.CubeID, st.Params.UserName, TextScopes, st.Params.AgentID, subK)
		if err != nil {
			s.logger.Debug("reflect missing_info vector search failed",
				slog.String("phrase", phrase), slog.Any("error", err))
			continue
		}
		st.TextFormatted = mergeUnseenFormatted(st.TextFormatted, vectorResultsToMerged(results), st.Params.IncludeEmbedding)
	}
}

// vectorResultsToMerged converts the db-layer VectorSearchResult slice into
// the search-layer MergedResult slice that FormatMergedItems accepts. Score
// is set to 0 because reflection-fetched items haven't been re-scored
// against the query — the rerank chain has already run for this query.
func vectorResultsToMerged(in []db.VectorSearchResult) []MergedResult {
	out := make([]MergedResult, 0, len(in))
	for _, r := range in {
		out = append(out, MergedResult{
			ID:         r.ID,
			Properties: r.Properties,
			Score:      0,
			Embedding:  r.Embedding,
		})
	}
	return out
}

// mergeUnseenFormatted formats `extra` and appends only items whose id is
// not already present in `existing`. Returns the new slice. Used by both
// reflection follow-up branches so dedup semantics stay identical.
func mergeUnseenFormatted(existing []map[string]any, extra []MergedResult, includeEmbedding bool) []map[string]any {
	if len(extra) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		if id, ok := item["id"].(string); ok && id != "" {
			seen[id] = struct{}{}
		}
	}
	formatted, _ := FormatMergedItems(extra, includeEmbedding)
	for _, item := range formatted {
		id, _ := item["id"].(string)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		existing = append(existing, item)
	}
	return existing
}
