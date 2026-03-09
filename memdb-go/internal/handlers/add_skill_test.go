package handlers

import (
	"encoding/json"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// --- parseExistingSkill tests ---

func TestParseExistingSkill_Full(t *testing.T) {
	props := map[string]any{
		"id":          "skill-001",
		"name":        "Code Review",
		"description": "Review code changes systematically",
		"procedure":   "1. Read diff 2. Check bugs",
		"tags":        []any{"code", "review"},
	}
	data, _ := json.Marshal(props)

	es := parseExistingSkill(string(data))
	if es.ID != "skill-001" {
		t.Errorf("expected id='skill-001', got %q", es.ID)
	}
	if es.Name != "Code Review" {
		t.Errorf("expected name='Code Review', got %q", es.Name)
	}
	if es.Description != "Review code changes systematically" {
		t.Errorf("expected description, got %q", es.Description)
	}
	if es.Procedure != "1. Read diff 2. Check bugs" {
		t.Errorf("expected procedure, got %q", es.Procedure)
	}
	if len(es.Tags) != 2 || es.Tags[0] != "code" || es.Tags[1] != "review" {
		t.Errorf("expected tags=[code, review], got %v", es.Tags)
	}
}

func TestParseExistingSkill_MissingFields(t *testing.T) {
	props := map[string]any{"id": "x"}
	data, _ := json.Marshal(props)

	es := parseExistingSkill(string(data))
	if es.ID != "x" {
		t.Errorf("expected id='x', got %q", es.ID)
	}
	if es.Name != "" {
		t.Errorf("expected empty name, got %q", es.Name)
	}
	if es.Tags != nil {
		t.Errorf("expected nil tags, got %v", es.Tags)
	}
}

func TestParseExistingSkill_InvalidJSON(t *testing.T) {
	es := parseExistingSkill("not json")
	if es.ID != "" {
		t.Errorf("expected empty skill for invalid JSON, got %+v", es)
	}
}

func TestParseExistingSkill_EmptyJSON(t *testing.T) {
	es := parseExistingSkill("{}")
	if es.ID != "" || es.Name != "" {
		t.Errorf("expected empty fields for empty JSON, got %+v", es)
	}
}

func TestParseExistingSkill_TagsWrongType(t *testing.T) {
	// tags is a string instead of array — should be ignored gracefully
	props := map[string]any{"id": "x", "tags": "not-an-array"}
	data, _ := json.Marshal(props)
	es := parseExistingSkill(string(data))
	if es.Tags != nil {
		t.Errorf("expected nil tags for wrong type, got %v", es.Tags)
	}
}

func TestParseExistingSkill_TagsMixedTypes(t *testing.T) {
	// tags array with mixed types — only strings should be kept
	props := map[string]any{"id": "x", "tags": []any{"valid", 123, "also-valid"}}
	data, _ := json.Marshal(props)
	es := parseExistingSkill(string(data))
	if len(es.Tags) != 2 || es.Tags[0] != "valid" || es.Tags[1] != "also-valid" {
		t.Errorf("expected [valid, also-valid], got %v", es.Tags)
	}
}

// --- buildSkillProperties tests ---

func TestBuildSkillProperties_AllFields(t *testing.T) {
	skill := &llm.SkillMemory{
		Name:        "Travel Planning",
		Description: "Plan multi-day trips",
		Procedure:   "1. Pick destination 2. Book",
		Experience:  []string{"Book early"},
		Preference:  []string{"Direct flights"},
		Examples:    []string{"Example itinerary"},
		Tags:        []string{"travel", "planning"},
		Scripts:     map[string]string{"gen.py": "print('hi')"},
		Others:      map[string]string{"notes.md": "# Notes"},
	}

	props := buildSkillProperties("id-1", "cube-a", "2026-03-03T00:00:00", skill)

	if props["id"] != "id-1" {
		t.Errorf("expected id='id-1', got %v", props["id"])
	}
	if props["memory_type"] != "SkillMemory" {
		t.Errorf("expected memory_type='SkillMemory', got %v", props["memory_type"])
	}
	if props["user_name"] != "cube-a" {
		t.Errorf("expected user_name='cube-a', got %v", props["user_name"])
	}
	if props["memory"] != "Plan multi-day trips" {
		t.Errorf("expected memory=description, got %v", props["memory"])
	}
	if props["name"] != "Travel Planning" {
		t.Errorf("expected name='Travel Planning', got %v", props["name"])
	}
	if props["source"] != "skill_extractor" {
		t.Errorf("expected source='skill_extractor', got %v", props["source"])
	}
	if props["confidence"] != 0.99 {
		t.Errorf("expected confidence=0.99, got %v", props["confidence"])
	}
	if props["status"] != "activated" {
		t.Errorf("expected status='activated', got %v", props["status"])
	}
	if props["scripts"] == nil {
		t.Error("expected scripts to be set")
	}
	if props["others"] == nil {
		t.Error("expected others to be set")
	}
}

func TestBuildSkillProperties_NilScriptsAndOthers(t *testing.T) {
	skill := &llm.SkillMemory{
		Name:        "Simple Skill",
		Description: "Does something",
		Scripts:     nil,
		Others:      nil,
	}

	props := buildSkillProperties("id-2", "cube-b", "2026-03-03T00:00:00", skill)

	if _, ok := props["scripts"]; ok {
		t.Error("expected scripts to be absent for nil")
	}
	if _, ok := props["others"]; ok {
		t.Error("expected others to be absent for nil")
	}
}

func TestBuildSkillProperties_SerializesToValidJSON(t *testing.T) {
	skill := &llm.SkillMemory{
		Name:        "Test",
		Description: "A test skill with \"quotes\" and special chars: <>&",
		Procedure:   "Step 1\nStep 2",
		Experience:  []string{"lesson 1"},
		Tags:        []string{"tag1"},
	}

	props := buildSkillProperties("id-3", "cube-c", "2026-03-03T00:00:00", skill)
	data, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("failed to marshal skill properties: %v", err)
	}

	// Verify it round-trips
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["description"] != skill.Description {
		t.Errorf("description round-trip failed: %v", parsed["description"])
	}
}

// --- strFromMap tests ---

func TestStrFromMap(t *testing.T) {
	m := map[string]any{
		"name":   "Alice",
		"count":  42,
		"nested": map[string]any{"x": 1},
	}
	if got := strFromMap(m, "name"); got != "Alice" {
		t.Errorf("expected 'Alice', got %q", got)
	}
	if got := strFromMap(m, "count"); got != "" {
		t.Errorf("expected '' for int value, got %q", got)
	}
	if got := strFromMap(m, "missing"); got != "" {
		t.Errorf("expected '' for missing key, got %q", got)
	}
}

// --- generateSkillMemory precondition tests ---

func TestGenerateSkillMemory_NilDependencies(t *testing.T) {
	h := &Handler{}
	// Should not panic with nil dependencies
	h.generateSkillMemory("cube", "user: hello\nassistant: hi", 15)
}

func TestGenerateSkillMemory_TooFewMessages(t *testing.T) {
	// messageCount < 10 should return early
	h := &Handler{}
	h.generateSkillMemory("cube", "user: hello", 5)
	// No panic = success (no LLM/DB configured to call)
}

func TestGenerateSkillMemory_HighCodeRatio(t *testing.T) {
	h := &Handler{}
	code := "```go\npackage main\nfunc main() {}\n```"
	// 100% code → should skip
	h.generateSkillMemory("cube", code, 15)
}
