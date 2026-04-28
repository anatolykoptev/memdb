// Package search — pipeline_multihop.go: parallel-DB / BFS / internet /
// merge / D2 graph expansion / CONTRADICTS stages.
//
// Each stage's body is a verbatim lift of the corresponding chunk from the
// pre-R2 SearchService.Search method.
package search

import (
	"context"
)

// stageParallelDB runs the parallel DB fan-out: vector + fulltext per scope
// (text/skill/tool/pref), graph recall by key/tag/entity, working-memory,
// and (optionally) the internet sidecar. The single point in the pipeline
// where Postgres / Qdrant load is concentrated.
//
// Returning an error here normally would have been fatal in the legacy
// Search method, but per the soft-fail contract we now record the error
// and continue with an empty psr — downstream stages tolerate nil/zero
// data and produce an empty result rather than a 5xx. The caller can
// inspect state.Errors if they need to distinguish.
func (s *SearchService) stageParallelDB(ctx context.Context, st *pipelineState) error {
	psr, err := s.runParallelSearches(ctx, st.QueryVec, st.Tokens, st.TSQuery, st.CutoffISO, st.HasCutoff, st.Params, st.Budget)
	if err != nil {
		return err
	}
	st.PSR = psr
	return nil
}

// stageBFSExpand performs BFS multi-hop expansion serially after the
// parallel phase. Walks memory_edges from text-scope vector seeds.
func (s *SearchService) stageBFSExpand(ctx context.Context, st *pipelineState) error {
	if st.PSR == nil {
		st.skip("bfs_expand")
		return nil
	}
	st.BFSResults = s.runBFSExpansion(ctx, st.PSR.textVec, st.Params)
	return nil
}

// stageInternetEmbed embeds internet results (if any). No-op when
// internet_search is disabled or returned no results.
func (s *SearchService) stageInternetEmbed(ctx context.Context, st *pipelineState) error {
	if st.PSR == nil || len(st.PSR.internetResults) == 0 {
		st.skip("internet_embed")
		return nil
	}
	st.InternetMerged = s.embedInternetResults(ctx, st.PSR.internetResults)
	return nil
}

// stageMergeCandidates merges per-type vector + fulltext + BFS + internet
// streams into TextMerged / SkillMerged / ToolMerged.
func (s *SearchService) stageMergeCandidates(_ context.Context, st *pipelineState) error {
	if st.PSR == nil {
		st.skip("merge_candidates")
		return nil
	}
	st.TextMerged, st.SkillMerged, st.ToolMerged = s.mergeSearchResults(st.PSR, st.BFSResults, st.InternetMerged, st.Params)
	return nil
}

// stageD2GraphExpand runs D2 multi-hop graph expansion (env-gated by
// MEMDB_SEARCH_MULTIHOP=true; default off). Walks memory_edges up to 2
// hops from current text_mem candidates, injecting neighbors with 0.8^hop
// score decay. Pool capped at 2× original size. Runs before CONTRADICTS
// so expanded neighbors also get the penalty if they are contradicted by
// a seed. No-op when disabled or on DB error.
func (s *SearchService) stageD2GraphExpand(ctx context.Context, st *pipelineState) error {
	st.TextMerged = expandViaGraph(ctx, s.postgres, s.logger, st.TextMerged, st.QueryVec, st.Params.CubeID, st.Params.UserName, st.Params.AgentID)
	return nil
}

// stageContradictsPenalty applies the CONTRADICTS edge penalty to text
// candidates. Pulls top-N seed IDs and queries CONTRADICTS edges; matches
// get a score multiplier reduction. Soft-fails on DB error (returns the
// input unchanged via the helper's existing graceful path).
func (s *SearchService) stageContradictsPenalty(ctx context.Context, st *pipelineState) error {
	st.TextMerged = s.applyContradictsPenalty(ctx, st.TextMerged, st.Params)
	return nil
}
