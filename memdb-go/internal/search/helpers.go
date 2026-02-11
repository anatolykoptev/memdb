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

// TrimSlice trims a slice to at most n items.
func TrimSlice(items []map[string]any, n int) []map[string]any {
	if len(items) > n {
		return items[:n]
	}
	return items
}
