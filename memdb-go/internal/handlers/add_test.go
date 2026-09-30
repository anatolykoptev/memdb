package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// --- extractFastMemoriesPerMessage tests ---

// TestExtractFastMemoriesPerMessage_OneRowPerMsg — granularity contract:
// no aggregation, no sliding window. Two messages → two extractedMemory.
func TestExtractFastMemoriesPerMessage_OneRowPerMsg(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "I have 3 kids", ChatTime: "2026-02-16T10:00:00"},
		{Role: "user", Content: "My pets are Oliver, Luna, Bailey", ChatTime: "2026-02-16T10:00:01"},
	}

	got := extractFastMemoriesPerMessage(msgs)
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "3 kids") {
		t.Errorf("row 0 missing fact: %q", got[0].Text)
	}
	if !strings.Contains(got[1].Text, "Oliver") {
		t.Errorf("row 1 missing fact: %q", got[1].Text)
	}
	for i, r := range got {
		if len(r.Sources) != 1 {
			t.Errorf("row %d expected 1 source, got %d", i, len(r.Sources))
		}
	}
}

// TestExtractFastMemoriesPerMessage_EmptyContentSkipped — keeps the contract
// of skipping whitespace-only messages so cosine dedup doesn't see them.
func TestExtractFastMemoriesPerMessage_EmptyContentSkipped(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "   "},
		{Role: "user", Content: "real msg"},
		{Role: "user", Content: ""},
	}
	got := extractFastMemoriesPerMessage(msgs)
	if len(got) != 1 {
		t.Fatalf("want 1 row (whitespace + empty skipped), got %d", len(got))
	}
}

// TestExtractFastMemoriesPerMessage_MemTypeByRole — single-msg "windows"
// can't be mixed-role, so the LongTermMemory class falls back to assistant
// role. user role keeps UserMemory.
func TestExtractFastMemoriesPerMessage_MemTypeByRole(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "user line"},
		{Role: "assistant", Content: "assistant line"},
	}
	got := extractFastMemoriesPerMessage(msgs)
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	if got[0].MemoryType != "UserMemory" {
		t.Errorf("user row: want UserMemory, got %s", got[0].MemoryType)
	}
	if got[1].MemoryType != "LongTermMemory" {
		t.Errorf("assistant row: want LongTermMemory, got %s", got[1].MemoryType)
	}
}

// TestSelectFastExtractor_DefaultPerMessage — empty env routes through the
// per-message extractor; same shape as TestExtractFastMemoriesPerMessage_*.
func TestSelectFastExtractor_DefaultPerMessage(t *testing.T) {
	t.Setenv(fastGranularityEnv, "")
	msgs := []chatMessage{
		{Role: "user", Content: "msg one"},
		{Role: "user", Content: "msg two"},
	}
	got := selectFastExtractor(&fullAddRequest{Messages: msgs})
	if len(got) != 2 {
		t.Errorf("default route should be per-message, got %d rows", len(got))
	}
}

// TestSelectFastExtractor_WindowOptIn — explicit MEMDB_FAST_GRANULARITY=window
// re-routes through the legacy extractor for A/B + summary workloads.
func TestSelectFastExtractor_WindowOptIn(t *testing.T) {
	t.Setenv(fastGranularityEnv, "window")
	msgs := []chatMessage{
		{Role: "user", Content: "msg one"},
		{Role: "user", Content: "msg two"},
	}
	got := selectFastExtractor(&fullAddRequest{Messages: msgs})
	// Windowed extractor packs both messages into one window (~64 chars total
	// is well under windowChars=4096), so 1 row is expected.
	if len(got) != 1 {
		t.Errorf("window route: want 1 windowed row, got %d", len(got))
	}
	if len(got[0].Sources) != 2 {
		t.Errorf("window row should aggregate both sources, got %d", len(got[0].Sources))
	}
}

// --- extractFastMemories tests ---

func TestExtractFastMemories_SingleMessage(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "Remember I like coffee", ChatTime: "2026-02-16T10:00:00"},
	}

	results := extractFastMemories(msgs, windowChars)

	if len(results) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(results))
	}
	if results[0].MemoryType != "UserMemory" {
		t.Errorf("expected UserMemory for user-only message, got %s", results[0].MemoryType)
	}
	if !strings.Contains(results[0].Text, "Remember I like coffee") {
		t.Errorf("expected memory text to contain original content, got: %s", results[0].Text)
	}
	if !strings.Contains(results[0].Text, "user:") {
		t.Errorf("expected memory text to contain role prefix, got: %s", results[0].Text)
	}
	if len(results[0].Sources) != 1 {
		t.Errorf("expected 1 source, got %d", len(results[0].Sources))
	}
}

func TestExtractFastMemories_MixedRoles(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "What is Go?", ChatTime: "2026-02-16T10:00:00"},
		{Role: "assistant", Content: "Go is a programming language.", ChatTime: "2026-02-16T10:00:01"},
	}

	results := extractFastMemories(msgs, windowChars)

	if len(results) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(results))
	}
	if results[0].MemoryType != "LongTermMemory" {
		t.Errorf("expected LongTermMemory for mixed roles, got %s", results[0].MemoryType)
	}
	if len(results[0].Sources) != 2 {
		t.Errorf("expected 2 sources, got %d", len(results[0].Sources))
	}
}

func TestExtractFastMemories_EmptyMessages(t *testing.T) {
	results := extractFastMemories(nil, windowChars)
	if results != nil {
		t.Errorf("expected nil for empty messages, got %v", results)
	}

	results = extractFastMemories([]chatMessage{}, windowChars)
	if results != nil {
		t.Errorf("expected nil for empty slice, got %v", results)
	}
}

func TestExtractFastMemories_LargeContent_MultipleWindows(t *testing.T) {
	// Create messages that exceed windowChars to trigger multiple windows
	longContent := strings.Repeat("This is a test sentence with some content. ", 200) // ~8800 chars
	msgs := []chatMessage{
		{Role: "user", Content: longContent, ChatTime: "2026-02-16T10:00:00"},
		{Role: "user", Content: longContent, ChatTime: "2026-02-16T10:00:01"},
	}

	results := extractFastMemories(msgs, windowChars)

	if len(results) < 2 {
		t.Errorf("expected multiple windows for large content, got %d", len(results))
	}
	for _, r := range results {
		if r.MemoryType != "UserMemory" {
			t.Errorf("expected UserMemory for all-user messages, got %s", r.MemoryType)
		}
	}
}

func TestExtractFastMemories_UserOnlyVsMixed(t *testing.T) {
	tests := []struct {
		name     string
		msgs     []chatMessage
		wantType string
	}{
		{
			name:     "user only",
			msgs:     []chatMessage{{Role: "user", Content: "hello"}},
			wantType: "UserMemory",
		},
		{
			name: "user + assistant",
			msgs: []chatMessage{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			wantType: "LongTermMemory",
		},
		{
			name: "user + system",
			msgs: []chatMessage{
				{Role: "user", Content: "hello"},
				{Role: "system", Content: "context"},
			},
			wantType: "LongTermMemory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := extractFastMemories(tt.msgs, windowChars)
			if len(results) == 0 {
				t.Fatal("expected at least 1 result")
			}
			if results[0].MemoryType != tt.wantType {
				t.Errorf("expected %s, got %s", tt.wantType, results[0].MemoryType)
			}
		})
	}
}

// --- buildMemoryProperties tests ---

func TestBuildMemoryProperties_AllFieldsPresent(t *testing.T) {
	sources := []map[string]any{
		{"role": "user", "content": "hello", "chat_time": "2026-02-16T10:00:00"},
	}
	info := map[string]any{"source": "test"}

	props := buildMemoryProperties(
		"test-uuid", "hello world", "LongTermMemory",
		"memos", "user-1", "", "session-1", "2026-02-16T10:00:00",
		info, []string{"custom:tag"}, sources, "[working_binding:wm-uuid]",
		extractionStateExtracted, "", "",
	)

	// Check all required fields
	requiredFields := []string{
		"id", "memory", "memory_type", "status", "user_name", "user_id",
		"session_id", "created_at", "updated_at", "delete_time", "delete_record_id",
		"tags", "key", "usage", "sources", "background", "confidence", "type",
		"info", "graph_id", "extraction_state",
	}
	for _, field := range requiredFields {
		if _, ok := props[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}

	if props["id"] != "test-uuid" {
		t.Errorf("expected id=test-uuid, got %v", props["id"])
	}
	if props["memory"] != "hello world" {
		t.Errorf("expected memory=hello world, got %v", props["memory"])
	}
	if props["memory_type"] != "LongTermMemory" {
		t.Errorf("expected memory_type=LongTermMemory, got %v", props["memory_type"])
	}
	if props["status"] != "activated" {
		t.Errorf("expected status=activated, got %v", props["status"])
	}
	if props["background"] != "[working_binding:wm-uuid]" {
		t.Errorf("expected background binding, got %v", props["background"])
	}
	if props["confidence"] != 0.99 {
		t.Errorf("expected confidence=0.99, got %v", props["confidence"])
	}

	// Check tags include mode:fast + custom
	tags, ok := props["tags"].([]string)
	if !ok {
		t.Fatal("tags is not []string")
	}
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d: %v", len(tags), tags)
	}
	if tags[0] != "mode:fast" {
		t.Errorf("expected first tag=mode:fast, got %s", tags[0])
	}
	if tags[1] != "custom:tag" {
		t.Errorf("expected second tag=custom:tag, got %s", tags[1])
	}
}

func TestBuildMemoryProperties_SourcesSerialized(t *testing.T) {
	sources := []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "world"},
	}

	props := buildMemoryProperties(
		"id", "text", "LongTermMemory", "user", "user", "", "", "now",
		nil, nil, sources, "",
		extractionStateExtracted, "", "",
	)

	serialized, ok := props["sources"].([]string)
	if !ok {
		t.Fatal("sources is not []string")
	}
	if len(serialized) != 2 {
		t.Fatalf("expected 2 serialized sources, got %d", len(serialized))
	}

	// Each element should be valid JSON
	for i, s := range serialized {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Errorf("source[%d] is not valid JSON: %s", i, s)
		}
	}
}

// --- serializeSources tests ---

func TestSerializeSources_Empty(t *testing.T) {
	result := serializeSources(nil)
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %v", result)
	}

	result = serializeSources([]map[string]any{})
	if len(result) != 0 {
		t.Errorf("expected empty slice for empty input, got %v", result)
	}
}

func TestSerializeSources_JSONStrings(t *testing.T) {
	sources := []map[string]any{
		{"role": "user", "content": "hi"},
	}

	result := serializeSources(sources)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}

	// Should be valid JSON containing the original data
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result[0]), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %s", result[0])
	}
	if parsed["role"] != "user" {
		t.Errorf("expected role=user, got %v", parsed["role"])
	}
	if parsed["content"] != "hi" {
		t.Errorf("expected content=hi, got %v", parsed["content"])
	}
}

// --- NativeAdd proxy fallback tests ---

func TestNativeAdd_MissingUserID(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodPost, "/product/add", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "user_id is required") {
		t.Errorf("expected user_id error, got: %s", w.Body.String())
	}
}

func TestNativeAdd_InvalidMode(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"test","mode":"turbo"}`))
	w := httptest.NewRecorder()
	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mode must be one of") {
		t.Errorf("expected mode error, got: %s", w.Body.String())
	}
}

func TestNativeAdd_InvalidAsyncMode(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"test","async_mode":"invalid"}`))
	w := httptest.NewRecorder()
	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "async_mode must be one of") {
		t.Errorf("expected async_mode error, got: %s", w.Body.String())
	}
}

func TestCanHandleNativeAdd(t *testing.T) {
	tests := []struct {
		name     string
		handler  *Handler
		req      *fullAddRequest
		expected bool
	}{
		{
			name:     "nil postgres",
			handler:  &Handler{},
			req:      &fullAddRequest{},
			expected: false,
		},
		{
			name: "mode fine",
			handler: &Handler{
				postgres: &stubPostgres,
				embedder: &stubEmbedder{},
			},
			req:      &fullAddRequest{Mode: strPtr("fine")},
			expected: false,
		},
		{
			name: "async mode",
			handler: &Handler{
				postgres: &stubPostgres,
				embedder: &stubEmbedder{},
			},
			req:      &fullAddRequest{AsyncMode: strPtr("async")},
			expected: false,
		},
		{
			name: "feedback without llmChat → proxy",
			handler: &Handler{
				postgres: &stubPostgres,
				embedder: &stubEmbedder{},
			},
			req:      &fullAddRequest{IsFeedback: boolPtr(true)},
			expected: false,
		},
		{
			name: "fast mode eligible",
			handler: &Handler{
				postgres: &stubPostgres,
				embedder: &stubEmbedder{},
			},
			req:      &fullAddRequest{Mode: strPtr("fast")},
			expected: true,
		},
		{
			name: "nil mode (defaults to fine, needs llmExtractor)",
			handler: &Handler{
				postgres: &stubPostgres,
				embedder: &stubEmbedder{},
			},
			req:      &fullAddRequest{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.handler.canHandleNativeAdd(tt.req)
			if got != tt.expected {
				t.Errorf("canHandleNativeAdd() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// --- test helpers ---

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// stubPostgres is a zero-value Postgres for nil-check tests (not for actual queries).
var stubPostgres = stubPostgresType{}

type stubPostgresType = db.Postgres

// stubEmbedder implements embedder.Embedder for tests.
type stubEmbedder struct{}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range texts {
		// Non-zero stub: first element = 1.0 so the zero-vector guard
		// in batchEmbedFastTexts / embedSingle doesn't reject it.
		// All stubs share the same direction — unit tests that need
		// distinct directions use livepgEmbedder instead.
		v := make([]float32, 1024)
		v[0] = 1.0
		result[i] = v
	}
	return result, nil
}
func (s *stubEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	v := make([]float32, 1024)
	v[0] = 1.0
	return v, nil
}
func (s *stubEmbedder) Dimension() int { return 1024 }
func (s *stubEmbedder) Close() error   { return nil }
