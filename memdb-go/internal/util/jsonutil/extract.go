// Package jsonutil provides small JSON helper functions shared across
// internal packages to avoid copy-paste drift.
package jsonutil

import "encoding/json"

// ExtractIDAndMemory parses a properties JSON blob (AGE vertex properties
// serialized as a JSON string) and returns the "id" and "memory" fields.
// Returns ("", "") on parse error or missing fields.
func ExtractIDAndMemory(propertiesJSON string) (id, memory string) {
	var props map[string]any
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		return "", ""
	}
	id, _ = props["id"].(string)
	memory, _ = props["memory"].(string)
	return id, memory
}
