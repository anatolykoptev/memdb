// Package search — cosine reranking for search results.
// Port of Python's CosineLocalReranker from cosine_local.py.
package search

import (
	"math"
	"sort"
	"time"
)

// ReRankByCosine re-scores items using cosine similarity between queryVec
// and each item's stored embedding. Items without embeddings keep their
// original score.
//
// This replaces the compressed PolarDB scores (0.88-0.92 range) with direct
// cosine similarity (0.2-0.95 range), giving much better discrimination
// for relativity threshold filtering.
func ReRankByCosine(queryVec []float32, items []map[string]any, embeddingsByID map[string][]float32) []map[string]any {
	if len(queryVec) == 0 || len(items) == 0 {
		return items
	}

	for _, item := range items {
		id, _ := item["id"].(string)
		emb := embeddingsByID[id]
		if len(emb) == 0 {
			continue
		}

		// Normalize to [0,1] range matching PolarDB's score: (raw_cosine + 1) / 2
		cosineSim := (float64(CosineSimilarity(queryVec, emb)) + 1.0) / 2.0
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["relativity"] = cosineSim
		}
	}

	// Sort by new score descending
	sort.SliceStable(items, func(i, j int) bool {
		scoreI := getRelativity(items[i])
		scoreJ := getRelativity(items[j])
		return scoreI > scoreJ
	})

	return items
}

// getRelativity extracts metadata.relativity from a formatted memory item.
func getRelativity(item map[string]any) float64 {
	meta, _ := item["metadata"].(map[string]any)
	if meta == nil {
		return 0
	}
	score, _ := meta["relativity"].(float64)
	return score
}

// ApplyTemporalDecay applies time-based recency scoring using a weighted combination:
//
//	final_score = SemanticWeight * cosine_score + RecencyWeight * recency_score
//	recency_score = exp(-alpha * days_since_last_access)  clamped to [0,1]
//
// Timestamp priority (matches LangChain/MemOS best practice):
//  1. last_accessed_at — reflects actual usage recency
//  2. updated_at — reflects content freshness
//  3. created_at — fallback
//
// Memories older than MaxDecayAgeDays get recency_score=0 but keep their
// semantic score (they are not removed, just ranked lower). This matches
// MemOS max_memory_age_days behavior.
//
// WorkingMemory items are exempt — session context should not decay.
// Items without any parseable timestamp keep their original score unchanged.
func ApplyTemporalDecay(items []map[string]any, now time.Time, alpha float64) []map[string]any {
	if alpha <= 0 || len(items) == 0 {
		return items
	}
	for _, item := range items {
		applyDecayToItem(item, now, alpha)
	}
	return items
}

// applyDecayToItem applies temporal decay to a single item's metadata in place.
func applyDecayToItem(item map[string]any, now time.Time, alpha float64) {
	meta, ok := item["metadata"].(map[string]any)
	if !ok || meta == nil {
		return
	}
	// WorkingMemory is session context — recency is already its purpose.
	if mt, _ := meta["memory_type"].(string); mt == "WorkingMemory" {
		return
	}

	refTime, ok := resolveRefTimestamp(meta)
	if !ok {
		return // no timestamp — leave score unchanged
	}

	days := now.Sub(refTime).Hours() / 24.0
	if days < 0 {
		days = 0
	}

	// Memories beyond MaxDecayAgeDays get recency=0 (semantic still counts).
	var recency float64
	if days <= MaxDecayAgeDays {
		recency = math.Exp(-alpha * days)
	}

	// Weighted combination: semantic relevance + recency tiebreaker.
	// Matches MemOS pattern (0.6 sem + 0.3 rec + 0.1 imp) but without
	// importance score (not yet stored). We use 0.75/0.25 split.
	if cosine, ok := meta["relativity"].(float64); ok {
		meta["relativity"] = DecaySemanticWeight*cosine + DecayRecencyWeight*recency
	}
}

// resolveRefTimestamp returns the most relevant timestamp from metadata.
// Priority: last_accessed_at > updated_at > created_at.
func resolveRefTimestamp(meta map[string]any) (time.Time, bool) {
	for _, field := range []string{"last_accessed_at", "updated_at", "created_at"} {
		if t, ok := parseTimestamp(meta, field); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseTimestamp extracts and parses a named timestamp field from metadata.
// Supports ISO 8601 formats stored as string.
func parseTimestamp(meta map[string]any, field string) (time.Time, bool) {
	raw, _ := meta[field].(string)
	if raw == "" {
		return time.Time{}, false
	}
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
