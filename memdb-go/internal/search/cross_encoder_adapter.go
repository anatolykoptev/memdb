// Package search — adapter between memdb map[string]any items and
// go-kit/rerank.Client. Owns the step 6.05 metadata writebacks
// (relativity + cross_encoder_reranked) that downstream temporal decay
// and relativity threshold filters rely on.
package search

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/go-kit/rerank"
)

// rerankMemoryItems adapts map[string]any memory items to rerank.Doc,
// calls the cross-encoder client, and returns the slice in CE order.
// Preserves pre-migration semantics:
//   - metadata.relativity is overwritten with the CE score (float64) —
//     downstream temporal decay + relativity threshold filter read this.
//   - metadata.cross_encoder_reranked = true is set on each reranked item.
//   - Items without a non-empty "memory" text field pass through unchanged
//     in original relative order after the reranked block.
//
// Text truncation (MaxCharsPerDoc) is applied inside rerank.Client using
// the value set in Config at construction time (server_init_search.go).
func rerankMemoryItems(
	ctx context.Context,
	client *rerank.Client,
	query string,
	items []map[string]any,
) []map[string]any {
	if len(items) == 0 {
		return items
	}

	docs := make([]rerank.Doc, 0, len(items))
	idxByID := make(map[string]int, len(items))
	for i, item := range items {
		text, _ := item["memory"].(string)
		if text == "" {
			continue
		}
		id := fmt.Sprintf("%d", i)
		docs = append(docs, rerank.Doc{ID: id, Text: text})
		idxByID[id] = i
	}
	if len(docs) == 0 {
		return items
	}

	// M10 Stream 6: bump live-call counter (regardless of why the live
	// path was reached — precompute miss, anchor without cache, or
	// MEMDB_CE_PRECOMPUTE=false).
	if mx := searchMx(); mx != nil && mx.CELiveCall != nil {
		mx.CELiveCall.Add(ctx, 1)
	}
	scored := client.Rerank(ctx, query, docs)

	seen := make(map[int]bool, len(scored))
	out := make([]map[string]any, 0, len(items))
	for _, s := range scored {
		origIdx, ok := idxByID[s.ID]
		if !ok {
			continue
		}
		if meta, ok2 := items[origIdx]["metadata"].(map[string]any); ok2 {
			meta["relativity"] = float64(s.Score)
			meta["cross_encoder_reranked"] = true
		}
		out = append(out, items[origIdx])
		seen[origIdx] = true
	}
	for i, item := range items {
		if !seen[i] {
			out = append(out, item)
		}
	}
	return out
}
