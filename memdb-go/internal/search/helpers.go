// Package search — shared helper functions used by both REST and MCP search handlers.
package search

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

// MergedResult combines a VectorSearchResult with a merged score.
type MergedResult struct {
	ID         string
	Properties string
	Score      float64
	Embedding  []float32
}

// MergeVectorAndFulltext merges vector and fulltext results by node ID.
// Fulltext matches get a meaningful score boost — keyword overlap is a strong relevance signal.
func MergeVectorAndFulltext(vector, fulltext []db.VectorSearchResult) []MergedResult {
	byID := make(map[string]*MergedResult, len(vector)+len(fulltext))
	order := make([]string, 0, len(vector)+len(fulltext))

	for _, r := range vector {
		normalizedScore := (r.Score + 1.0) / 2.0
		if existing, ok := byID[r.ID]; ok {
			if normalizedScore > existing.Score {
				existing.Score = normalizedScore
			}
		} else {
			byID[r.ID] = &MergedResult{ID: r.ID, Properties: r.Properties, Score: normalizedScore, Embedding: r.Embedding}
			order = append(order, r.ID)
		}
	}

	for _, r := range fulltext {
		ftScore := r.Score * 0.5
		if existing, ok := byID[r.ID]; ok {
			existing.Score = existing.Score + ftScore*0.5
		} else {
			byID[r.ID] = &MergedResult{ID: r.ID, Properties: r.Properties, Score: ftScore}
			order = append(order, r.ID)
		}
	}

	results := make([]MergedResult, 0, len(byID))
	for _, id := range order {
		results = append(results, *byID[id])
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

// FormatMergedItems formats merged results and builds an embedding side-map.
func FormatMergedItems(merged []MergedResult, includeEmbedding bool) ([]map[string]any, map[string][]float32) {
	formatted := make([]map[string]any, 0, len(merged))
	embeddingByID := make(map[string][]float32, len(merged))

	for _, m := range merged {
		props := ParseProperties(m.Properties)
		if props == nil {
			continue
		}
		item := FormatMemoryItem(props, includeEmbedding)
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["relativity"] = m.Score
		}
		if m.Embedding != nil {
			if id, ok := item["id"].(string); ok {
				embeddingByID[id] = m.Embedding
			}
		}
		formatted = append(formatted, item)
	}
	return formatted, embeddingByID
}

// ParseProperties parses a JSON properties string into a map.
func ParseProperties(propsJSON string) map[string]any {
	if propsJSON == "" {
		return nil
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return nil
	}
	return props
}

// FilterByRelativity filters formatted items by their metadata.relativity score.
func FilterByRelativity(items []map[string]any, threshold float64) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		meta, _ := item["metadata"].(map[string]any)
		if meta == nil {
			continue
		}
		score, _ := meta["relativity"].(float64)
		if score >= threshold {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// FilterPrefByQuality rejects low-quality preference entries:
//   - Too short (< MinPrefLen chars) — conversation fragments like "Да", "Протестируй"
//   - Raw message leaks starting with "user:" or "assistant:" or "system:"
func FilterPrefByQuality(items []map[string]any) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		memory, _ := item["memory"].(string)
		memory = strings.TrimSpace(memory)
		if len(memory) < MinPrefLen {
			continue
		}
		lower := strings.ToLower(memory)
		if strings.HasPrefix(lower, "user:") || strings.HasPrefix(lower, "assistant:") || strings.HasPrefix(lower, "system:") {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// DedupByText removes exact text duplicates (case-insensitive trim).
func DedupByText(items []map[string]any) []map[string]any {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]bool, len(items))
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		mem, _ := item["memory"].(string)
		key := strings.TrimSpace(strings.ToLower(mem))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result
}

// FormatPrefResults converts Qdrant preference results to formatted memory items.
func FormatPrefResults(results []db.QdrantSearchResult) []map[string]any {
	formatted := make([]map[string]any, 0, len(results))
	seen := make(map[string]bool)

	for _, r := range results {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true

		memory, _ := r.Payload["memory"].(string)
		if memory == "" {
			memory, _ = r.Payload["memory_content"].(string)
		}
		if memory == "" {
			continue
		}

		metadata := make(map[string]any)
		for k, v := range r.Payload {
			metadata[k] = v
		}
		metadata["relativity"] = float64(r.Score)
		metadata["embedding"] = []any{}
		metadata["usage"] = []any{}
		metadata["id"] = r.ID
		metadata["memory"] = memory

		refID := r.ID
		if idx := strings.IndexByte(refID, '-'); idx > 0 {
			refID = refID[:idx]
		}
		refIDStr := "[" + refID + "]"
		metadata["ref_id"] = refIDStr

		item := map[string]any{
			"id":       r.ID,
			"ref_id":   refIDStr,
			"memory":   memory,
			"metadata": metadata,
		}
		formatted = append(formatted, item)
	}
	return formatted
}

// ToSearchItems converts formatted memory items to SearchItem slice for dedup.
func ToSearchItems(items []map[string]any, embeddingByID map[string][]float32, memType string) []SearchItem {
	result := make([]SearchItem, 0, len(items))
	for _, item := range items {
		memory, _ := item["memory"].(string)
		meta, _ := item["metadata"].(map[string]any)
		score := 0.0
		if meta != nil {
			if s, ok := meta["relativity"].(float64); ok {
				score = s
			}
		}
		var embedding []float32
		if id, ok := item["id"].(string); ok && embeddingByID != nil {
			embedding = embeddingByID[id]
		}
		result = append(result, SearchItem{
			Memory:     memory,
			Score:      score,
			MemType:    memType,
			BucketIdx:  0,
			Embedding:  embedding,
			Properties: item,
		})
	}
	return result
}

// FromSearchItems converts SearchItems back to formatted memory items.
func FromSearchItems(items []SearchItem) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, item.Properties)
	}
	return result
}

// StripEmbeddings removes embeddings from formatted items' metadata.
func StripEmbeddings(items []map[string]any) {
	for _, item := range items {
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["embedding"] = []any{}
		}
	}
}

// CapWorkingMemScores caps metadata.relativity for WorkingMemory items to
// prevent them from dominating text results with a perfect 1.00 score.
func CapWorkingMemScores(items []map[string]any) {
	for _, item := range items {
		meta, _ := item["metadata"].(map[string]any)
		if meta == nil {
			continue
		}
		mtype, _ := meta["memory_type"].(string)
		if mtype != "WorkingMemory" {
			continue
		}
		score, _ := meta["relativity"].(float64)
		if score > WorkingMemMaxScore {
			meta["relativity"] = WorkingMemMaxScore
		}
	}
}

// CrossSourceDedupByText removes items from secondary slices that have
// identical text (case-insensitive) to items already in the primary (text_mem).
// text_mem has the highest priority; duplicates are removed from skill/tool/pref.
func CrossSourceDedupByText(text, skill, tool, pref []map[string]any) ([]map[string]any, []map[string]any, []map[string]any) {
	seen := make(map[string]bool, len(text))
	for _, item := range text {
		mem, _ := item["memory"].(string)
		key := strings.TrimSpace(strings.ToLower(mem))
		if key != "" {
			seen[key] = true
		}
	}
	dedup := func(items []map[string]any) []map[string]any {
		result := make([]map[string]any, 0, len(items))
		for _, item := range items {
			mem, _ := item["memory"].(string)
			key := strings.TrimSpace(strings.ToLower(mem))
			if key != "" && seen[key] {
				continue
			}
			if key != "" {
				seen[key] = true
			}
			result = append(result, item)
		}
		return result
	}
	return dedup(skill), dedup(tool), dedup(pref)
}

// TrimSlice trims a slice to at most n items.
func TrimSlice(items []map[string]any, n int) []map[string]any {
	if len(items) > n {
		return items[:n]
	}
	return items
}

// MergeGraphIntoResults merges graph recall results into an existing merged slice.
// Graph results get a fixed score. If an ID already exists, the higher score is kept.
func MergeGraphIntoResults(existing []MergedResult, graph []db.GraphRecallResult) []MergedResult {
	byID := make(map[string]int, len(existing))
	for i, r := range existing {
		byID[r.ID] = i
	}

	for _, g := range graph {
		score := GraphKeyScore
		if g.TagOverlap > 0 {
			score = GraphTagBaseScore + GraphTagBonusPerTag*float64(g.TagOverlap)
			if score > GraphKeyScore {
				score = GraphKeyScore
			}
		}
		if idx, ok := byID[g.ID]; ok {
			if score > existing[idx].Score {
				existing[idx].Score = score
			}
		} else {
			existing = append(existing, MergedResult{
				ID:         g.ID,
				Properties: g.Properties,
				Score:      score,
			})
			byID[g.ID] = len(existing) - 1
		}
	}

	// Re-sort by score descending
	sort.SliceStable(existing, func(i, j int) bool {
		return existing[i].Score > existing[j].Score
	})
	return existing
}

// MergeWorkingMemIntoResults merges WorkingMemory items into an existing merged slice.
// Computes actual cosine similarity against queryVec for proper relevance scoring.
// Items without embeddings get WorkingMemBaseScore as a fallback.
// If an ID already exists, the higher score is kept.
func MergeWorkingMemIntoResults(existing []MergedResult, wm []db.VectorSearchResult, queryVec []float32) []MergedResult {
	byID := make(map[string]int, len(existing))
	for i, r := range existing {
		byID[r.ID] = i
	}

	for _, w := range wm {
		score := WorkingMemBaseScore
		if len(w.Embedding) > 0 && len(queryVec) > 0 {
			rawCosine := CosineSimilarity(queryVec, w.Embedding)
			score = (float64(rawCosine) + 1.0) / 2.0
			if score > WorkingMemMaxScore {
				score = WorkingMemMaxScore
			}
		}
		if idx, ok := byID[w.ID]; ok {
			if score > existing[idx].Score {
				existing[idx].Score = score
			}
			if len(w.Embedding) > 0 && len(existing[idx].Embedding) == 0 {
				existing[idx].Embedding = w.Embedding
			}
		} else {
			existing = append(existing, MergedResult{
				ID:         w.ID,
				Properties: w.Properties,
				Score:      score,
				Embedding:  w.Embedding,
			})
			byID[w.ID] = len(existing) - 1
		}
	}

	sort.SliceStable(existing, func(i, j int) bool {
		return existing[i].Score > existing[j].Score
	})
	return existing
}
