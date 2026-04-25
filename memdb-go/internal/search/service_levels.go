// Package search — service_levels.go: MemOS L1/L2/L3 memory tier routing.
//
// SearchByLevel dispatches to tier-specific implementations based on
// SearchParams.Level. For Level="" (LevelAll) it calls the full pipeline
// in service.go unchanged (zero regression).
//
// Design intent: NO refactor of the existing pipeline. Each level path is
// a thin wrapper that skips irrelevant phases and returns a compatible
// *SearchOutput. The metric `memdb.search.level_total{level=...}` is emitted
// on every call via this dispatcher so the full pipeline path is also counted.
package search

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// SearchByLevel dispatches a search request to the correct memory tier.
// Callers (handlers) should call SearchByLevel when a level parameter is
// present; it transparently delegates to Search when Level==LevelAll.
func (s *SearchService) SearchByLevel(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Emit metric before dispatch so it counts every call including errors.
	levelLabel := string(p.Level)
	if levelLabel == "" {
		levelLabel = "all"
	}
	searchMx().LevelTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("level", levelLabel)))

	switch p.Level {
	case LevelL1:
		return s.searchL1(ctx, p)
	case LevelL2:
		return s.searchL2(ctx, p)
	case LevelL3:
		return s.searchL3(ctx, p)
	default:
		// LevelAll — full pipeline, backward compat.
		return s.Search(ctx, p)
	}
}

// searchL1 returns working memory only (Redis VSET / postgres WorkingMemory rows).
// Skips all vector/fulltext Postgres phases and graph recall.
func (s *SearchService) searchL1(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Step 1: Embed query (needed for cosine re-ranking of working mem items).
	queryVec, err := s.embedder.EmbedQuery(ctx, p.Query)
	if err != nil {
		return nil, err
	}

	// Step 2: Fetch working memory directly.
	items, err := s.postgres.GetWorkingMemory(ctx, p.CubeID, p.UserName, WorkingMemoryLimit, p.AgentID)
	if err != nil {
		s.logger.Debug("l1 working memory fetch failed", "error", err)
		items = nil // graceful degrade — return empty result
	}

	actMem := s.formatWorkingMem(queryVec, items, p)

	result := &SearchResult{
		TextMem:  []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		SkillMem: []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		ToolMem:  []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		PrefMem:  []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		ActMem:   toAnySlice(actMem),
		ParaMem:  []any{},
	}
	return &SearchOutput{Result: result}, nil
}

// searchL2 returns episodic memories only (Postgres Memory rows where memory_type='EpisodicMemory').
// Uses EpisodicScopes to restrict the vector and fulltext search.
// Graph recall, working memory, and BFS expansion are skipped.
func (s *SearchService) searchL2(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Step 1: Embed query.
	queryVec, err := s.embedder.EmbedQuery(ctx, p.Query)
	if err != nil {
		return nil, err
	}

	budget := s.computeBudget(p)
	t0 := time.Now()

	// Step 2: Vector search restricted to EpisodicMemory scope only.
	var episodicVec []db.VectorSearchResult
	switch {
	case len(p.CubeIDs) > 1:
		episodicVec, err = s.postgres.VectorSearchMultiCube(ctx, queryVec, p.CubeIDs, p.UserName, EpisodicScopes, p.AgentID, budget.textK)
	default:
		episodicVec, err = s.postgres.VectorSearch(ctx, queryVec, p.CubeID, p.UserName, EpisodicScopes, p.AgentID, budget.textK)
	}
	if err != nil {
		return nil, err
	}
	parallelDur := time.Since(t0)
	s.logger.Debug("l2 episodic search", "count", len(episodicVec), "dur", parallelDur)

	// Step 3: Format.
	merged := make([]MergedResult, 0, len(episodicVec))
	for _, r := range episodicVec {
		merged = append(merged, MergedResult{
			ID:         r.ID,
			Properties: r.Properties,
			Score:      r.Score,
			Embedding:  r.Embedding,
		})
	}
	textFormatted, _ := FormatMergedItems(merged, p.IncludeEmbedding)
	textFormatted = TrimSlice(textFormatted, p.TopK)

	result := &SearchResult{
		TextMem:  []MemoryBucket{{CubeID: p.CubeID, Memories: textFormatted, TotalNodes: len(textFormatted)}},
		SkillMem: []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		ToolMem:  []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		PrefMem:  []MemoryBucket{{CubeID: p.CubeID, Memories: []map[string]any{}, TotalNodes: 0}},
		ActMem:   []any{},
		ParaMem:  []any{},
	}
	return &SearchOutput{Result: result}, nil
}

// searchL3 runs the full LTM graph pipeline via the standard Search path.
// The key difference from LevelAll: L3 calls the full search pipeline
// with standard TextScopes (LTM + UserMemory + EpisodicMemory) and includes
// graph traversal, making it semantically equivalent to the current default.
// Kept as a distinct entry point for future differentiation and for metric labelling.
func (s *SearchService) searchL3(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// L3 is the full pipeline — delegate to Search.
	return s.Search(ctx, p)
}

// toAnySlice converts []map[string]any to []any for SearchResult.ActMem.
func toAnySlice(in []map[string]any) []any {
	if len(in) == 0 {
		return []any{}
	}
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
