// Package search provides fulltext search utilities and response formatting
// for memory search results. The response types match the Python API exactly
// (see formatters_handler.py, single_cube.py, composite_cube.py).
package search

import "strings"

// MemoryBucket holds a group of memories for one cube_id.
// Matches Python's {"cube_id": ..., "memories": [...], "total_nodes": N} shape
// used in post_process_textual_mem and post_process_pref_mem.
type MemoryBucket struct {
	CubeID     string           `json:"cube_id"`
	Memories   []map[string]any `json:"memories"`
	TotalNodes int              `json:"total_nodes"`
}

// SearchResult is the top-level search response data.
// Matches the MOSSearchResult TypedDict + additional fields added by
// post_process_pref_mem (pref_note, pref_string) in the Python API.
type SearchResult struct {
	TextMem    []MemoryBucket `json:"text_mem"`
	SkillMem   []MemoryBucket `json:"skill_mem"`
	PrefMem    []MemoryBucket `json:"pref_mem"`
	ToolMem    []MemoryBucket `json:"tool_mem"`
	ActMem     []any          `json:"act_mem"`
	ParaMem    []any          `json:"para_mem"`
	PrefNote   string         `json:"pref_note"`
	PrefString string         `json:"pref_string"`
}

// NewEmptySearchResult creates a SearchResult with all empty slices (not nil).
// This ensures JSON serialization produces [] rather than null, matching the
// Python API behavior where every field is initialized to [].
func NewEmptySearchResult() *SearchResult {
	return &SearchResult{
		TextMem:    []MemoryBucket{},
		SkillMem:   []MemoryBucket{},
		PrefMem:    []MemoryBucket{},
		ToolMem:    []MemoryBucket{},
		ActMem:     []any{},
		ParaMem:    []any{},
		PrefNote:   "",
		PrefString: "",
	}
}

// FormatMemoryItem formats a single memory item for API response.
// This is the Go equivalent of formatters_handler.py:format_memory_item().
//
// It expects props to be a flat map of properties from the DB (e.g. "id",
// "memory", "memory_type", "embedding", "sources", etc.). It builds a
// nested structure with top-level "id", "ref_id", "memory", and "metadata"
// keys, where metadata contains all original properties plus ref_id, id,
// memory, and cleared embedding/usage fields.
//
// If includeEmbedding is false, metadata["embedding"] is set to an empty
// slice (matching Python's `memory["metadata"]["embedding"] = []`).
func FormatMemoryItem(props map[string]any, includeEmbedding bool) map[string]any {
	// Extract the memory ID.
	memoryID, _ := props["id"].(string)

	// Build ref_id: "[<first segment before '-'>]"
	refID := "[" + firstSegment(memoryID) + "]"

	// Build metadata from all original properties.
	metadata := make(map[string]any, len(props)+4)
	for k, v := range props {
		metadata[k] = v
	}

	// Overlay standard fields (matches Python lines 56-63 in formatters_handler.py).
	if !includeEmbedding {
		metadata["embedding"] = []any{}
	}
	// Python: save_sources=True by default, so we keep sources.
	// Clear usage array (always cleared in Python).
	metadata["usage"] = []any{}
	metadata["ref_id"] = refID
	metadata["id"] = memoryID
	metadata["memory"] = props["memory"]

	// Build the top-level item.
	memory, _ := props["memory"].(string)
	return map[string]any{
		"id":       memoryID,
		"ref_id":   refID,
		"memory":   memory,
		"metadata": metadata,
	}
}

// BuildSearchResult creates a SearchResult with proper buckets from
// pre-formatted memory items. Each slice of items becomes a single
// MemoryBucket under the corresponding field.
//
// textMems covers fact-type memories (LongTermMemory, UserMemory,
// WorkingMemory, OuterMemory) which go into text_mem.
// skillMems go into skill_mem, prefMems go into pref_mem.
// tool_mem, act_mem, and para_mem are always empty (they require
// activation/parametric memory which is not implemented in Go).
func BuildSearchResult(textMems, skillMems, prefMems []map[string]any, cubeID string) *SearchResult {
	result := NewEmptySearchResult()

	// Ensure non-nil slices for JSON serialization.
	if textMems == nil {
		textMems = []map[string]any{}
	}
	if skillMems == nil {
		skillMems = []map[string]any{}
	}
	if prefMems == nil {
		prefMems = []map[string]any{}
	}

	result.TextMem = []MemoryBucket{{
		CubeID:     cubeID,
		Memories:   textMems,
		TotalNodes: len(textMems),
	}}

	result.SkillMem = []MemoryBucket{{
		CubeID:     cubeID,
		Memories:   skillMems,
		TotalNodes: len(skillMems),
	}}

	result.PrefMem = []MemoryBucket{{
		CubeID:     cubeID,
		Memories:   prefMems,
		TotalNodes: len(prefMems),
	}}

	// tool_mem gets an empty bucket for the cube (matches Python behavior
	// where post_process_textual_mem always appends a tool_mem bucket).
	result.ToolMem = []MemoryBucket{{
		CubeID:     cubeID,
		Memories:   []map[string]any{},
		TotalNodes: 0,
	}}

	return result
}

// SplitByMemoryType splits formatted memories by metadata.memory_type.
// This matches formatters_handler.py:post_process_textual_mem() which
// partitions text_formatted_mem into fact_mem, tool_mem, and skill_mem.
//
// Classification:
//   - fact: WorkingMemory, LongTermMemory, UserMemory, OuterMemory
//   - tool: ToolSchemaMemory, ToolTrajectoryMemory
//   - skill: SkillMemory
//
// Items with unrecognized memory_type are placed in factMem.
func SplitByMemoryType(formatted []map[string]any) (factMem, toolMem, skillMem []map[string]any) {
	factMem = make([]map[string]any, 0, len(formatted))
	toolMem = make([]map[string]any, 0)
	skillMem = make([]map[string]any, 0)

	for _, item := range formatted {
		memType := extractMemoryType(item)
		switch memType {
		case "SkillMemory":
			skillMem = append(skillMem, item)
		case "ToolSchemaMemory", "ToolTrajectoryMemory":
			toolMem = append(toolMem, item)
		default:
			// WorkingMemory, LongTermMemory, UserMemory, OuterMemory,
			// and any unrecognized types go to factMem.
			factMem = append(factMem, item)
		}
	}

	return factMem, toolMem, skillMem
}

// firstSegment returns the portion of id before the first "-".
// If id contains no "-", the entire id is returned.
func firstSegment(id string) string {
	if idx := strings.IndexByte(id, '-'); idx >= 0 {
		return id[:idx]
	}
	return id
}

// extractMemoryType reads metadata.memory_type from a formatted memory item.
// Returns empty string if the field is missing or not a string.
func extractMemoryType(item map[string]any) string {
	meta, ok := item["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	mt, _ := meta["memory_type"].(string)
	return mt
}
