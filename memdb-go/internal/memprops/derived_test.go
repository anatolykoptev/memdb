package memprops

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildDerivedMemoryProps_RequiresObservationDate is the M12.1 invariant test:
// derived memories MUST carry observation_date or the helper rejects the call.
//
// Why: 15 derived-write paths historically hand-rolled props maps without
// observation_date. The LoCoMo cat2 today-leak (4% direct + N% via "discussed"
// memories) traced to this single missing key. Failing loud at the helper
// boundary makes a regression impossible to land silently.
func TestBuildDerivedMemoryProps_RequiresObservationDate(t *testing.T) {
	cases := []struct {
		name string
		obs  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"tab only", "\t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildDerivedMemoryProps(DerivedMemoryProps{
				ID: "x", Memory: "m", MemoryType: "EpisodicMemory",
				UserName: "u", UserID: "u", Now: "2026-05-03T00:00:00.000000",
				ObservationDate: tc.obs, Source: "episodic_summarizer",
			})
			if err == nil {
				t.Fatalf("expected error for ObservationDate=%q, got nil", tc.obs)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "observation_date") {
				t.Errorf("error message must mention ObservationDate; got %q", err.Error())
			}
		})
	}
}

// TestBuildDerivedMemoryProps_HappyPath asserts the canonical shape of the
// emitted props map matches what consumers (search retrieval, eval harness
// _extract_ts, AGE traversal) expect.
func TestBuildDerivedMemoryProps_HappyPath(t *testing.T) {
	props, err := BuildDerivedMemoryProps(DerivedMemoryProps{
		ID:              "id-123",
		Memory:          "Caroline discussed a hike in 2023.",
		MemoryType:      "EpisodicMemory",
		UserID:          "user-a",
		UserName:        "cube-a",
		SessionID:       "sess-1",
		Now:             "2026-05-03T07:47:03.000000",
		ObservationDate: "2023-08-25",
		Source:          "episodic_summarizer",
		HierarchyLevel:  "episodic",
		Confidence:      0.9,
		Tags:            []string{"mode:fine"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustEqual := func(key, want string) {
		t.Helper()
		got, _ := props[key].(string)
		if got != want {
			t.Errorf("props[%q] = %q, want %q", key, got, want)
		}
	}
	mustEqual("id", "id-123")
	mustEqual("memory", "Caroline discussed a hike in 2023.")
	mustEqual("memory_type", "EpisodicMemory")
	mustEqual("status", "activated")
	mustEqual("user_id", "user-a")
	mustEqual("user_name", "cube-a")
	mustEqual("session_id", "sess-1")
	mustEqual("created_at", "2026-05-03T07:47:03.000000")
	mustEqual("updated_at", "2026-05-03T07:47:03.000000")
	mustEqual("source", "episodic_summarizer")
	mustEqual("hierarchy_level", "episodic")

	// Critical: observation_date is the in-conversation anchor consumed by
	// query.py::_extract_ts. Without it, ts falls through to created_at = NOW.
	mustEqual("observation_date", "2023-08-25")

	if conf, _ := props["confidence"].(float64); conf != 0.9 {
		t.Errorf("confidence = %v, want 0.9", conf)
	}

	tags, _ := props["tags"].([]string)
	if len(tags) != 1 || tags[0] != "mode:fine" {
		t.Errorf("tags = %v, want [mode:fine]", tags)
	}

	// Sanity: the props must JSON-marshal cleanly so callers can hand them
	// straight to db.MemoryInsertNode.PropertiesJSON.
	if _, err := json.Marshal(props); err != nil {
		t.Errorf("props must JSON-marshal: %v", err)
	}
}

// TestBuildDerivedMemoryProps_DefaultsAreSafe asserts the helper still
// produces a well-formed map when optional fields are omitted: the empty
// HierarchyLevel falls back to "raw" and Confidence falls back to 0.99 (the
// canonical buildNodeProps defaults).
func TestBuildDerivedMemoryProps_DefaultsAreSafe(t *testing.T) {
	props, err := BuildDerivedMemoryProps(DerivedMemoryProps{
		ID: "id", Memory: "m", MemoryType: "SemanticMemory",
		UserName: "cube", UserID: "user",
		Now: "2026-05-03T00:00:00.000000", ObservationDate: "2023-09-01",
		Source: "tree_reorganizer",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := props["hierarchy_level"].(string); got != "raw" {
		t.Errorf("hierarchy_level default = %q, want %q", got, "raw")
	}
	if conf, _ := props["confidence"].(float64); conf != 0.99 {
		t.Errorf("confidence default = %v, want 0.99", conf)
	}
}

// TestBuildDerivedMemoryProps_ObservationDateTrimmed asserts surrounding
// whitespace on a valid date does not slip through — callers that read from
// LLM JSON / DB cells regularly carry padding.
func TestBuildDerivedMemoryProps_ObservationDateTrimmed(t *testing.T) {
	props, err := BuildDerivedMemoryProps(DerivedMemoryProps{
		ID: "id", Memory: "m", MemoryType: "EpisodicMemory",
		UserName: "cube", UserID: "user",
		Now: "2026-05-03T00:00:00.000000", ObservationDate: "  2023-08-25  ",
		Source: "episodic_summarizer",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := props["observation_date"].(string); got != "2023-08-25" {
		t.Errorf("observation_date should be trimmed: got %q, want %q", got, "2023-08-25")
	}
}

// TestBuildDerivedMemoryProps_RequiresMandatoryFields catches missing
// id/memory/memory_type/source — these are non-negotiable for retrieval.
func TestBuildDerivedMemoryProps_RequiresMandatoryFields(t *testing.T) {
	base := DerivedMemoryProps{
		ID: "id", Memory: "m", MemoryType: "EpisodicMemory",
		UserName: "cube", UserID: "user",
		Now: "2026-05-03T00:00:00.000000", ObservationDate: "2023-08-25",
		Source: "episodic_summarizer",
	}
	cases := []struct {
		name string
		mut  func(*DerivedMemoryProps)
	}{
		{"missing ID", func(p *DerivedMemoryProps) { p.ID = "" }},
		{"missing Memory", func(p *DerivedMemoryProps) { p.Memory = "" }},
		{"missing MemoryType", func(p *DerivedMemoryProps) { p.MemoryType = "" }},
		{"missing Source", func(p *DerivedMemoryProps) { p.Source = "" }},
		{"missing UserName", func(p *DerivedMemoryProps) { p.UserName = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mut(&p)
			if _, err := BuildDerivedMemoryProps(p); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
