// Package handlers — deprecated field normalization.
// Converts deprecated API fields to their current equivalents before proxying
// to the Python backend. This reduces Python-side Pydantic deprecation log spam
// and prepares for Phase 2 native handlers.
package handlers

import "encoding/json"

// normalizeSearch converts deprecated fields in /product/search requests.
//   - mem_cube_id (string) → readable_cube_ids ([]string)
func normalizeSearch(body []byte) []byte {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body
	}

	if migrateCubeID(m, "readable_cube_ids") {
		return mustMarshal(m, body)
	}
	return body
}

// normalizeAdd converts deprecated fields in /product/add requests.
//   - mem_cube_id (string) → writable_cube_ids ([]string)
//   - memory_content (string) → appended to messages as {role:"user", content:...}
//   - source (string) → info.source
func normalizeAdd(body []byte) []byte {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body
	}

	changed := migrateCubeID(m, "writable_cube_ids")
	changed = migrateMemoryContent(m) || changed
	changed = migrateSource(m) || changed

	if changed {
		return mustMarshal(m, body)
	}
	return body
}

// normalizeChatComplete converts deprecated fields in /product/chat/complete requests.
//   - mem_cube_id (string) → readable_cube_ids + writable_cube_ids ([]string)
func normalizeChatComplete(body []byte) []byte {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body
	}

	if cubeID, ok := popString(m, "mem_cube_id"); ok {
		ids := []string{cubeID}
		if _, exists := m["readable_cube_ids"]; !exists {
			m["readable_cube_ids"] = ids
		}
		if _, exists := m["writable_cube_ids"]; !exists {
			m["writable_cube_ids"] = ids
		}
		return mustMarshal(m, body)
	}
	return body
}

// --- helpers ---

// migrateCubeID moves mem_cube_id → target field (as []string). Returns true if changed.
func migrateCubeID(m map[string]any, target string) bool {
	cubeID, ok := popString(m, "mem_cube_id")
	if !ok {
		return false
	}
	if _, exists := m[target]; !exists {
		m[target] = []string{cubeID}
	}
	return true
}

// migrateMemoryContent moves memory_content → messages[{role:"user", content:...}].
func migrateMemoryContent(m map[string]any) bool {
	content, ok := popString(m, "memory_content")
	if !ok {
		return false
	}

	msg := map[string]string{"role": "user", "content": content}
	if existing, ok := m["messages"].([]any); ok {
		m["messages"] = append(existing, msg)
	} else {
		m["messages"] = []any{msg}
	}
	return true
}

// migrateSource moves source → info.source.
func migrateSource(m map[string]any) bool {
	source, ok := popString(m, "source")
	if !ok {
		return false
	}

	info, _ := m["info"].(map[string]any)
	if info == nil {
		info = make(map[string]any)
	}
	if _, exists := info["source"]; !exists {
		info["source"] = source
	}
	m["info"] = info
	return true
}

// popString extracts a string value from the map and deletes the key.
func popString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	delete(m, key)
	return s, true
}

// mustMarshal re-encodes the map, returning original body on error.
func mustMarshal(m map[string]any, fallback []byte) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return fallback
	}
	return b
}
