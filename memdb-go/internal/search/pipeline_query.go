// Package search — pipeline_query.go: query-side stages of the search
// pipeline (D7 / D4 / embed / tokenize / temporal cutoff / D11).
//
// Each stage's body is a verbatim lift of the corresponding chunk from the
// pre-R2 SearchService.Search method. No behavior changes — only the side
// effect of writing into *pipelineState instead of local variables.
package search

import (
	"context"
	"time"
)

// stageD7CoTDecompose runs D7 — optional LLM Chain-of-Thought query
// decomposition. Multi-part questions get split into up to 3 atomic
// sub-questions. Always normalises subqueries[0] to be the original query.
// Env-gated (MEMDB_SEARCH_COT) — graceful degrade to a single-element slice
// on any error / short query / disabled env.
func (s *SearchService) stageD7CoTDecompose(ctx context.Context, st *pipelineState) error {
	subs := DecomposeQuery(ctx, s.logger, st.Params.Query, CoTConfig{
		APIURL: s.LLMReranker.APIURL,
		APIKey: s.LLMReranker.APIKey,
		Model:  s.LLMReranker.Model,
	})
	if len(subs) == 0 || subs[0] != st.Params.Query {
		subs = append([]string{st.Params.Query}, subs...)
	}
	st.Subqueries = subs
	return nil
}

// stageD4QueryRewrite runs D4 — optional LLM rewrite of the query before
// embedding. Falls back to the original query on any error / low confidence
// / short or long query. Only the embed call uses the rewritten query;
// BM25, CE rerank, LLM rerank, and D10 enhance keep p.Query for user-intent
// fidelity.
func (s *SearchService) stageD4QueryRewrite(ctx context.Context, st *pipelineState) error {
	rewritten, _ := applyQueryRewrite(ctx, s.logger, st.Params.Query,
		time.Now().UTC().Format(time.RFC3339),
		QueryRewriteConfig{
			APIURL: s.LLMReranker.APIURL,
			APIKey: s.LLMReranker.APIKey,
			Model:  s.LLMReranker.Model,
		})
	st.EmbedQuery = rewritten
	return nil
}

// stageEmbedQuery embeds the (possibly D4-rewritten) query. This is the
// only fatal stage in the pipeline — Search() unwraps st.embedErr after
// runPipeline and returns it as the function-level error so callers see
// the true cause instead of an empty result.
func (s *SearchService) stageEmbedQuery(ctx context.Context, st *pipelineState) error {
	vec, err := s.embedder.EmbedQuery(ctx, st.EmbedQuery)
	if err != nil {
		st.embedErr = err
		return err
	}
	st.QueryVec = vec
	return nil
}

// stageTokenizeTemporal builds the BM25 token list, the tsquery, and
// detects an optional temporal cutoff from the natural-language query.
// Pure CPU — never errors.
func (s *SearchService) stageTokenizeTemporal(_ context.Context, st *pipelineState) error {
	st.Tokens = TokenizeMixed(st.Params.Query)
	st.TSQuery = BuildTSQuery(st.Tokens)
	st.CutoffISO, st.HasCutoff = s.detectTemporalCutoff(st.Params.Query)
	st.Budget = s.computeBudget(st.Params)
	return nil
}

// stageD7CoTAugment fans subqueries[1:] through extra VectorSearch probes
// and unions the results into psr by id (max-score). No-op for atomic
// queries (len(subqueries) <= 1).
func (s *SearchService) stageD7CoTAugment(ctx context.Context, st *pipelineState) error {
	if st.PSR == nil {
		st.skip("d7_cot_augment")
		return nil
	}
	if len(st.Subqueries) <= 1 {
		st.skip("d7_cot_augment")
		return nil
	}
	s.augmentWithSubqueries(ctx, st.PSR, st.Subqueries, st.Params, st.Budget)
	return nil
}

// stageD11CoTDecompose runs D11 — Chain-of-Thought decomposition for
// multi-hop / temporal questions. Sibling of D7 with a stricter heuristic
// gate. Each sub-query[1:] runs an extra text-scope VectorSearch and the
// results are unioned into psr.textVec so the downstream D2 expandViaGraph
// step walks edges from a richer seed set. No-op when CoTDecomposer is nil
// or the gate skips.
func (s *SearchService) stageD11CoTDecompose(ctx context.Context, st *pipelineState) error {
	if st.PSR == nil {
		st.skip("d11_cot_decompose")
		return nil
	}
	if s.CoTDecomposer == nil {
		st.skip("d11_cot_decompose")
		return nil
	}
	st.D11Subqueries = s.applyCoTDecomposition(ctx, st.PSR, st.Params.Query, st.Params, st.Budget)
	return nil
}
