// Package search — service_fine.go: fine-mode orchestration (fast search → LLM filter → recall).
package search

import "context"

// SearchFine runs fast search + LLM filtering + optional recall.
func (s *SearchService) SearchFine(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Run the standard fast search first.
	p.LLMRerank = true // fine always reranks
	output, err := s.Search(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.Fine.APIURL == "" || output.Result == nil {
		return output, nil
	}

	// Extract text memories for LLM filtering.
	textMems := extractBucketMemories(output.Result.TextMem)
	if len(textMems) < 3 {
		return output, nil // too few to filter
	}

	// LLM filter.
	kept := LLMFilter(ctx, p.Query, textMems, s.Fine)
	dropRate := 1.0 - float64(len(kept))/float64(len(textMems))

	// Recall for missing if >30% dropped.
	if dropRate > fineRecallThresh && s.embedder != nil && s.postgres != nil {
		hint := LLMRecallHint(ctx, p.Query, kept, s.Fine)
		if hint != "" {
			recallResults := s.recallSearch(ctx, hint, p)
			kept = mergeRecallIntoKept(kept, recallResults, p.TopK)
		}
	}

	// Rebuild text_mem bucket.
	output.Result.TextMem = []MemoryBucket{{
		CubeID:     p.CubeID,
		Memories:   TrimSlice(kept, p.TopK),
		TotalNodes: len(kept),
	}}

	return output, nil
}

// extractBucketMemories flattens all memories from multiple buckets into a single slice.
func extractBucketMemories(buckets []MemoryBucket) []map[string]any {
	var all []map[string]any
	for _, b := range buckets {
		all = append(all, b.Memories...)
	}
	return all
}

// recallSearch embeds a hint query and performs vector search for missing memories.
func (s *SearchService) recallSearch(ctx context.Context, hint string, p SearchParams) []map[string]any {
	vecs, err := s.embedder.Embed(ctx, []string{hint})
	if err != nil || len(vecs) == 0 {
		return nil
	}
	// Filter by CubeID: postgres filters by the `user_name` JSONB property,
	// which writes populate from cube_id. CubeID comes from readable_cube_ids,
	// falling back to user_id when no cube is specified. See handlers/search.go
	// buildSearchParams for the naming note.
	results, err := s.postgres.VectorSearch(ctx, vecs[0], p.CubeID, p.UserName, TextScopes, p.AgentID, p.TopK)
	if err != nil {
		return nil
	}
	merged := MergeVectorAndFulltext(results, nil)
	formatted, _ := FormatMergedItems(merged, false)
	return formatted
}

// mergeRecallIntoKept appends recall results to kept, deduplicating by ID.
func mergeRecallIntoKept(kept, recall []map[string]any, limit int) []map[string]any {
	seen := make(map[string]bool, len(kept))
	for _, m := range kept {
		seen[extractID(m)] = true
	}
	for _, m := range recall {
		if len(kept) >= limit {
			break
		}
		if !seen[extractID(m)] {
			kept = append(kept, m)
			seen[extractID(m)] = true
		}
	}
	return kept
}
