package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
