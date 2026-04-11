package filter

import (
	"encoding/json"
	"testing"
)

// FuzzParse feeds arbitrary JSON into Parse. The parser must never panic —
// any malformed shape is expected to return a typed error. The fuzzer also
// calls BuildAGEWhereConditions on successful parses to check the SQL
// renderer is crash-proof on any value the parser accepts.
func FuzzParse(f *testing.F) {
	seeds := []string{
		`{"user_id": "alice"}`,
		`{"and": [{"type": "text"}, {"created_at": {"gt": "2026-01-01"}}]}`,
		`{"or": [{"tags": {"contains": "python"}}]}`,
		`{"file_ids": {"in": ["a", "b", "c"]}}`,
		`{"memory": {"like": "foo%bar_"}}`,
		`{"info.custom": 42}`,
		`{"confidence": {"gte": 0.75}}`,
		`{}`,
		`{"tags": "'; DROP TABLE memories; --"}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return
		}
		parsed, err := Parse(m)
		if err != nil {
			return
		}
		// Renderer must also survive; result is ignored.
		_, _ = BuildAGEWhereConditions(parsed)
	})
}
