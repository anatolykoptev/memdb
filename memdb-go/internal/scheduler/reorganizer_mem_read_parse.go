package scheduler

// reorganizer_mem_read_parse.go — parse WorkingMemory properties JSON into
// structured mem_read inputs (texts, session/agent IDs) and dedup-candidate
// property parsing.

import (
	"encoding/json"
)

// extractWMInfo extracts texts, sessionID, agentID and property IDs from raw WM node rows.
func extractWMInfo(fullNodes []map[string]any) wmInfo {
	var info wmInfo
	for _, fn := range fullNodes {
		propsStr, _ := fn["properties"].(string)
		if propsStr == "" {
			continue
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsStr), &props); err != nil {
			continue
		}
		mem, _ := props["memory"].(string)
		id, _ := props["id"].(string)
		if mem == "" || id == "" {
			continue
		}
		info.texts = append(info.texts, mem)
		info.processedWMIDs = append(info.processedWMIDs, id)
		if info.sessionID == "" {
			info.sessionID, _ = props["session_id"].(string)
		}
		if info.agentID == "" {
			info.agentID, _ = props["agent_id"].(string)
		}
	}
	return info
}

// extractIDAndMemory parses a properties JSON blob to extract the id and memory fields.
func extractIDAndMemory(propertiesJSON string) (id, memory string) {
	var props map[string]any
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		return "", ""
	}
	id, _ = props["id"].(string)
	memory, _ = props["memory"].(string)
	return id, memory
}
