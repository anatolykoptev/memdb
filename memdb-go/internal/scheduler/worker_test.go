package scheduler

import (
	"strings"
	"testing"
)

// ---- splitStreamKey tests ---------------------------------------------------

func TestSplitStreamKey_Valid(t *testing.T) {
	key := "scheduler:messages:stream:v2.0:user123:cube456:mem_update"
	parts := splitStreamKey(key)
	if parts.userID != "user123" {
		t.Errorf("userID = %q, want %q", parts.userID, "user123")
	}
	if parts.cubeID != "cube456" {
		t.Errorf("cubeID = %q, want %q", parts.cubeID, "cube456")
	}
	if parts.label != "mem_update" {
		t.Errorf("label = %q, want %q", parts.label, "mem_update")
	}
}

func TestSplitStreamKey_NoLabel(t *testing.T) {
	// Some keys may omit the label segment (just user:cube).
	key := "scheduler:messages:stream:v2.0:user123:cube456"
	parts := splitStreamKey(key)
	if parts.userID != "user123" {
		t.Errorf("userID = %q, want %q", parts.userID, "user123")
	}
	if parts.cubeID != "cube456" {
		t.Errorf("cubeID = %q, want %q", parts.cubeID, "cube456")
	}
	if parts.label != "" {
		t.Errorf("label = %q, want empty", parts.label)
	}
}

func TestSplitStreamKey_TooShort(t *testing.T) {
	// A key with fewer than 4 prefix segments returns empty.
	key := "scheduler:messages:stream"
	parts := splitStreamKey(key)
	if parts.cubeID != "" || parts.userID != "" {
		t.Errorf("expected empty parts for short key, got userID=%q cubeID=%q", parts.userID, parts.cubeID)
	}
}

func TestSplitStreamKey_Empty(t *testing.T) {
	parts := splitStreamKey("")
	if parts.cubeID != "" || parts.userID != "" || parts.label != "" {
		t.Errorf("expected all-empty parts for empty key")
	}
}

// ---- indexOf tests ----------------------------------------------------------

func TestIndexOf_Found(t *testing.T) {
	s := "hello:world"
	idx := indexOf(s, ':', 0)
	if idx != 5 {
		t.Errorf("indexOf = %d, want 5", idx)
	}
}

func TestIndexOf_NotFound(t *testing.T) {
	s := "helloworld"
	idx := indexOf(s, ':', 0)
	if idx != -1 {
		t.Errorf("indexOf = %d, want -1", idx)
	}
}

func TestIndexOf_FromOffset(t *testing.T) {
	s := "a:b:c"
	idx := indexOf(s, ':', 2)
	if idx != 3 {
		t.Errorf("indexOf from offset 2 = %d, want 3", idx)
	}
}

// ---- Label constants sanity check -------------------------------------------

func TestLabelConstants(t *testing.T) {
	labels := map[string]string{
		"add":          LabelAdd,
		"mem_organize": LabelMemOrganize,
		"mem_read":     LabelMemRead,
		"mem_update":   LabelMemUpdate,
		"pref_add":     LabelPrefAdd,
		"query":        LabelQuery,
		"answer":       LabelAnswer,
		"mem_feedback": LabelMemFeedback,
		"mem_archive":  LabelMemArchive,
	}
	for want, got := range labels {
		if got != want {
			t.Errorf("label constant = %q, want %q", got, want)
		}
	}
}

// ---- Worker constants sanity check ------------------------------------------

func TestWorkerConstants(t *testing.T) {
	if ConsumerGroup == "" {
		t.Error("ConsumerGroup must not be empty")
	}
	if StreamKeyPrefix == "" {
		t.Error("StreamKeyPrefix must not be empty")
	}
	if vsetKeyScanPattern == "" {
		t.Error("vsetKeyScanPattern must not be empty")
	}
	if periodicReorgInterval <= 0 {
		t.Error("periodicReorgInterval must be positive")
	}
	if minIdleTime <= 0 {
		t.Error("minIdleTime must be positive")
	}
}

// ---- parseMemReadIDs tests --------------------------------------------------

func TestParseMemReadIDs_CommaSeparated(t *testing.T) {
	ids := parseMemReadIDs("uuid1,uuid2,uuid3")
	if len(ids) != 3 || ids[0] != "uuid1" || ids[2] != "uuid3" {
		t.Errorf("expected [uuid1 uuid2 uuid3], got %v", ids)
	}
}

func TestParseMemReadIDs_CommaSeparatedWithSpaces(t *testing.T) {
	ids := parseMemReadIDs(" uuid1 , uuid2 , uuid3 ")
	if len(ids) != 3 || ids[0] != "uuid1" || ids[1] != "uuid2" {
		t.Errorf("expected [uuid1 uuid2 uuid3], got %v", ids)
	}
}

func TestParseMemReadIDs_JSONArray(t *testing.T) {
	ids := parseMemReadIDs(`{"memory_ids":["id1","id2"]}`)
	if len(ids) != 2 || ids[0] != "id1" || ids[1] != "id2" {
		t.Errorf("expected [id1 id2], got %v", ids)
	}
}

func TestParseMemReadIDs_JSONStr(t *testing.T) {
	ids := parseMemReadIDs(`{"memory_ids_str":"id1,id2,id3"}`)
	if len(ids) != 3 || ids[2] != "id3" {
		t.Errorf("expected [id1 id2 id3], got %v", ids)
	}
}

func TestParseMemReadIDs_Empty(t *testing.T) {
	if ids := parseMemReadIDs(""); ids != nil {
		t.Errorf("expected nil for empty, got %v", ids)
	}
	if ids := parseMemReadIDs("  "); ids != nil {
		t.Errorf("expected nil for whitespace, got %v", ids)
	}
}

func TestParseMemReadIDs_Single(t *testing.T) {
	ids := parseMemReadIDs("only-one-id")
	if len(ids) != 1 || ids[0] != "only-one-id" {
		t.Errorf("expected [only-one-id], got %v", ids)
	}
}

// ---- parsePrefConversation tests --------------------------------------------

func TestParsePrefConversation_PlainText(t *testing.T) {
	got := parsePrefConversation("user likes coffee and dislikes tea")
	if got != "user likes coffee and dislikes tea" {
		t.Errorf("expected plain text pass-through, got %q", got)
	}
}

func TestParsePrefConversation_JSONHistory(t *testing.T) {
	got := parsePrefConversation(`{"history":[{"role":"user","content":"I love hiking"},{"role":"assistant","content":"Great!"}]}`)
	if !strings.Contains(got, "I love hiking") || !strings.Contains(got, "Great!") {
		t.Errorf("expected history concatenated, got %q", got)
	}
}

func TestParsePrefConversation_JSONConversation(t *testing.T) {
	got := parsePrefConversation(`{"conversation":"user prefers dark mode"}`)
	if got != "user prefers dark mode" {
		t.Errorf("expected conversation field, got %q", got)
	}
}

func TestParsePrefConversation_Empty(t *testing.T) {
	if got := parsePrefConversation(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := parsePrefConversation("  "); got != "" {
		t.Errorf("expected empty for whitespace, got %q", got)
	}
}

// ---- parseFeedbackMemoryIDs tests ------------------------------------------

func TestParseFeedbackPayload_Full(t *testing.T) {
	content := `{"session_id":"s1","retrieved_memory_ids":["id1","id2"],"feedback_content":"memory is wrong"}`
	ids, fc := parseFeedbackPayload(content)
	if len(ids) != 2 || ids[0] != "id1" {
		t.Errorf("ids = %v, want [id1 id2]", ids)
	}
	if fc != "memory is wrong" {
		t.Errorf("feedbackContent = %q, want %q", fc, "memory is wrong")
	}
}

func TestParseFeedbackPayload_NoFeedbackContent(t *testing.T) {
	ids, fc := parseFeedbackPayload(`{"retrieved_memory_ids":["id1"]}`)
	if len(ids) != 1 {
		t.Errorf("expected 1 id, got %v", ids)
	}
	if fc != "" {
		t.Errorf("expected empty feedback_content, got %q", fc)
	}
}

func TestParseFeedbackMemoryIDs_Valid(t *testing.T) {
	content := `{"session_id":"sess1","retrieved_memory_ids":["id1","id2","id3"],"feedback_content":"good"}`
	ids := parseFeedbackMemoryIDs(content)
	if len(ids) != 3 {
		t.Fatalf("len = %d, want 3", len(ids))
	}
	if ids[0] != "id1" || ids[1] != "id2" || ids[2] != "id3" {
		t.Errorf("ids = %v, want [id1 id2 id3]", ids)
	}
}

func TestParseFeedbackMemoryIDs_Empty(t *testing.T) {
	content := `{"session_id":"sess1","retrieved_memory_ids":[]}`
	ids := parseFeedbackMemoryIDs(content)
	if len(ids) != 0 {
		t.Errorf("expected empty, got %v", ids)
	}
}

func TestParseFeedbackMemoryIDs_MissingField(t *testing.T) {
	content := `{"session_id":"sess1","feedback_content":"wrong memory"}`
	ids := parseFeedbackMemoryIDs(content)
	if ids != nil {
		t.Errorf("expected nil for missing field, got %v", ids)
	}
}

func TestParseFeedbackMemoryIDs_Malformed(t *testing.T) {
	if ids := parseFeedbackMemoryIDs("not json"); ids != nil {
		t.Errorf("expected nil for malformed JSON, got %v", ids)
	}
	if ids := parseFeedbackMemoryIDs(""); ids != nil {
		t.Errorf("expected nil for empty string, got %v", ids)
	}
}

// ---- DLQ key sanity check ---------------------------------------------------

func TestDLQStreamKey(t *testing.T) {
	if dlqStreamKey == "" {
		t.Error("dlqStreamKey must not be empty")
	}
	// Must not conflict with scheduler stream keys.
	if len(dlqStreamKey) < 10 {
		t.Errorf("dlqStreamKey %q looks too short", dlqStreamKey)
	}
}
