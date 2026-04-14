package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockSkillLLM creates a test server that returns responseBody as LLM content.
func mockSkillLLM(t *testing.T, responseBody string) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": responseBody}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	client := NewClient(srv.URL, "", "test-model", nil, slog.Default())
	return srv, client
}

// --- ChunkTasks tests ---

func TestChunkTasks_Basic(t *testing.T) {
	rawLLM := `[
		{"task_id": 1, "task_name": "Code Review", "message_indices": [[0, 3]]},
		{"task_id": 2, "task_name": "Data Analysis", "message_indices": [[4, 6]]}
	]`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	conversation := "user: review this code\nassistant: sure\nuser: what about line 5?\nassistant: looks good\nuser: now analyze the data\nassistant: here are the results\nuser: thanks"
	chunks, err := ChunkTasks(context.Background(), client, conversation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].TaskName != "Code Review" {
		t.Errorf("expected 'Code Review', got %q", chunks[0].TaskName)
	}
	if chunks[1].TaskName != "Data Analysis" {
		t.Errorf("expected 'Data Analysis', got %q", chunks[1].TaskName)
	}
	if chunks[0].Messages == "" {
		t.Error("expected non-empty Messages for chunk 0")
	}
	if chunks[1].Messages == "" {
		t.Error("expected non-empty Messages for chunk 1")
	}
}

func TestChunkTasks_JumpingConversation(t *testing.T) {
	rawLLM := `[
		{"task_id": 1, "task_name": "Travel Planning", "message_indices": [[0, 1], [4, 4]]}
	]`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	conversation := "user: plan my trip\nassistant: where to?\nuser: weather check\nassistant: it's sunny\nuser: back to the trip"
	chunks, err := ChunkTasks(context.Background(), client, conversation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	// Messages should contain lines 0, 1, and 4 (not 2, 3)
	msg := chunks[0].Messages
	if msg == "" {
		t.Error("expected non-empty Messages")
	}
	// Should contain first and last lines
	if !containsLine(msg, "user: plan my trip") {
		t.Error("expected 'user: plan my trip' in messages")
	}
	if !containsLine(msg, "user: back to the trip") {
		t.Error("expected 'user: back to the trip' in messages")
	}
}

func TestChunkTasks_MarkdownFences(t *testing.T) {
	rawLLM := "```json\n[{\"task_id\": 1, \"task_name\": \"Debug\", \"message_indices\": [[0, 2]]}]\n```"
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	chunks, err := ChunkTasks(context.Background(), client, "line0\nline1\nline2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestChunkTasks_Empty(t *testing.T) {
	srv, client := mockSkillLLM(t, "[]")
	defer srv.Close()

	chunks, err := ChunkTasks(context.Background(), client, "user: hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkTasks_InvalidJSON(t *testing.T) {
	srv, client := mockSkillLLM(t, "not json")
	defer srv.Close()

	_, err := ChunkTasks(context.Background(), client, "user: hi")
	if err == nil {
		t.Error("expected error on invalid JSON")
	}
}

func TestChunkTasks_LLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", "test-model", nil, slog.Default())
	_, err := ChunkTasks(context.Background(), client, "user: hi")
	if err == nil {
		t.Error("expected error on LLM failure")
	}
}

// --- ExtractSkill tests ---

func TestExtractSkill_NewSkill(t *testing.T) {
	rawLLM := `{
		"name": "Code Review Workflow",
		"description": "Systematic process for reviewing code changes",
		"procedure": "1. Read the diff 2. Check for bugs 3. Suggest improvements",
		"experience": ["Always check edge cases"],
		"preference": ["Prefer small PRs"],
		"examples": ["Example review output"],
		"tags": ["code", "review"],
		"scripts": null,
		"others": null,
		"update": false,
		"old_memory_id": ""
	}`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: review this code\nassistant: looks good", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill == nil {
		t.Fatal("expected non-nil skill")
	}
	if skill.Name != "Code Review Workflow" {
		t.Errorf("expected 'Code Review Workflow', got %q", skill.Name)
	}
	if skill.Description == "" {
		t.Error("expected non-empty description")
	}
	if skill.Procedure == "" {
		t.Error("expected non-empty procedure")
	}
	if len(skill.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(skill.Tags))
	}
	if skill.Update {
		t.Error("expected update=false for new skill")
	}
	if skill.OldMemoryID != "" {
		t.Errorf("expected empty old_memory_id, got %q", skill.OldMemoryID)
	}
}

func TestExtractSkill_UpdateExisting(t *testing.T) {
	rawLLM := `{
		"name": "Travel Planning",
		"description": "Planning multi-day trips with accommodations",
		"procedure": "1. Choose destination 2. Book flights 3. Reserve hotels",
		"experience": ["Book early for better prices", "Check visa requirements"],
		"preference": ["Prefer direct flights"],
		"examples": [],
		"tags": ["travel"],
		"scripts": null,
		"others": null,
		"update": true,
		"old_memory_id": "skill-abc-123"
	}`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	existing := []ExistingSkill{{
		ID: "skill-abc-123", Name: "Travel Planning",
		Description: "Basic travel planning", Procedure: "1. Choose destination",
		Tags: []string{"travel"},
	}}

	skill, err := ExtractSkill(context.Background(), client, "user: plan trip with hotels", existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill == nil {
		t.Fatal("expected non-nil skill")
	}
	if !skill.Update {
		t.Error("expected update=true")
	}
	if skill.OldMemoryID != "skill-abc-123" {
		t.Errorf("expected old_memory_id='skill-abc-123', got %q", skill.OldMemoryID)
	}
	if len(skill.Experience) != 2 {
		t.Errorf("expected 2 experience entries, got %d", len(skill.Experience))
	}
}

func TestExtractSkill_NullResponse(t *testing.T) {
	srv, client := mockSkillLLM(t, "null")
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: hi\nassistant: hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill != nil {
		t.Errorf("expected nil skill for null response, got %+v", skill)
	}
}

func TestExtractSkill_EmptyResponse(t *testing.T) {
	srv, client := mockSkillLLM(t, "")
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: hi", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill != nil {
		t.Errorf("expected nil skill for empty response, got %+v", skill)
	}
}

func TestExtractSkill_IncompleteSkillSkipped(t *testing.T) {
	// Missing description → should return nil
	rawLLM := `{"name": "Something", "description": "", "procedure": "...", "update": false, "old_memory_id": ""}`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: do something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill != nil {
		t.Errorf("expected nil for incomplete skill (empty description), got %+v", skill)
	}
}

func TestExtractSkill_MissingNameSkipped(t *testing.T) {
	rawLLM := `{"name": "", "description": "A valid description", "procedure": "...", "update": false, "old_memory_id": ""}`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: do something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill != nil {
		t.Errorf("expected nil for incomplete skill (empty name), got %+v", skill)
	}
}

func TestExtractSkill_WithScriptsAndOthers(t *testing.T) {
	rawLLM := `{
		"name": "Data Processing",
		"description": "Process CSV data files",
		"procedure": "1. Load CSV 2. Clean data 3. Export",
		"experience": [],
		"preference": [],
		"examples": [],
		"tags": ["data"],
		"scripts": {"process.py": "import csv\nprint('hello')"},
		"others": {"notes.md": "# Notes\nSome notes"},
		"update": false,
		"old_memory_id": ""
	}`
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: process the CSV", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill == nil {
		t.Fatal("expected non-nil skill")
	}
	if skill.Scripts == nil || skill.Scripts["process.py"] == "" {
		t.Errorf("expected scripts with process.py, got %v", skill.Scripts)
	}
	if skill.Others == nil || skill.Others["notes.md"] == "" {
		t.Errorf("expected others with notes.md, got %v", skill.Others)
	}
}

func TestExtractSkill_MarkdownFences(t *testing.T) {
	rawLLM := "```json\n{\"name\": \"Test\", \"description\": \"A test skill\", \"procedure\": \"...\", \"update\": false, \"old_memory_id\": \"\"}\n```"
	srv, client := mockSkillLLM(t, rawLLM)
	defer srv.Close()

	skill, err := ExtractSkill(context.Background(), client, "user: test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill == nil {
		t.Fatal("expected non-nil skill after stripping fences")
	}
	if skill.Name != "Test" {
		t.Errorf("expected name 'Test', got %q", skill.Name)
	}
}

func TestExtractSkill_InvalidJSON(t *testing.T) {
	srv, client := mockSkillLLM(t, "not valid json at all")
	defer srv.Close()

	_, err := ExtractSkill(context.Background(), client, "user: something", nil)
	if err == nil {
		t.Error("expected error on invalid JSON")
	}
}

func TestExtractSkill_LLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", "test-model", nil, slog.Default())
	_, err := ExtractSkill(context.Background(), client, "user: something", nil)
	if err == nil {
		t.Error("expected error on LLM failure")
	}
}

// --- sliceConversationLines tests ---

func TestSliceConversationLines_Basic(t *testing.T) {
	lines := []string{"line0", "line1", "line2", "line3", "line4"}

	got := sliceConversationLines(lines, [][]int{{0, 2}})
	want := "line0\nline1\nline2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSliceConversationLines_MultipleRanges(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	got := sliceConversationLines(lines, [][]int{{0, 1}, {3, 4}})
	want := "a\nb\nd\ne"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSliceConversationLines_SingleIndex(t *testing.T) {
	lines := []string{"a", "b", "c"}
	got := sliceConversationLines(lines, [][]int{{1}})
	if got != "b" {
		t.Errorf("got %q, want 'b'", got)
	}
}

func TestSliceConversationLines_OutOfBounds(t *testing.T) {
	lines := []string{"a", "b"}
	got := sliceConversationLines(lines, [][]int{{0, 10}})
	want := "a\nb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSliceConversationLines_EmptyIndices(t *testing.T) {
	lines := []string{"a", "b"}
	got := sliceConversationLines(lines, [][]int{{}})
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSliceConversationLines_NegativeStart(t *testing.T) {
	lines := []string{"a", "b"}
	got := sliceConversationLines(lines, [][]int{{-1, 0}})
	if got != "" {
		t.Errorf("got %q, want empty (negative start)", got)
	}
}

// --- parseIndexRange tests ---

func TestParseIndexRange(t *testing.T) {
	tests := []struct {
		input      []int
		wantStart  int
		wantEnd    int
	}{
		{[]int{0, 5}, 0, 5},
		{[]int{3}, 3, 3},
		{[]int{}, -1, -1},
		{[]int{1, 2, 3}, 1, 2}, // extra elements ignored
	}
	for _, tt := range tests {
		start, end := parseIndexRange(tt.input)
		if start != tt.wantStart || end != tt.wantEnd {
			t.Errorf("parseIndexRange(%v) = (%d, %d), want (%d, %d)",
				tt.input, start, end, tt.wantStart, tt.wantEnd)
		}
	}
}

// --- helper ---

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
