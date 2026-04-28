// Package search — adapter between the search pipeline's map[string]any
// memory items and the rerank.Reranker contract.
//
// The rerank package MUST NOT import internal/search (would cycle), so
// the adapter lives here. mapItem wraps a single map and implements
// rerank.Item. adaptItems / unadaptItems are O(n) shims used by
// postProcessResults to lift a slice across the boundary.
//
// This is the ONE allowed file outside rerank/ that mutates
// metadata.relativity / metadata.<flag>_reranked — see the M11/R3 spec
// "single-contract invariant".
package search

import (
	"github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
)

// mapItem adapts a formatted memory map to rerank.Item.
//
// Score is canonically metadata.relativity (float64); SetMeta lazily
// allocates the metadata sub-map if absent so strategies don't have to
// nil-check.
type mapItem struct {
	m map[string]any
}

// ID implements rerank.Item.
func (it mapItem) ID() string {
	id, _ := it.m["id"].(string)
	return id
}

// Score implements rerank.Item — reads metadata.relativity.
func (it mapItem) Score() float64 {
	meta, _ := it.m["metadata"].(map[string]any)
	if meta == nil {
		return 0
	}
	v, _ := meta["relativity"].(float64)
	return v
}

// SetScore implements rerank.Item — writes metadata.relativity, allocating
// metadata if needed.
func (it mapItem) SetScore(v float64) {
	meta, _ := it.m["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		it.m["metadata"] = meta
	}
	meta["relativity"] = v
}

// SetMeta implements rerank.Item — sets a metadata key (e.g. *_reranked
// flags), allocating metadata if needed.
func (it mapItem) SetMeta(key string, val any) {
	meta, _ := it.m["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		it.m["metadata"] = meta
	}
	meta[key] = val
}

// GetMeta implements rerank.Item.
func (it mapItem) GetMeta(key string) (any, bool) {
	meta, _ := it.m["metadata"].(map[string]any)
	if meta == nil {
		return nil, false
	}
	v, ok := meta[key]
	return v, ok
}

// EmbeddingText implements rerank.Item — returns the "memory" field used
// by CE / LLM strategies as the document text. Empty string when absent.
func (it mapItem) EmbeddingText() string {
	t, _ := it.m["memory"].(string)
	return t
}

// adaptItems wraps every map in items as a rerank.Item. The returned
// slice shares the underlying maps — mutations on the rerank side are
// visible to the original slice.
func adaptItems(items []map[string]any) []rerank.Item {
	out := make([]rerank.Item, len(items))
	for i, m := range items {
		out[i] = mapItem{m: m}
	}
	return out
}

// unadaptItems unwraps rerank.Item back to the underlying maps. Order is
// preserved — strategies are expected to return the slice in the desired
// final order.
func unadaptItems(items []rerank.Item) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		mi, ok := it.(mapItem)
		if !ok {
			// Defensive: a non-mapItem implementation slipped in. We
			// have no way to recover the underlying map, so return nil
			// for that slot. Should never happen in practice — every
			// item in the chain originates from adaptItems.
			continue
		}
		out[i] = mi.m
	}
	return out
}
