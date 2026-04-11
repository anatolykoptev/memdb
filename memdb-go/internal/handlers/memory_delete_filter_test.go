package handlers

// memory_delete_filter_test.go — validation-layer tests for NativeDelete
// routing between memory_ids / file_ids / filter branches. The native
// file_ids and filter paths ultimately hit Postgres, which cannot be mocked
// without a DB (Handler.postgres is a concrete *db.Postgres). These tests
// therefore cover everything that happens BEFORE the postgres call:
// mutual-exclusion enforcement, cube_id resolution, and filter parsing.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNativeDelete_FileIDs_FilterMutualExclusion verifies the "exactly one of"
// rule: supplying both file_ids and filter in one request is rejected.
func TestNativeDelete_FileIDs_FilterMutualExclusion(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"user_id":"memos","file_ids":["f1"],"filter":{"tags":"a"}}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exactly one of") {
		t.Errorf("expected mutual-exclusion error, got: %s", w.Body.String())
	}
}

// TestNativeDelete_MemoryIDs_Filter_MutualExclusion verifies memory_ids +
// filter is also rejected.
func TestNativeDelete_MemoryIDs_Filter_MutualExclusion(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"user_id":"memos","memory_ids":["m1"],"filter":{"tags":"a"}}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exactly one of") {
		t.Errorf("expected mutual-exclusion error, got: %s", w.Body.String())
	}
}

// TestNativeDelete_MemoryIDs_FileIDs_MutualExclusion verifies memory_ids +
// file_ids is also rejected.
func TestNativeDelete_MemoryIDs_FileIDs_MutualExclusion(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"user_id":"memos","memory_ids":["m1"],"file_ids":["f1"]}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestNativeDelete_Filter_InvalidJSON verifies invalid filter shapes return
// 400 with a descriptive error before any DB call.
func TestNativeDelete_Filter_InvalidJSON(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	// Top-level "and" combined with other keys is rejected by filter.Parse.
	body := `{"user_id":"memos","filter":{"and":[{"tags":"a"}],"tags":"b"}}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid filter") {
		t.Errorf("expected invalid filter error, got: %s", w.Body.String())
	}
}

// TestNativeDelete_Filter_DisallowedField verifies fields outside the
// allowlist are rejected (e.g. "password").
func TestNativeDelete_Filter_DisallowedField(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"user_id":"memos","filter":{"password":"x"}}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid filter") {
		t.Errorf("expected invalid filter error, got: %s", w.Body.String())
	}
}

// TestNativeDelete_FileIDs_MissingCube verifies file_ids without user_id or
// writable_cube_ids is rejected before any DB call.
func TestNativeDelete_FileIDs_MissingCube(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"file_ids":["f1"]}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "writable_cube_ids") {
		t.Errorf("expected cube-id validation error, got: %s", w.Body.String())
	}
}

// TestNativeDelete_Filter_MissingCube verifies filter without user_id or
// writable_cube_ids is rejected before any DB call.
func TestNativeDelete_Filter_MissingCube(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"filter":{"tags":"a"}}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "writable_cube_ids") {
		t.Errorf("expected cube-id validation error, got: %s", w.Body.String())
	}
}
