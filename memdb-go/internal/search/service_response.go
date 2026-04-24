// Package search — service_response.go: working-memory formatting, result-ID
// collection, and final SearchResult construction.
package search

import (
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// formatWorkingMem converts working memory items to formatted act_mem entries.
func (s *SearchService) formatWorkingMem(queryVec []float32, items []db.VectorSearchResult, p SearchParams) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	wmMerged := make([]MergedResult, 0, len(items))
	for _, r := range items {
		wmMerged = append(wmMerged, MergedResult{
			ID:         r.ID,
			Properties: r.Properties,
			Score:      WorkingMemBaseScore,
			Embedding:  r.Embedding,
		})
	}
	actFormatted, actEmbByID := FormatMergedItems(wmMerged, false)
	actFormatted = ReRankByCosine(queryVec, actFormatted, actEmbByID)
	if p.Relativity > 0 {
		threshold := p.Relativity - 0.10
		if threshold > 0 {
			actFormatted = FilterByRelativity(actFormatted, threshold)
		}
	}
	actFormatted = TrimSlice(actFormatted, WorkingMemoryLimit)
	StripEmbeddings(actFormatted)
	return actFormatted
}

// collectResultIDs extracts the database node IDs from formatted search result slices.
// Used to batch-increment retrieval_count after a search response is built.
// Reads the "id" field from each item's metadata (set by FormatMemoryItem).
func collectResultIDs(buckets ...[]map[string]any) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, bucket := range buckets {
		for _, item := range bucket {
			meta, _ := item["metadata"].(map[string]any)
			if meta == nil {
				continue
			}
			id, _ := meta["id"].(string)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// buildFullSearchResult creates a SearchResult with all memory types.
func buildFullSearchResult(text, skill, tool, pref, actMem []map[string]any, cubeID string) *SearchResult {
	if text == nil {
		text = []map[string]any{}
	}
	if skill == nil {
		skill = []map[string]any{}
	}
	if tool == nil {
		tool = []map[string]any{}
	}
	if pref == nil {
		pref = []map[string]any{}
	}

	actAny := make([]any, 0, len(actMem))
	for _, item := range actMem {
		actAny = append(actAny, item)
	}

	return &SearchResult{
		TextMem:  []MemoryBucket{{CubeID: cubeID, Memories: text, TotalNodes: len(text)}},
		SkillMem: []MemoryBucket{{CubeID: cubeID, Memories: skill, TotalNodes: len(skill)}},
		ToolMem:  []MemoryBucket{{CubeID: cubeID, Memories: tool, TotalNodes: len(tool)}},
		PrefMem:  []MemoryBucket{{CubeID: cubeID, Memories: pref, TotalNodes: len(pref)}},
		ActMem:   actAny,
		ParaMem:  []any{},
	}
}
