package scheduler

// worker_parsers.go — pure parse helpers for scheduler message payloads.
// No Redis dependency. Covers: splitStreamKey, parseMemReadIDs, splitIDs,
// parsePrefConversation, parseFeedbackPayload, parseFeedbackMemoryIDs, indexOf.

import (
	"encoding/json"
	"strings"
)

// streamKeyParts holds parsed components of a scheduler stream key.
type streamKeyParts struct {
	userID string
	cubeID string
	label  string
}

// splitStreamKey parses "scheduler:messages:stream:v2.0:{user_id}:{cube_id}:{label}".
func splitStreamKey(key string) streamKeyParts {
	// prefix has 4 colon-separated segments: scheduler:messages:stream:v2.0
	const prefixSegments = 4
	pos := 0
	for i := 0; i < prefixSegments; i++ {
		next := indexOf(key, ':', pos)
		if next < 0 {
			return streamKeyParts{}
		}
		pos = next + 1
	}
	// remaining: {user_id}:{cube_id}:{label}
	rest := key[pos:]
	p1 := indexOf(rest, ':', 0)
	if p1 < 0 {
		return streamKeyParts{}
	}
	userID := rest[:p1]
	rest2 := rest[p1+1:]
	p2 := indexOf(rest2, ':', 0)
	if p2 < 0 {
		return streamKeyParts{userID: userID, cubeID: rest2}
	}
	return streamKeyParts{
		userID: userID,
		cubeID: rest2[:p2],
		label:  rest2[p2+1:],
	}
}

// parseMemReadIDs parses WorkingMemory node IDs from a mem_read message content.
// Python sends either:
//   - comma-separated string: "uuid1,uuid2,uuid3"
//   - JSON: {"memory_ids": ["uuid1","uuid2"]} or {"memory_ids_str": "uuid1,uuid2"}
//
// Returns nil if the content is empty or unparseable.
func parseMemReadIDs(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	// Try JSON array first (Go async add sends: ["uuid1","uuid2"]).
	if content[0] == '[' {
		var ids []string
		if err := json.Unmarshal([]byte(content), &ids); err == nil && len(ids) > 0 {
			return ids
		}
	}
	// Try JSON object (Python sends: {"memory_ids": ["uuid1","uuid2"]}).
	if content[0] == '{' {
		var payload struct {
			MemoryIDs    []string `json:"memory_ids"`
			MemoryIDsStr string   `json:"memory_ids_str"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			if len(payload.MemoryIDs) > 0 {
				return payload.MemoryIDs
			}
			if payload.MemoryIDsStr != "" {
				return splitIDs(payload.MemoryIDsStr)
			}
		}
	}
	// Fallback: comma-separated string.
	return splitIDs(content)
}

// splitIDs splits a comma-separated string of IDs, trimming whitespace.
func splitIDs(s string) []string {
	parts := strings.Split(s, ",")
	var ids []string
	for _, p := range parts {
		if id := strings.TrimSpace(p); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// parsePrefConversation extracts a conversation string from a pref_add message.
// Python sends either plain conversation text or JSON with a "history" or "content" field.
// Returns the raw content if it's not JSON (treat as plain text).
func parsePrefConversation(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if content[0] == '{' {
		var payload struct {
			History []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"history"`
			Conversation string `json:"conversation"`
			Content      string `json:"content"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			if payload.Conversation != "" {
				return payload.Conversation
			}
			if payload.Content != "" {
				return payload.Content
			}
			if len(payload.History) > 0 {
				var sb strings.Builder
				for _, msg := range payload.History {
					sb.WriteString(msg.Role)
					sb.WriteString(": ")
					sb.WriteString(msg.Content)
					sb.WriteString("\n")
				}
				return sb.String()
			}
		}
	}
	return content
}

// parseFeedbackPayload extracts retrieved_memory_ids and feedback_content from
// a mem_feedback message. The Python scheduler sends:
//
//	{"session_id":"...","retrieved_memory_ids":["uuid1","uuid2"],"feedback_content":"..."}
//
// Returns (nil, "") if the JSON is malformed or the field is absent.
func parseFeedbackPayload(content string) (ids []string, feedbackContent string) {
	var payload struct {
		RetrievedMemoryIDs []string `json:"retrieved_memory_ids"`
		FeedbackContent    string   `json:"feedback_content"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, ""
	}
	return payload.RetrievedMemoryIDs, payload.FeedbackContent
}

// parseFeedbackMemoryIDs extracts only the retrieved_memory_ids (kept for tests).
func parseFeedbackMemoryIDs(content string) []string {
	ids, _ := parseFeedbackPayload(content)
	return ids
}

// indexOf returns the index of byte b in s starting at from, or -1.
func indexOf(s string, b byte, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
