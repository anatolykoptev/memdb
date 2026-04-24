package handlers

// add_props.go — memory node property construction and source serialization.
// Responsibility: build the JSONB properties map for a memory node and
// serialize/deserialize sources. No I/O, no DB calls.

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// memoryNodeProps holds the parameters for building a memory node's property map.
// All fields correspond 1:1 to Python's memory properties format.
type memoryNodeProps struct {
	ID         string
	Memory     string
	MemoryType string // "LongTermMemory" | "UserMemory" | "PreferenceMemory" | "WorkingMemory"
	UserName   string // cube partition key (upstream MemOS convention; holds cube_id)
	UserID     string // person identity — Phase 2 split from cube_id
	AgentID    string // agent scope
	SessionID  string
	Mode       string // "fast" | "fine" — becomes tag "mode:<mode>"
	Now        string // updated_at timestamp (always current)
	CreatedAt  string // created_at timestamp (may differ for valid_at from LLM)
	Info       map[string]any
	CustomTags []string
	Sources    []map[string]any
	Background string // "[working_binding:<wm_id>]" for LTM nodes; "" for WM nodes

	// D6: verbatim original utterance, preserved alongside the resolved form in
	// Memory. Empty string means no raw text was provided by the extractor
	// (e.g. non-LLM callers like manual add).
	RawText string
	// D8: one of the 22-key MemOS preference taxonomy. Only set for PreferenceMemory
	// entries; empty otherwise.
	PreferenceCategory string
}

// buildNodeProps constructs the JSONB properties dict matching the Python format.
func buildNodeProps(p memoryNodeProps) map[string]any {
	tags := []string{"mode:" + p.Mode}
	tags = append(tags, p.CustomTags...)

	props := map[string]any{
		"id":          p.ID,
		"memory":      p.Memory,
		"memory_type": p.MemoryType,
		"status":      "activated",
		// user_name is the cube partition key (upstream MemOS convention; populated from cube_id)
		"user_name":         p.UserName,
		"user_id":           p.UserID,
		"agent_id":          p.AgentID,
		"session_id":        p.SessionID,
		"created_at":        p.CreatedAt,
		"updated_at":        p.Now,
		"delete_time":       "",
		"delete_record_id":  "",
		"tags":              tags,
		"key":               "",
		"usage":             []string{},
		"sources":           serializeSources(p.Sources),
		"background":        p.Background,
		"confidence":        0.99,
		"type":              "fact",
		"info":              p.Info,
		"graph_id":          uuid.New().String(),
		"importance_score":  1.0,
		"retrieval_count":   0,
		"last_retrieved_at": "",
		// D3 hierarchy defaults — new memories start as 'raw' (direct extraction).
		// TreeManager promotes clusters to 'episodic' and themes to 'semantic',
		// populating parent_memory_id on children at promotion time.
		"hierarchy_level":  "raw",
		"parent_memory_id": nil,
	}
	// D6: include raw_text only when provided — keeps payload lean for callers
	// that don't pass it (manual add, legacy paths).
	if p.RawText != "" {
		props["raw_text"] = p.RawText
	}
	// D8: preference_category populated only for PreferenceMemory entries.
	// Null-by-default keeps JSON size small and makes property introspection
	// explicit about which rows are categorised.
	if p.PreferenceCategory != "" {
		props["preference_category"] = p.PreferenceCategory
	}
	return props
}

// buildMemoryProperties is a convenience wrapper for fast-mode (created_at == updated_at).
// userName is the cube partition key; userID is the person identity (Phase 2 split).
func buildMemoryProperties(
	id, memory, memoryType, userName, userID, agentID, sessionID, timestamp string,
	info map[string]any, customTags []string,
	sources []map[string]any, background string,
) map[string]any {
	return buildNodeProps(memoryNodeProps{
		ID:         id,
		Memory:     memory,
		MemoryType: memoryType,
		UserName:   userName,
		UserID:     userID,
		AgentID:    agentID,
		SessionID:  sessionID,
		Mode:       modeFast,
		Now:        timestamp,
		CreatedAt:  timestamp,
		Info:       info,
		CustomTags: customTags,
		Sources:    sources,
		Background: background,
	})
}

// buildMemoryPropertiesAt is a convenience wrapper for fine-mode with a separate
// createdAt (from LLM-provided valid_at) and now (actual insert time).
// userName is the cube partition key; userID is the person identity (Phase 2 split).
func buildMemoryPropertiesAt(
	id, memory, memoryType, userName, userID, agentID, sessionID, now, createdAt string,
	info map[string]any, customTags []string,
	sources []map[string]any, background string,
) map[string]any {
	return buildNodeProps(memoryNodeProps{
		ID:         id,
		Memory:     memory,
		MemoryType: memoryType,
		UserName:   userName,
		UserID:     userID,
		AgentID:    agentID,
		SessionID:  sessionID,
		Mode:       modeFine,
		Now:        now,
		CreatedAt:  createdAt,
		Info:       info,
		CustomTags: customTags,
		Sources:    sources,
		Background: background,
	})
}

// buildSourcesFromMessages creates a sources slice from the raw messages list.
func buildSourcesFromMessages(messages []chatMessage) []map[string]any {
	sources := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = time.Now().UTC().Format("2006-01-02T15:04:05")
		}
		sources = append(sources, map[string]any{
			"role":      msg.Role,
			"content":   msg.Content,
			"chat_time": chatTime,
		})
	}
	return sources
}

// serializeSources converts each source map to a JSON string, matching Python's format
// where each element in the sources array is a JSON-serialized string.
func serializeSources(sources []map[string]any) []string {
	if len(sources) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(sources))
	for _, src := range sources {
		b, err := json.Marshal(src)
		if err != nil {
			continue
		}
		result = append(result, string(b))
	}
	return result
}

// extractIDAndMemory parses a properties JSON blob to extract the id and memory fields.
// Used when building LLM candidate lists from vector search results.
func extractIDAndMemory(propertiesJSON string) (id, memory string) {
	var props map[string]any
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		return "", ""
	}
	id, _ = props["id"].(string)
	memory, _ = props["memory"].(string)
	return id, memory
}
