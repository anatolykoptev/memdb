package handlers

// memory_format.go — shared formatting helpers for memory HTTP handlers.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// prefCollectionsGetMemory lists the Qdrant collections for preference memory.
var prefCollectionsGetMemory = []string{"explicit_preference", "implicit_preference"}

// formatPrefResultExtraFields is the number of extra fields added to each pref metadata map
// beyond the payload fields (embedding, usage, id, memory).
const formatPrefResultExtraFields = 3

// formatMemoryBucket formats PolarDB results into a MemoryBucket with parsed properties.
// Each memory item gets the full FormatMemoryItem treatment matching the Python API.
func formatMemoryBucket(results []map[string]any, cubeID string, total int) search.MemoryBucket {
	memories := make([]map[string]any, 0, len(results))
	for _, result := range results {
		if propsStr, ok := result["properties"].(string); ok {
			var props map[string]any
			if json.Unmarshal([]byte(propsStr), &props) == nil {
				item := search.FormatMemoryItem(props, false)
				memories = append(memories, item)
			}
		}
	}
	return search.MemoryBucket{
		CubeID:     cubeID,
		Memories:   memories,
		TotalNodes: total,
	}
}

// scrollPreferences scrolls Qdrant preference collections for a user and formats results.
func (h *Handler) scrollPreferences(ctx context.Context, userID string, limit int) []map[string]any {
	var allItems []map[string]any
	seen := make(map[string]bool)

	for _, coll := range prefCollectionsGetMemory {
		results, err := h.qdrant.ScrollByUserID(ctx, coll, userID, limit)
		if err != nil {
			h.logger.Debug("pref scroll failed",
				slog.String("collection", coll),
				slog.Any("error", err),
			)
			continue
		}
		for _, r := range results {
			item, ok := formatPrefResult(r.ID, r.Payload, seen)
			if !ok {
				continue
			}
			seen[r.ID] = true
			allItems = append(allItems, item)
		}
	}

	if allItems == nil {
		allItems = []map[string]any{}
	}
	return allItems
}

// formatPrefResult formats a single Qdrant preference result for the API response.
// Returns (item, true) when valid; (nil, false) when the entry should be skipped.
func formatPrefResult(id string, payload map[string]any, seen map[string]bool) (map[string]any, bool) {
	if seen[id] {
		return nil, false
	}
	memory, _ := payload["memory"].(string)
	if memory == "" {
		memory, _ = payload["memory_content"].(string)
	}
	if memory == "" {
		return nil, false
	}

	metadata := make(map[string]any, len(payload)+formatPrefResultExtraFields)
	for k, v := range payload {
		metadata[k] = v
	}
	metadata["embedding"] = []any{}
	metadata["usage"] = []any{}
	metadata["id"] = id
	metadata["memory"] = memory

	refID := id
	if idx := strings.IndexByte(refID, '-'); idx > 0 {
		refID = refID[:idx]
	}
	refIDStr := "[" + refID + "]"
	metadata["ref_id"] = refIDStr

	return map[string]any{
		"id":       id,
		"ref_id":   refIDStr,
		"memory":   memory,
		"metadata": metadata,
	}, true
}
