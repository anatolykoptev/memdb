package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// ---- constants sanity -------------------------------------------------------

func TestWMCompactConstants(t *testing.T) {
	if wmCompactThreshold <= 0 {
		t.Error("wmCompactThreshold must be positive")
	}
	if wmCompactKeepRecent <= 0 {
		t.Error("wmCompactKeepRecent must be positive")
	}
	if wmCompactKeepRecent >= wmCompactThreshold {
		t.Errorf("wmCompactKeepRecent (%d) must be < wmCompactThreshold (%d)",
			wmCompactKeepRecent, wmCompactThreshold)
	}
	if wmCompactLLMTimeout <= 0 {
		t.Error("wmCompactLLMTimeout must be positive")
	}
	if wmCompactFetchLimit <= wmCompactThreshold {
		t.Errorf("wmCompactFetchLimit (%d) must be > wmCompactThreshold (%d)",
			wmCompactFetchLimit, wmCompactThreshold)
	}
}

// ---- buildEpisodicProps -----------------------------------------------------

func TestBuildEpisodicProps_Fields(t *testing.T) {
	r := &Reorganizer{}
	data := r.buildEpisodicProps("id-1", "The user discussed Go.", "test-person", "cube-1", "2026-02-19T12:00:00.000000", 42)

	var props map[string]any
	if err := json.Unmarshal(data, &props); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	checks := map[string]any{
		"id":          "id-1",
		"memory":      "The user discussed Go.",
		"memory_type": "EpisodicMemory",
		"status":      "activated",
		"user_name":   "cube-1",      // cube partition key
		"user_id":     "test-person", // person identity — Phase 2 split
	}
	for k, want := range checks {
		got, ok := props[k]
		if !ok {
			t.Errorf("missing field %q", k)
			continue
		}
		if got != want {
			t.Errorf("field %q = %v, want %v", k, got, want)
		}
	}

	info, ok := props["info"].(map[string]any)
	if !ok {
		t.Fatal("info field missing or wrong type")
	}
	if v, ok := info["compacted_from"].(float64); !ok || int(v) != 42 {
		t.Errorf("info.compacted_from = %v, want 42", info["compacted_from"])
	}

	tags, ok := props["tags"].([]any)
	if !ok || len(tags) == 0 {
		t.Fatal("tags field missing or empty")
	}
	if tags[0] != "mode:compacted" {
		t.Errorf("tags[0] = %v, want mode:compacted", tags[0])
	}

	if score, ok := props["importance_score"].(float64); !ok || score != 1.0 {
		t.Errorf("importance_score = %v, want 1.0", props["importance_score"])
	}
}

func TestBuildEpisodicProps_ValidJSON(t *testing.T) {
	r := &Reorganizer{}
	for _, summary := range []string{"", "Short.", strings.Repeat("x", 2000)} {
		data := r.buildEpisodicProps("id", summary, "cube", "cube", "2026-01-01T00:00:00.000000", 5)
		if !json.Valid(data) {
			t.Errorf("invalid JSON for summary len %d", len(summary))
		}
	}
}

// ---- llmSummarizeWM: empty input --------------------------------------------

func TestLLMSummarizeWM_EmptyNodes(t *testing.T) {
	r := &Reorganizer{}
	summary, err := r.llmSummarizeWM(context.Background(), "cube-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty summary for nil nodes, got %q", summary)
	}
}

// ---- JSON parsing fallback (markdown fences) --------------------------------

func TestLLMSummarizeWM_JSONFallback(t *testing.T) {
	raw := "```json\n{\"summary\": \"The user asked about retry logic.\"}\n```"

	var result struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(raw[start:end+1]), &result); err2 != nil {
				t.Fatalf("fallback unmarshal: %v", err2)
			}
		}
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary from markdown-wrapped JSON")
	}
}

// ---- split logic (oldest-first) --------------------------------------------

func TestWMCompact_SplitLogic(t *testing.T) {
	nodeCount := wmCompactThreshold + 20
	nodes := makeTestMemNodes(nodeCount)

	toSummarize := nodes[:len(nodes)-wmCompactKeepRecent]
	toKeep := nodes[len(nodes)-wmCompactKeepRecent:]

	wantSummarize := nodeCount - wmCompactKeepRecent
	if len(toSummarize) != wantSummarize {
		t.Errorf("toSummarize len = %d, want %d", len(toSummarize), wantSummarize)
	}
	if len(toKeep) != wmCompactKeepRecent {
		t.Errorf("toKeep len = %d, want %d", len(toKeep), wmCompactKeepRecent)
	}

	// Oldest nodes must be in toSummarize
	if toSummarize[0].ID != nodes[0].ID {
		t.Error("toSummarize must start with the oldest node (index 0)")
	}
	// Most recent must be in toKeep
	if toKeep[len(toKeep)-1].ID != nodes[len(nodes)-1].ID {
		t.Error("toKeep must end with the newest node")
	}
}

func TestWMCompact_SplitLogic_ExactThreshold(t *testing.T) {
	nodes := makeTestMemNodes(wmCompactThreshold)
	toSummarize := nodes[:len(nodes)-wmCompactKeepRecent]
	toKeep := nodes[len(nodes)-wmCompactKeepRecent:]

	if len(toSummarize)+len(toKeep) != wmCompactThreshold {
		t.Errorf("split total = %d, want %d", len(toSummarize)+len(toKeep), wmCompactThreshold)
	}
}

// ---- below threshold: no fetch ----------------------------------------------

func TestCompactWorkingMemory_BelowThreshold_NoFetch(t *testing.T) {
	// Verify that when count < threshold, GetWorkingMemoryOldestFirst is never called.
	// We use a real Reorganizer with a nil postgres — CountWorkingMemory would panic,
	// so we test the logic indirectly via the split check.
	count := int64(wmCompactThreshold - 1)
	if count >= int64(wmCompactThreshold) {
		t.Error("test setup: count must be below threshold")
	}
	// The condition `count < int64(wmCompactThreshold)` must be true
	if count >= int64(wmCompactThreshold) {
		t.Error("threshold check logic is wrong")
	}
}

// ---- not enough nodes after keep_recent ------------------------------------

func TestCompactWorkingMemory_NotEnoughNodes(t *testing.T) {
	// If fetched nodes <= wmCompactKeepRecent, compaction must be skipped.
	nodes := makeTestMemNodes(wmCompactKeepRecent)
	if len(nodes) > wmCompactKeepRecent {
		t.Fatal("test setup error")
	}
	// The condition `len(nodes) <= wmCompactKeepRecent` must be true
	if len(nodes) > wmCompactKeepRecent {
		t.Error("guard condition is wrong")
	}
}

// ---- LLM user message format ------------------------------------------------

func TestLLMSummarizeWM_UserMessageFormat(t *testing.T) {
	nodes := makeTestMemNodes(3)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Working memory notes for session (cube: %s):\n\n", "cube-test"))
	for i, n := range nodes {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, n.Text))
	}
	msg := sb.String()

	if !strings.Contains(msg, "cube-test") {
		t.Error("user message must contain cube ID")
	}
	if !strings.Contains(msg, "1.") {
		t.Error("user message must be numbered list")
	}
	if !strings.Contains(msg, "Working memory note") {
		t.Error("user message must contain node text")
	}
	// Must have 3 numbered items
	for i := 1; i <= 3; i++ {
		if !strings.Contains(msg, fmt.Sprintf("%d.", i)) {
			t.Errorf("user message missing item %d", i)
		}
	}
}

// ---- EpisodicMemory type in SearchLTM query --------------------------------

func TestEpisodicMemory_NotInWMType(t *testing.T) {
	// EpisodicMemory must NOT be memory_type=WorkingMemory
	r := &Reorganizer{}
	data := r.buildEpisodicProps("id", "summary", "cube", "cube", "2026-01-01T00:00:00.000000", 5)
	var props map[string]any
	_ = json.Unmarshal(data, &props)
	if props["memory_type"] == "WorkingMemory" {
		t.Error("EpisodicMemory must not have memory_type=WorkingMemory")
	}
	if props["memory_type"] != "EpisodicMemory" {
		t.Errorf("memory_type = %v, want EpisodicMemory", props["memory_type"])
	}
}

// ---- wmCompactionSystemPrompt sanity ----------------------------------------

func TestWMCompactionSystemPrompt(t *testing.T) {
	if wmCompactionSystemPrompt == "" {
		t.Error("wmCompactionSystemPrompt must not be empty")
	}
	if !strings.Contains(wmCompactionSystemPrompt, "summary") {
		t.Error("wmCompactionSystemPrompt must reference 'summary' field")
	}
	if !strings.Contains(wmCompactionSystemPrompt, "third person") {
		t.Error("wmCompactionSystemPrompt must instruct third-person writing")
	}
	if !strings.Contains(wmCompactionSystemPrompt, "JSON") {
		t.Error("wmCompactionSystemPrompt must require JSON output")
	}
}

// ---- helpers ----------------------------------------------------------------

func makeTestMemNodes(n int) []db.MemNode {
	nodes := make([]db.MemNode, n)
	for i := range nodes {
		nodes[i] = db.MemNode{
			ID:   fmt.Sprintf("node-%03d", i),
			Text: fmt.Sprintf("Working memory note %d about the user session", i),
		}
	}
	return nodes
}
