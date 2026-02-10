package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

// TestNativeGetMemoryByIDs_NoPostgres verifies validation still works when
// postgres is nil (falls through to ValidatedGetMemoryByIDs which validates).

func TestNativeGetMemoryByIDs_MissingIDs(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest("POST", "/product/get_memory_by_ids",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	// With no postgres, should fall through to ValidatedGetMemoryByIDs
	// which validates and returns 400 for missing memory_ids
	h.NativeGetMemoryByIDs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "memory_ids is required") {
		t.Errorf("expected memory_ids error, got: %s", w.Body.String())
	}
}

func TestNativeGetMemoryByIDs_EmptyArray(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest("POST", "/product/get_memory_by_ids",
		strings.NewReader(`{"memory_ids":[]}`))
	w := httptest.NewRecorder()
	h.NativeGetMemoryByIDs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- NativeGetAll tests (falls back to ValidatedGetAll on nil postgres) ---

func TestNativeGetAll_NoPostgres_MissingFields(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_all",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	h.NativeGetAll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "user_id is required") {
		t.Errorf("expected user_id error, got: %s", body)
	}
	if !strings.Contains(body, "memory_type is required") {
		t.Errorf("expected memory_type error, got: %s", body)
	}
}

func TestNativeGetAll_NoPostgres_InvalidMemoryType(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_all",
		strings.NewReader(`{"user_id":"memos","memory_type":"invalid"}`))
	w := httptest.NewRecorder()

	h.NativeGetAll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "memory_type must be one of") {
		t.Errorf("expected memory_type validation error, got: %s", w.Body.String())
	}
}

func TestNativeGetAll_NoPostgres_NilBody(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_all", nil)
	w := httptest.NewRecorder()

	h.NativeGetAll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

func TestNativeGetAll_NoPostgres_InvalidJSON(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_all",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	h.NativeGetAll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("expected JSON error, got: %s", w.Body.String())
	}
}

func TestNativeGetAll_NoPostgres_EmptyUserID(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_all",
		strings.NewReader(`{"user_id":"","memory_type":"text_mem"}`))
	w := httptest.NewRecorder()

	h.NativeGetAll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "user_id is required") {
		t.Errorf("expected user_id error, got: %s", w.Body.String())
	}
}

// --- NativeDelete tests (falls back to ValidatedDelete on nil postgres) ---

func TestNativeDelete_NoPostgres_MissingFields(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/delete_memory",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "at least one of memory_ids, file_ids, or filter is required") {
		t.Errorf("expected validation error, got: %s", w.Body.String())
	}
}

func TestNativeDelete_NoPostgres_EmptyMemoryIDs(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/delete_memory",
		strings.NewReader(`{"memory_ids":[]}`))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestNativeDelete_NoPostgres_NilBody(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/delete_memory", nil)
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

func TestNativeDelete_NoPostgres_InvalidJSON(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/delete_memory",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("expected JSON error, got: %s", w.Body.String())
	}
}

// --- NativePostGetMemory tests ---

// TestNativePostGetMemory_NoPostgres_NilBody verifies that a nil body returns 400.
// When postgres is nil, NativePostGetMemory falls through to ValidatedGetMemory
// which proxies. But readBody runs first, so nil body is caught.
func TestNativePostGetMemory_NoPostgres_NilBody(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_memory", nil)
	w := httptest.NewRecorder()

	// With nil postgres, it tries ValidatedGetMemory which calls readBody first.
	// readBody catches nil body and returns 400.
	h.NativePostGetMemory(w, req)

	// ValidatedGetMemory also calls readBody which returns 400 for nil.
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

// TestNativePostGetMemory_NoPostgres_InvalidJSON verifies invalid JSON returns 400.
func TestNativePostGetMemory_NoPostgres_InvalidJSON(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_memory",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	h.NativePostGetMemory(w, req)

	// ValidatedGetMemory proxies the body, so with nil python it may fail differently.
	// But our NativePostGetMemory (postgres=nil) calls ValidatedGetMemory which calls
	// readBody + decodeJSON. Since python is nil, it will fail on proxy.
	// Actually: with nil postgres, NativePostGetMemory calls ValidatedGetMemory
	// which does readBody, decodeJSON (both pass), then proxyWithBody.
	// With nil python, that panics. So this test only works with postgres != nil path.
	// Let's skip this test and test the native path validation instead.
	t.Skip("cannot test proxy fallback without python client")
}

// TestNativePostGetMemory_MissingMemCubeID verifies that missing mem_cube_id returns 400.
// Uses a handler with a non-nil postgres mock that won't be called.
func TestNativePostGetMemory_MissingMemCubeID(t *testing.T) {
	// We need postgres non-nil to reach the native validation path.
	// Use a simple trick: create a handler and set a non-nil postgres.
	// Since we expect validation to fail before any DB call, this is safe.
	h := testValidateHandler()
	// Hack: set postgres to a non-nil value so NativePostGetMemory doesn't proxy.
	// We use setPostgresNonNil helper (defined below).
	setPostgresNonNil(h)

	req := httptest.NewRequest("POST", "/product/get_memory",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	h.NativePostGetMemory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mem_cube_id is required") {
		t.Errorf("expected mem_cube_id error, got: %s", w.Body.String())
	}
}

// TestNativePostGetMemory_EmptyMemCubeID verifies that empty mem_cube_id returns 400.
func TestNativePostGetMemory_EmptyMemCubeID(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	req := httptest.NewRequest("POST", "/product/get_memory",
		strings.NewReader(`{"mem_cube_id":""}`))
	w := httptest.NewRecorder()

	h.NativePostGetMemory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mem_cube_id is required") {
		t.Errorf("expected mem_cube_id error, got: %s", w.Body.String())
	}
}

// TestNativePostGetMemory_InvalidJSON_NativeRoute verifies invalid JSON on native path returns 400.
func TestNativePostGetMemory_InvalidJSON_NativeRoute(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	req := httptest.NewRequest("POST", "/product/get_memory",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	h.NativePostGetMemory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("expected JSON error, got: %s", w.Body.String())
	}
}

// TestNativePostGetMemory_EmptyBody_NativeRoute verifies empty body on native path returns 400.
func TestNativePostGetMemory_EmptyBody_NativeRoute(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	req := httptest.NewRequest("POST", "/product/get_memory",
		strings.NewReader(""))
	w := httptest.NewRecorder()

	h.NativePostGetMemory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

// TestFormatMemoryBucket verifies the formatMemoryBucket helper.
func TestFormatMemoryBucket(t *testing.T) {
	results := []map[string]any{
		{
			"memory_id":  "123",
			"properties": `{"id":"abc-123","memory":"test memory","memory_type":"LongTermMemory","status":"activated"}`,
		},
		{
			"memory_id":  "456",
			"properties": `{"id":"def-456","memory":"another memory","memory_type":"LongTermMemory","status":"activated"}`,
		},
	}

	bucket := formatMemoryBucket(results, "memos", 42)

	if bucket.CubeID != "memos" {
		t.Errorf("expected CubeID 'memos', got %q", bucket.CubeID)
	}
	if bucket.TotalNodes != 42 {
		t.Errorf("expected TotalNodes 42, got %d", bucket.TotalNodes)
	}
	if len(bucket.Memories) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(bucket.Memories))
	}

	// Check first memory has proper FormatMemoryItem structure
	first := bucket.Memories[0]
	if first["id"] != "abc-123" {
		t.Errorf("expected id 'abc-123', got %v", first["id"])
	}
	if first["memory"] != "test memory" {
		t.Errorf("expected memory 'test memory', got %v", first["memory"])
	}
	if first["ref_id"] != "[abc]" {
		t.Errorf("expected ref_id '[abc]', got %v", first["ref_id"])
	}
	// Check metadata exists
	meta, ok := first["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata to be map[string]any")
	}
	if meta["memory_type"] != "LongTermMemory" {
		t.Errorf("expected metadata.memory_type 'LongTermMemory', got %v", meta["memory_type"])
	}
	// Embedding should be cleared
	if emb, ok := meta["embedding"].([]any); !ok || len(emb) != 0 {
		t.Errorf("expected embedding to be empty slice, got %v", meta["embedding"])
	}
}

// TestFormatMemoryBucket_InvalidJSON verifies that invalid JSON properties are skipped.
func TestFormatMemoryBucket_InvalidJSON(t *testing.T) {
	results := []map[string]any{
		{
			"memory_id":  "123",
			"properties": `{invalid json`,
		},
		{
			"memory_id":  "456",
			"properties": `{"id":"def-456","memory":"good memory","memory_type":"LongTermMemory"}`,
		},
	}

	bucket := formatMemoryBucket(results, "memos", 2)

	// Only the valid entry should be in memories
	if len(bucket.Memories) != 1 {
		t.Errorf("expected 1 memory (invalid skipped), got %d", len(bucket.Memories))
	}
}

// TestFormatMemoryBucket_Empty verifies empty results produce an empty bucket.
func TestFormatMemoryBucket_Empty(t *testing.T) {
	bucket := formatMemoryBucket(nil, "memos", 0)

	if bucket.CubeID != "memos" {
		t.Errorf("expected CubeID 'memos', got %q", bucket.CubeID)
	}
	if bucket.TotalNodes != 0 {
		t.Errorf("expected TotalNodes 0, got %d", bucket.TotalNodes)
	}
	if len(bucket.Memories) != 0 {
		t.Errorf("expected 0 memories, got %d", len(bucket.Memories))
	}
}

// setPostgresNonNil sets a non-nil but unusable postgres client on the handler.
// This allows testing validation paths that require postgres != nil without
// actually connecting to a database. Any DB query will panic.
func setPostgresNonNil(h *Handler) {
	h.postgres = db.NewStubPostgres()
}

// --- Cache helper tests (redis=nil graceful degradation) ---

func TestCacheGet_NilRedis(t *testing.T) {
	h := testValidateHandler() // redis is nil
	got := h.cacheGet(context.Background(), "memdb:db:test:key")
	if got != nil {
		t.Errorf("expected nil from cacheGet with nil redis, got %v", got)
	}
}

func TestCacheSet_NilRedis(t *testing.T) {
	h := testValidateHandler()
	// Should not panic
	h.cacheSet(context.Background(), "memdb:db:test:key", []byte("data"), 30*time.Second)
}

func TestCacheInvalidate_NilRedis(t *testing.T) {
	h := testValidateHandler()
	// Should not panic
	h.cacheInvalidate(context.Background(), "memdb:db:*")
}

// Integration tests with a real Redis would cover cache hit/miss paths.
// Unit tests verify graceful degradation when redis=nil (all handlers still work).
