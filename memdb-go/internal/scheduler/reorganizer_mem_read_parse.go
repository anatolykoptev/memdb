package scheduler

// reorganizer_mem_read_parse.go — parse WorkingMemory properties JSON into
// structured mem_read inputs (texts, session/agent IDs) and dedup-candidate
// property parsing.

import (
	"encoding/json"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/jsonutil"
)

// extractWMInfo extracts texts, sessionID, agentID, property IDs and the
// max in-conversation date across raw WM node rows. observationDate is
// computed as max(observation_date|chat_time)[0:10] across rows; "" when
// no row carries a usable date.
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
		// M12.1: derive observationDate from source rows. observation_date
		// wins; chat_time is the legacy fallback. Take only YYYY-MM-DD.
		if d := pickWMDate(props, "observation_date", "chat_time"); d != "" {
			if len(d) > 10 {
				d = d[:10]
			}
			if d > info.observationDate {
				info.observationDate = d
			}
		}
	}
	return info
}

// pickWMDate returns the first non-empty string field at the top level of
// props for any of the given keys.
func pickWMDate(props map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := props[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if ok && s != "" {
			return s
		}
	}
	return ""
}

// extractIDAndMemory parses a properties JSON blob to extract the id and memory fields.
func extractIDAndMemory(propertiesJSON string) (id, memory string) {
	return jsonutil.ExtractIDAndMemory(propertiesJSON)
}
