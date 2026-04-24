// Package search — service_parallel.go: parallel DB fetch phase + post-parallel
// augmentation (internet embedding, BFS graph expansion).
package search

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// runParallelSearches executes all DB searches concurrently.
func (s *SearchService) runParallelSearches(
	ctx context.Context,
	queryVec []float32,
	tokens []string,
	tsquery, cutoffISO string,
	hasCutoff bool,
	p SearchParams,
	budget searchBudget,
) (*parallelSearchResults, error) {
	g, gctx := errgroup.WithContext(ctx)
	psr := &parallelSearchResults{}

	s.spawnTextSearches(g, gctx, psr, queryVec, tsquery, cutoffISO, hasCutoff, p, budget)
	s.spawnSkillToolSearches(g, gctx, psr, queryVec, tsquery, p, budget)
	s.spawnPrefSearch(g, gctx, psr, queryVec, p, budget)
	s.spawnWorkingMemAndGraph(g, gctx, psr, tokens, p)
	s.spawnInternetSearch(g, gctx, psr, p)

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return psr, nil
}

// spawnTextSearches enqueues vector and fulltext search goroutines for the text scope.
func (s *SearchService) spawnTextSearches(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	queryVec []float32, tsquery, cutoffISO string, hasCutoff bool,
	p SearchParams, budget searchBudget,
) {
	g.Go(func() error {
		var err error
		switch {
		case hasCutoff:
			// Filter by CubeID: postgres filters by the `user_name` JSONB property,
			// which writes populate from cube_id. CubeID comes from readable_cube_ids,
			// falling back to user_id when no cube is specified. See handlers/search.go
			// buildSearchParams for the naming note.
			psr.textVec, err = s.postgres.VectorSearchWithCutoff(ctx, queryVec, p.CubeID, p.UserName, TextScopes, budget.textK, cutoffISO, p.AgentID)
		case len(p.CubeIDs) > 1:
			psr.textVec, err = s.postgres.VectorSearchMultiCube(ctx, queryVec, p.CubeIDs, p.UserName, TextScopes, p.AgentID, budget.textK)
		default:
			psr.textVec, err = s.postgres.VectorSearch(ctx, queryVec, p.CubeID, p.UserName, TextScopes, p.AgentID, budget.textK)
		}
		return err
	})
	if tsquery != "" {
		g.Go(func() error {
			var err error
			if hasCutoff {
				psr.textFT, err = s.postgres.FulltextSearchWithCutoff(ctx, tsquery, p.CubeID, p.UserName, TextScopes, budget.textK, cutoffISO, p.AgentID)
			} else {
				psr.textFT, err = s.postgres.FulltextSearch(ctx, tsquery, p.CubeID, p.UserName, TextScopes, p.AgentID, budget.textK)
			}
			return err
		})
	}
}

// spawnSkillToolSearches enqueues skill and tool vector/fulltext search goroutines.
func (s *SearchService) spawnSkillToolSearches(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	queryVec []float32, tsquery string, p SearchParams, budget searchBudget,
) {
	if p.IncludeSkill && p.SkillTopK > 0 {
		g.Go(func() error {
			var err error
			// Filter by CubeID (see spawnTextSearches for the naming note).
			psr.skillVec, err = s.postgres.VectorSearch(ctx, queryVec, p.CubeID, p.UserName, SkillScopes, p.AgentID, budget.skillK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				psr.skillFT, err = s.postgres.FulltextSearch(ctx, tsquery, p.CubeID, p.UserName, SkillScopes, p.AgentID, budget.skillK)
				return err
			})
		}
	}
	if p.IncludeTool && p.ToolTopK > 0 {
		g.Go(func() error {
			var err error
			// Filter by CubeID (see spawnTextSearches for the naming note).
			psr.toolVec, err = s.postgres.VectorSearch(ctx, queryVec, p.CubeID, p.UserName, ToolScopes, p.AgentID, budget.toolK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				psr.toolFT, err = s.postgres.FulltextSearch(ctx, tsquery, p.CubeID, p.UserName, ToolScopes, p.AgentID, budget.toolK)
				return err
			})
		}
	}
}

// spawnPrefSearch enqueues the Qdrant preference search goroutine if applicable.
func (s *SearchService) spawnPrefSearch(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	queryVec []float32, p SearchParams, budget searchBudget,
) {
	if s.qdrant == nil || !p.IncludePref || p.PrefTopK <= 0 {
		return
	}
	g.Go(func() error {
		for _, coll := range PrefCollections {
			results, err := s.qdrant.SearchByVector(ctx, coll, queryVec, uint64(budget.prefK), p.UserName) //nolint:gosec // prefK is a small positive search top-k value
			if err != nil {
				s.logger.Debug("pref search failed", slog.String("collection", coll), slog.Any("error", err))
				continue
			}
			psr.prefResults = append(psr.prefResults, results...)
		}
		return nil
	})
}

// spawnWorkingMemAndGraph enqueues working-memory fetch and all graph recall goroutines.
func (s *SearchService) spawnWorkingMemAndGraph(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	tokens []string, p SearchParams,
) {
	g.Go(func() error {
		var err error
		// Filter by CubeID (see spawnTextSearches for the naming note).
		psr.workingMemItems, err = s.postgres.GetWorkingMemory(ctx, p.CubeID, p.UserName, WorkingMemoryLimit, p.AgentID)
		if err != nil {
			s.logger.Debug("working memory fetch failed", slog.Any("error", err))
		}
		return nil
	})
	if len(tokens) > 0 {
		g.Go(func() error {
			var err error
			psr.graphKeyResults, err = s.postgres.GraphRecallByKey(ctx, p.CubeID, p.UserName, GraphRecallScopes, tokens, p.AgentID, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("graph recall by key failed", slog.Any("error", err))
			}
			return nil
		})
		g.Go(func() error {
			normalized := make([]string, len(tokens))
			for i, t := range tokens {
				normalized[i] = db.NormalizeEntityID(t)
			}
			entityIDs, err := s.postgres.FindEntitiesByNormalizedID(ctx, normalized, p.CubeID, p.UserName)
			if err != nil || len(entityIDs) == 0 {
				return nil
			}
			psr.entityGraphResults, err = s.postgres.GetMemoriesByEntityIDs(ctx, entityIDs, p.CubeID, p.UserName, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("entity graph recall failed", slog.Any("error", err))
			}
			return nil
		})
	}
	if len(tokens) >= 2 {
		g.Go(func() error {
			var err error
			psr.graphTagResults, err = s.postgres.GraphRecallByTags(ctx, p.CubeID, p.UserName, GraphRecallScopes, tokens, p.AgentID, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("graph recall by tags failed", slog.Any("error", err))
			}
			return nil
		})
	}
}

// spawnInternetSearch enqueues an internet search goroutine if enabled.
func (s *SearchService) spawnInternetSearch(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults, p SearchParams,
) {
	if s.Internet == nil || !p.InternetSearch {
		return
	}
	g.Go(func() error {
		var err error
		psr.internetResults, err = s.Internet.Search(ctx, p.Query)
		if err != nil {
			s.logger.Debug("internet search failed", slog.Any("error", err))
		}
		return nil // never fail the pipeline
	})
}

// embedInternetResults converts internet search results into MergedResults with embeddings.
func (s *SearchService) embedInternetResults(ctx context.Context, results []InternetResult) []MergedResult {
	if len(results) == 0 || s.embedder == nil {
		return nil
	}
	texts := make([]string, len(results))
	for i, r := range results {
		texts[i] = r.Text()
	}
	vecs, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		s.logger.Debug("internet embed failed", slog.Any("error", err))
		return nil
	}
	merged := make([]MergedResult, 0, len(results))
	for i, r := range results {
		if i >= len(vecs) {
			break
		}
		merged = append(merged, MergedResult{
			ID: "internet:" + r.URL,
			Properties: fmt.Sprintf(
				`{"memory": %q, "memory_type": "InternetMemory", "sources": [{"url": %q, "title": %q}]}`,
				r.Text(), r.URL, r.Title,
			),
			Score:     InternetBaseScore,
			Embedding: vecs[i],
		})
	}
	return merged
}

// runBFSExpansion runs graph BFS traversal from top vector hits.
func (s *SearchService) runBFSExpansion(ctx context.Context, textVec []db.VectorSearchResult, p SearchParams) []db.GraphRecallResult {
	if len(textVec) == 0 {
		return nil
	}
	seedN := 5
	if len(textVec) < seedN {
		seedN = len(textVec)
	}
	seedIDs := make([]string, 0, seedN)
	for _, r := range textVec[:seedN] {
		seedIDs = append(seedIDs, r.ID)
	}
	bfs, err := s.postgres.GraphBFSTraversal(ctx, seedIDs, p.CubeID, p.UserName, GraphRecallScopes, 2, GraphRecallLimit, p.AgentID)
	if err != nil {
		s.logger.Debug("graph bfs traversal failed", slog.Any("error", err))
		return nil
	}
	return bfs
}
