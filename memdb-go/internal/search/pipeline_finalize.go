// Package search — pipeline_finalize.go: format / post-process /
// working-memory / response build / profile inject / async retrieval-count
// stages. These run after the candidate pool is finalised.
//
// The post_process stage delegates to s.postProcessResults (R3) — R2 does
// NOT touch the rerank chain implementation; we just wrap the call.
package search

import (
	"context"
	"log/slog"
	"time"
)

// stageFormatItems formats merged per-type slices into the wire-format
// map[string]any candidates, capturing per-id embeddings for downstream
// rerank/dedup stages. Pure CPU — never errors.
func (s *SearchService) stageFormatItems(_ context.Context, st *pipelineState) error {
	st.TextFormatted, st.TextEmbByID = FormatMergedItems(st.TextMerged, st.Params.IncludeEmbedding)
	st.SkillFormatted, st.SkillEmbByID = FormatMergedItems(st.SkillMerged, st.Params.IncludeEmbedding)
	st.ToolFormatted, st.ToolEmbByID = FormatMergedItems(st.ToolMerged, st.Params.IncludeEmbedding)
	if st.PSR != nil {
		st.PrefFormatted = FormatPrefResults(st.PSR.prefResults)
	}
	return nil
}

// stagePostProcess delegates to the existing postProcessResults function
// (R3 territory — DO NOT modify there). Runs steps 6–11: cosine + CE +
// LLM rerank, iterative expansion, temporal decay, relativity threshold,
// pref quality filter, dedup, cross-source dedup, D10 enhance, trim,
// strip-embeddings.
//
// Per-rerank-strategy timings (ce / llm / iterative) are emitted by
// postProcessResults via the legacy slog line — they ALSO show up in the
// new memdb.search.stage_duration_ms{stage="post_process"} as the sum
// (which is what operators want for budget tracking). Detailed breakdown
// stays available in the existing logs.
func (s *SearchService) stagePostProcess(ctx context.Context, st *pipelineState) error {
	text, skill, tool, pref, _, _, _ := s.postProcessResults(
		ctx, st.QueryVec,
		st.TextEmbByID, st.SkillEmbByID, st.ToolEmbByID,
		st.TextFormatted, st.SkillFormatted, st.ToolFormatted, st.PrefFormatted,
		st.Params,
	)
	st.TextFormatted = text
	st.SkillFormatted = skill
	st.ToolFormatted = tool
	st.PrefFormatted = pref
	return nil
}

// stageWorkingMemFormat formats working-memory rows into the response's
// ActMem slice. Cosine-rescores against the query vector for stable
// ordering. No-op when psr is nil or working-memory was empty.
func (s *SearchService) stageWorkingMemFormat(_ context.Context, st *pipelineState) error {
	if st.PSR == nil {
		st.skip("working_mem_format")
		return nil
	}
	st.ActMemFormatted = s.formatWorkingMem(st.QueryVec, st.PSR.workingMemItems, st.Params)
	return nil
}

// stageBuildResponse assembles the final SearchResult from the formatted
// per-type slices. Always succeeds (pure data shaping).
func (s *SearchService) stageBuildResponse(_ context.Context, st *pipelineState) error {
	st.Result = buildFullSearchResult(st.TextFormatted, st.SkillFormatted, st.ToolFormatted, st.PrefFormatted, st.ActMemFormatted, st.Params.CubeID)
	return nil
}

// stageProfileInject injects the Memobase-style user profile summary into
// the response. No-op when Profiler is nil or CubeID is empty.
func (s *SearchService) stageProfileInject(ctx context.Context, st *pipelineState) error {
	if s.Profiler == nil || st.Params.CubeID == "" {
		st.skip("profile_inject")
		return nil
	}
	if st.Result == nil {
		st.skip("profile_inject")
		return nil
	}
	st.Result.ProfileMem = s.Profiler.GetProfile(ctx, st.Params.CubeID)
	return nil
}

// stageRetrievalCountAsync fires-and-forgets a goroutine that bumps the
// retrieval_count column on every returned text/skill row. Cap-bound by
// the rows' explicit ID list — no fan-out beyond the response size. Soft-
// fail: errors land in s.logger.Debug, never blocking the pipeline.
func (s *SearchService) stageRetrievalCountAsync(_ context.Context, st *pipelineState) error {
	if s.postgres == nil {
		st.skip("retrieval_count_async")
		return nil
	}
	ids := collectResultIDs(st.TextFormatted, st.SkillFormatted)
	if len(ids) == 0 {
		st.skip("retrieval_count_async")
		return nil
	}
	nowStr := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	go func() {
		if err := s.postgres.IncrRetrievalCount(context.Background(), ids, nowStr); err != nil {
			s.logger.Debug("incr retrieval count failed", slog.Any("error", err))
		}
	}()
	return nil
}
