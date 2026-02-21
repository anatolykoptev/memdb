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
	MemoryType string // "LongTermMemory" | "UserMemory" | "WorkingMemory"
	UserName   string // also used as user_id
	AgentID    string // agent scope
	SessionID  string
	Mode       string // "fast" | "fine" — becomes tag "mode:<mode>"
	Now        string // updated_at timestamp (always current)
	CreatedAt  string // created_at timestamp (may differ for valid_at from LLM)
	Info       map[string]any
	CustomTags []string
	Sources    []map[string]any
	Background string // "[working_binding:<wm_id>]" for LTM nodes; "" for WM nodes
}

// buildNodeProps constructs the JSONB properties dict matching the Python format.
func buildNodeProps(p memoryNodeProps) map[string]any {
	tags := []string{"mode:" + p.Mode}
	tags = append(tags, p.CustomTags...)

	return map[string]any{
		"id":               p.ID,
		"memory":           p.Memory,
		"memory_type":      p.MemoryType,
		"status":           "activated",
		"user_name":        p.UserName,
		"user_id":          p.UserName,
		"agent_id":         p.AgentID,
		"session_id":       p.SessionID,
		"created_at":       p.CreatedAt,
		"updated_at":       p.Now,
		"delete_time":      "",
		"delete_record_id": "",
		"tags":             tags,
		"key":              "",
		"usage":            []string{},
		"sources":          serializeSources(p.Sources),
		"background":       p.Background,
		"confidence":        0.99,
		"type":              "fact",
		"info":              p.Info,
		"graph_id":          uuid.New().String(),
		"importance_score":  1.0,
		"retrieval_count":   0,
		"last_retrieved_at": "",
	}
}

// buildMemoryProperties is a convenience wrapper for fast-mode (created_at == updated_at).
func buildMemoryProperties(
	id, memory, memoryType, userName, agentID, sessionID, timestamp string,
	info map[string]any, customTags []string,
	sources []map[string]any, background string,
) map[string]any {
	return buildNodeProps(memoryNodeProps{
		ID:         id,
		Memory:     memory,
		MemoryType: memoryType,
		UserName:   userName,
		AgentID:    agentID,
		SessionID:  sessionID,
		Mode:       "fast",
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
func buildMemoryPropertiesAt(
	id, memory, memoryType, userName, agentID, sessionID, now, createdAt string,
	info map[string]any, customTags []string,
	sources []map[string]any, background string,
) map[string]any {
	return buildNodeProps(memoryNodeProps{
		ID:         id,
		Memory:     memory,
		MemoryType: memoryType,
		UserName:   userName,
		AgentID:    agentID,
		SessionID:  sessionID,
		Mode:       "fine",
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
