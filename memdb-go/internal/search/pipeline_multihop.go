// Package search — pipeline_multihop.go: parallel-DB / BFS / internet /
// merge / D2 graph expansion / CONTRADICTS stages.
//
// Each stage's body is a verbatim lift of the corresponding chunk from the
// pre-R2 SearchService.Search method.
package search

import (
	"context"
	"regexp"
	"strings"
)

// cat2QueryRe matches the leading temporal interrogative phrases that
// characterise LoCoMo category-2 (multi-hop temporal) questions:
//
//	"When did ...", "How long ...", "After what ...", "Before what ...",
//	"How many months/years/days/weeks ago ...", "At what time ...",
//	"What year/month/day/date ..."
//
// "How many" is deliberately restricted to temporal units (months, years,
// days, weeks, times) to avoid false-positive matches on non-temporal
// cardinality questions like "How many siblings does she have?".
//
// The pattern is deliberately broad for the other starters — false positives
// (e.g. non-cat-2 "When was X built?") get a lower threshold but never lose
// recall. False negatives (genuine cat-2 that don't match) fall through to
// the default threshold unchanged.
//
// Env override MEMDB_CAT2_THRESHOLD=0 disables the adjustment entirely.
var cat2QueryRe = regexp.MustCompile(
	`(?i)^(when |how long |how many (months|years|days|weeks|times) |after what |before what |at what (time|point) |what (year|month|day|date) )`,
)

// isCat2Query returns true when q matches the cat-2 temporal heuristic.
// Exported so it can be pinned by TestCat2Heuristic.
func isCat2Query(q string) bool {
	return cat2QueryRe.MatchString(strings.TrimSpace(q))
}

// cat3QueryRe matches LoCoMo category-3 (multi-hop / aggregation /
// comparison) starters: "Compare ...", "Which one ...", "How does X
// relate to Y ...", "What's the difference ...", "List all ...",
// "Summarise ...". Used by the F2 reflection-loop gate to decide whether
// a query is "complex enough" to spend an extra LLM judgement on.
//
// The pattern is deliberately broad — false positives here only mean a
// reflection LLM call fires on a slightly-easier-than-expected query,
// which is recoverable (the LLM returns "sufficient" and the loop exits).
// False negatives push the query to the default path with no extra cost.
var cat3QueryRe = regexp.MustCompile(
	`(?i)^(compare |which (one|of) |how does .+ relate |what(?:'s| is) the difference|list all |summari[sz]e |across all |between .+ and )`,
)

// isCat3Query returns true when q matches the cat-3 multi-hop / aggregation
// heuristic. Pair with isCat2Query for the F2 reflection-loop "complex
// query" gate (MEMDB_REFLECTION_ON_COMPLEX_ONLY).
func isCat3Query(q string) bool {
	return cat3QueryRe.MatchString(strings.TrimSpace(q))
}

// applyCat2Threshold lowers st.Params.Relativity to thr when the query is a
// cat-2 temporal multi-hop question and thr is strictly less than the current
// value.
//
// Semantics: only-lower-never-raise.
//   - Relativity == 0 means "no threshold filter" — that intent is preserved;
//     we never raise it from zero to thr.
//   - Relativity > 0 and thr < Relativity → lower to thr (broaden recall for
//     weak bridging hops).
//   - Relativity > 0 and thr >= Relativity → keep (caller already has a
//     looser or equal threshold, no adjustment needed).
//
// Returns the category label ("cat2" or "other") for metric tagging.
func applyCat2Threshold(st *pipelineState) string {
	if !isCat2Query(st.Params.Query) {
		return "other"
	}
	thr := cat2Threshold()
	// Only lower the threshold for cat-2 queries; never raise.
	// Relativity == 0 means "no threshold filter" — respect that intent.
	if st.Params.Relativity > 0 && thr < st.Params.Relativity {
		st.Params.Relativity = thr
	}
	return "cat2"
}

// stageParallelDB runs the parallel DB fan-out: vector + fulltext per scope
// (text/skill/tool/pref), graph recall by key/tag/entity, working-memory,
// and (optionally) the internet sidecar. The single point in the pipeline
// where Postgres / Qdrant load is concentrated.
//
// F9 recall budget: when isCat2Query detects a temporal multi-hop question
// ("When...", "How long...", etc.), Params.Relativity is lowered to
// cat2Threshold() (default 0.05, env MEMDB_CAT2_THRESHOLD) for this query
// only.  This lets weak bridging-hop memories survive the similarity filter
// rather than being cut before the reranker.  All other queries are
// unchanged.
//
// Returning an error here normally would have been fatal in the legacy
// Search method, but per the soft-fail contract we now record the error
// and continue with an empty psr — downstream stages tolerate nil/zero
// data and produce an empty result rather than a 5xx. The caller can
// inspect state.Errors if they need to distinguish.
func (s *SearchService) stageParallelDB(ctx context.Context, st *pipelineState) error {
	// F9: cat-2 heuristic — drop threshold for temporal multi-hop queries.
	cat := applyCat2Threshold(st)
	searchMx().RecallBudget.Add(ctx, 1, recallBudgetAttrs(cat, st.Budget.textK))

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
	if st.PSR == nil {
		st.skip("d2_graph_expand")
		return nil
	}
	st.TextMerged = expandViaGraph(ctx, s.postgres, s.logger, st.TextMerged, st.QueryVec, st.Params.CubeID, st.Params.UserName, st.Params.AgentID)
	return nil
}

// stageContradictsPenalty applies the CONTRADICTS edge penalty to text
// candidates. Pulls top-N seed IDs and queries CONTRADICTS edges; matches
// get a score multiplier reduction. Soft-fails on DB error (returns the
// input unchanged via the helper's existing graceful path).
func (s *SearchService) stageContradictsPenalty(ctx context.Context, st *pipelineState) error {
	if st.PSR == nil {
		st.skip("contradicts_penalty")
		return nil
	}
	st.TextMerged = s.applyContradictsPenalty(ctx, st.TextMerged, st.Params)
	return nil
}
