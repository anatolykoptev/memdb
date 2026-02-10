package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
