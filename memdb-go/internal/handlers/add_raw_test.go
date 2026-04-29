package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- extractRawItems tests ---

func TestExtractRawItems_PreservesContent(t *testing.T) {
	// Raw mode must NOT add timestamp prefixes or split — text comes through as-is.
	jsonPayload := `{"action":"click","selector":".btn","ts":1234567890}`
	msgs := []chatMessage{
		{Role: "user", Content: jsonPayload, ChatTime: "2026-04-10T10:00:00"},
	}

	items := extractRawItems(msgs)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Text != jsonPayload {
		t.Errorf("expected text unchanged, got: %q", items[0].Text)
	}
	if items[0].ChatTime != "2026-04-10T10:00:00" {
		t.Errorf("expected ChatTime preserved, got %q", items[0].ChatTime)
	}
	if items[0].Role != "user" {
		t.Errorf("expected Role preserved, got %q", items[0].Role)
	}
	if strings.Contains(items[0].Text, "user:") {
		t.Errorf("raw text must not contain role prefix, got: %q", items[0].Text)
	}
	if strings.Contains(items[0].Text, "[2026-04-10") {
		t.Errorf("raw text must not contain timestamp prefix, got: %q", items[0].Text)
	}
}

func TestExtractRawItems_PerMsgMetadata(t *testing.T) {
	// Per-msg metadata (uuid, agent_id, role) must propagate so add_raw
	// can stamp them into properties.info.
	msgs := []chatMessage{
		{Role: "assistant", Content: "x", UUID: "uuid-1", AgentID: "opus"},
	}
	items := extractRawItems(msgs)
	if items[0].UUID != "uuid-1" || items[0].AgentID != "opus" || items[0].Role != "assistant" {
		t.Errorf("per-msg metadata lost: %+v", items[0])
	}
}

func TestExtractRawItems_LongContentNotSplit(t *testing.T) {
	long := strings.Repeat("x", windowChars*2)
	msgs := []chatMessage{{Role: "user", Content: long}}

	items := extractRawItems(msgs)
	if len(items) != 1 {
		t.Fatalf("expected 1 item (no splitting), got %d", len(items))
	}
	if len(items[0].Text) != len(long) {
		t.Errorf("expected text length %d, got %d", len(long), len(items[0].Text))
	}
}

func TestExtractRawItems_MultipleMessages(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
		{Role: "user", Content: "third"},
	}
	items := extractRawItems(msgs)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	want := []string{"first", "second", "third"}
	for i, w := range want {
		if items[i].Text != w {
			t.Errorf("items[%d].Text = %q, want %q", i, items[i].Text, w)
		}
	}
}

func TestExtractRawItems_SkipsEmpty(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: ""},
		{Role: "user", Content: "   "},
		{Role: "user", Content: "real content"},
	}
	items := extractRawItems(msgs)
	if len(items) != 1 {
		t.Fatalf("expected 1 item after skipping empty, got %d", len(items))
	}
	if items[0].Text != "real content" {
		t.Errorf("expected 'real content', got %q", items[0].Text)
	}
}

func TestExtractRawItems_EmptyMessages(t *testing.T) {
	if items := extractRawItems(nil); len(items) != 0 {
		t.Errorf("expected empty for nil input, got %v", items)
	}
	if items := extractRawItems([]chatMessage{}); len(items) != 0 {
		t.Errorf("expected empty for empty slice, got %v", items)
	}
}

// --- hashRawItems tests ---

func TestHashRawItems_DeterministicAndDistinct(t *testing.T) {
	items := []rawTextItem{
		{Text: "alpha"}, {Text: "beta"}, {Text: "alpha"},
	}
	hashes := hashRawItems(items)
	if len(hashes) != 3 {
		t.Fatalf("expected 3 hashes, got %d", len(hashes))
	}
	if hashes[0] != hashes[2] {
		t.Errorf("identical texts must yield identical hashes: %q vs %q", hashes[0], hashes[2])
	}
	if hashes[0] == hashes[1] {
		t.Errorf("different texts must yield different hashes: %q == %q", hashes[0], hashes[1])
	}
}

// --- buildRawNode tests ---

func TestBuildRawNode_LongTermOnlyAndRawMode(t *testing.T) {
	fac := fastAddContext{
		cubeID:     "user-123",
		agentID:    "agent-1",
		sessionID:  "session-1",
		now:        "2026-04-10T10:00:00.000000",
		info:       map[string]any{},
		customTags: []string{"source:experience"},
	}
	embedding := make([]float32, 1024)
	info := mergeInfo(fac.info, "deadbeef")

	node, item, err := buildRawNode("hello raw world", embedding, fac, info)
	if err != nil {
		t.Fatalf("buildRawNode: %v", err)
	}
	if node.ID == "" {
		t.Error("expected non-empty node ID")
	}
	if item.MemoryID != node.ID {
		t.Errorf("item.MemoryID %q != node.ID %q", item.MemoryID, node.ID)
	}
	if item.MemoryType != memTypeLongTerm {
		t.Errorf("expected MemoryType=%s, got %s", memTypeLongTerm, item.MemoryType)
	}
	if item.Memory != "hello raw world" {
		t.Errorf("expected memory text unchanged, got %q", item.Memory)
	}

	var props map[string]any
	if err := json.Unmarshal(node.PropertiesJSON, &props); err != nil {
		t.Fatalf("unmarshal props: %v", err)
	}
	if props["memory_type"] != memTypeLongTerm {
		t.Errorf("expected memory_type=%s, got %v", memTypeLongTerm, props["memory_type"])
	}
	if props["background"] != "" {
		t.Errorf("raw node must have empty background (no WM binding), got %v", props["background"])
	}

	tags, _ := props["tags"].([]any)
	if len(tags) < 1 {
		t.Fatal("expected at least one tag")
	}
	if tags[0] != "mode:raw" {
		t.Errorf("expected first tag=mode:raw, got %v", tags[0])
	}
}

// --- validation tests ---

func TestValidateAddRequest_RawModeAccepted(t *testing.T) {
	userID := "user-1"
	mode := "raw"
	errs := validateAddRequest(&userID, nil, &mode)
	if len(errs) != 0 {
		t.Errorf("expected no errors for mode=raw, got %v", errs)
	}
}

func TestValidateAddRequest_InvalidModeMessageMentionsRaw(t *testing.T) {
	userID := "user-1"
	bad := "turbo"
	errs := validateAddRequest(&userID, nil, &bad)
	if len(errs) == 0 {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(errs[0], "raw") {
		t.Errorf("expected mode error to mention 'raw', got: %s", errs[0])
	}
}

// --- routing / eligibility tests ---

func TestCanHandleNativeAdd_RawMode(t *testing.T) {
	h := &Handler{
		postgres: &stubPostgres,
		embedder: &stubEmbedder{},
	}
	req := &fullAddRequest{Mode: strPtr("raw")}
	if !h.canHandleNativeAdd(req) {
		t.Error("expected raw mode to be handled natively (no LLM required)")
	}
}

func TestCanHandleNativeAdd_RawMode_NilEmbedder(t *testing.T) {
	h := &Handler{postgres: &stubPostgres}
	req := &fullAddRequest{Mode: strPtr("raw")}
	if h.canHandleNativeAdd(req) {
		t.Error("expected raw mode to fail without embedder")
	}
}

// --- Validation gate: raw mode passes the validateAddRequest gate ---

func TestValidateAddRequest_RawNotRejected(t *testing.T) {
	// Sanity: ensure validation accepts raw + valid user_id, returning zero errors.
	uid := "u1"
	mode := "raw"
	if errs := validateAddRequest(&uid, nil, &mode); len(errs) != 0 {
		t.Errorf("raw mode should pass validation, got errors: %v", errs)
	}
}
