// Package search — service_cot.go: D7 Chain-of-Thought augmentation.
//
// When DecomposeQuery yields more than one sub-question, each sub-question is
// embedded independently and used to run extra vector searches on the text /
// skill / tool scopes. Those results are unioned into the primary
// parallelSearchResults by id (keeping max score) so the downstream pipeline
// (merge, graph expansion, CE rerank, LLM rerank, D5 staged, D10 enhance) can
// operate on a single combined pool without any further structural changes.
//
// Design invariants:
//   - No-op when len(subqueries) <= 1 (primary path is already optimal).
//   - A single subquery failure must NOT kill the whole pipeline; the error
//     is logged and the remaining subqueries still contribute. A post-hoc
//     rerank compensates for inconsistent per-sub scores.
//   - Skill / tool scopes are only searched when the caller enabled them to
//     mirror the primary path's budget gating.
package search

import (
	"context"
	"log/slog"
	"time"
)

// augmentWithSubqueries runs an extra VectorSearch per sub-question (after the
// first, which already ran via the primary path), then unions every returned
// item into the existing psr.textVec / skillVec / toolVec slices by id —
// keeping the max score across subqueries. Each subquery is independently
// passed through the query rewriter and embedder so both D4 and D7 compose.
//
// subqueries[0] is assumed to be the primary query that already produced psr;
// it is NOT re-embedded here. Only subqueries[1:] trigger extra LLM/DB work.
func (s *SearchService) augmentWithSubqueries(
	ctx context.Context,
	psr *parallelSearchResults,
	subqueries []string,
	p SearchParams,
	budget searchBudget,
) {
	if len(subqueries) <= 1 {
		return
	}
	for _, sq := range subqueries[1:] {
		// Apply D4 rewrite to the sub-question too so both features compose.
		subEmbedQuery, _ := applyQueryRewrite(ctx, s.logger, sq,
			time.Now().UTC().Format(time.RFC3339),
			QueryRewriteConfig{
				APIURL: s.LLMReranker.APIURL,
				APIKey: s.LLMReranker.APIKey,
				Model:  s.LLMReranker.Model,
			})
		vec, err := s.embedder.EmbedQuery(ctx, subEmbedQuery)
		if err != nil {
			s.logger.Debug("cot subquery embed failed", slog.String("sub", sq), slog.Any("error", err))
			continue
		}

		// Text scope — always run.
		if extra, err := s.postgres.VectorSearch(ctx, vec, p.CubeID, p.UserName, TextScopes, p.AgentID, budget.textK); err == nil {
			psr.textVec = unionVectorResults(psr.textVec, extra)
		} else {
			s.logger.Debug("cot subquery text vector search failed", slog.String("sub", sq), slog.Any("error", err))
		}

		// Skill scope — only when caller opted in.
		if p.IncludeSkill && p.SkillTopK > 0 {
			if extra, err := s.postgres.VectorSearch(ctx, vec, p.CubeID, p.UserName, SkillScopes, p.AgentID, budget.skillK); err == nil {
				psr.skillVec = unionVectorResults(psr.skillVec, extra)
			} else {
				s.logger.Debug("cot subquery skill vector search failed", slog.String("sub", sq), slog.Any("error", err))
			}
		}

		// Tool scope — only when caller opted in.
		if p.IncludeTool && p.ToolTopK > 0 {
			if extra, err := s.postgres.VectorSearch(ctx, vec, p.CubeID, p.UserName, ToolScopes, p.AgentID, budget.toolK); err == nil {
				psr.toolVec = unionVectorResults(psr.toolVec, extra)
			} else {
				s.logger.Debug("cot subquery tool vector search failed", slog.String("sub", sq), slog.Any("error", err))
			}
		}
	}
}
