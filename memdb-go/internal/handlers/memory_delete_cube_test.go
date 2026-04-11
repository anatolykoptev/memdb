package handlers

// memory_delete_cube_test.go — cube scoping tests for NativeDelete memory_ids path.
//
// Covers:
//   1. resolveDeleteCubeIDs logic: writable_cube_ids takes precedence over user_id.
//   2. memory_ids path: validation rejects requests with no cube info.
//   3. Backward-compat: user_id alone (no writable_cube_ids) resolves to [user_id].

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- resolveDeleteCubeIDs unit tests ---

func TestResolveDeleteCubeIDs_WritableCubeIDsTakesPrecedence(t *testing.T) {
	userID := "shared"
	cubeIDs := []string{"cube-A", "cube-B"}
	req := &deleteNativeRequest{
		UserID:          &userID,
		WritableCubeIDs: &cubeIDs,
	}
	got := resolveDeleteCubeIDs(req)
	if len(got) != 2 || got[0] != "cube-A" || got[1] != "cube-B" {
		t.Errorf("expected [cube-A cube-B], got %v", got)
	}
}

func TestResolveDeleteCubeIDs_FallsBackToUserID(t *testing.T) {
	userID := "legacy-user"
	req := &deleteNativeRequest{
		UserID: &userID,
	}
	got := resolveDeleteCubeIDs(req)
	if len(got) != 1 || got[0] != "legacy-user" {
		t.Errorf("expected [legacy-user], got %v", got)
	}
}

func TestResolveDeleteCubeIDs_EmptyWritableCubeIDsFallsBackToUserID(t *testing.T) {
	userID := "legacy-user"
	empty := []string{}
	req := &deleteNativeRequest{
		UserID:          &userID,
		WritableCubeIDs: &empty,
	}
	got := resolveDeleteCubeIDs(req)
	if len(got) != 1 || got[0] != "legacy-user" {
		t.Errorf("expected [legacy-user] when writable_cube_ids is empty, got %v", got)
	}
}

func TestResolveDeleteCubeIDs_NilBothReturnsNil(t *testing.T) {
	req := &deleteNativeRequest{}
	got := resolveDeleteCubeIDs(req)
	if got != nil {
		t.Errorf("expected nil when no cube info provided, got %v", got)
	}
}

// --- NativeDelete memory_ids validation tests (pre-DB, with setPostgresNonNil) ---

// TestNativeDelete_MemoryIDs_MissingCube verifies that memory_ids without
// user_id or writable_cube_ids is rejected before any DB call.
func TestNativeDelete_MemoryIDs_MissingCube(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"memory_ids":["abc123"]}`
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

// TestNativeDelete_MemoryIDs_WritableCubeIDsPassesValidation verifies that
// writable_cube_ids is accepted for memory_ids delete. The DB call itself
// is not reached (StubPostgres will panic), but validation passes.
// This test confirms the cube routing logic runs without early rejection.
func TestNativeDelete_MemoryIDs_WritableCubeIDsPassesValidation(t *testing.T) {
	// We cannot drive the DB call without a real Postgres connection.
	// We verify that the request with writable_cube_ids passes all validation
	// gates up to the DB call by checking the handler does NOT return 400.
	// The stub postgres panics on actual query — recover to distinguish 400 vs panic.

	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"user_id":"shared","writable_cube_ids":["cube-A"],"memory_ids":["abc123"]}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			// Panic means we got past all validation into the DB call — correct behaviour.
			return
		}
		// No panic: check it was not a validation 400.
		if w.Code == http.StatusBadRequest {
			t.Errorf("unexpected 400 (validation rejected cube-scoped request): %s", w.Body.String())
		}
	}()

	h.NativeDelete(w, req)
}

// TestNativeDelete_MemoryIDs_UserIDFallbackPassesValidation verifies that
// user_id alone (no writable_cube_ids) still resolves correctly — backward compat.
func TestNativeDelete_MemoryIDs_UserIDFallbackPassesValidation(t *testing.T) {
	h := testValidateHandler()
	setPostgresNonNil(h)

	body := `{"user_id":"legacy-user","memory_ids":["abc123"]}`
	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(body))
	w := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			// Got past validation into DB call — correct.
			return
		}
		if w.Code == http.StatusBadRequest {
			t.Errorf("unexpected 400 (backward-compat user_id rejected): %s", w.Body.String())
		}
	}()

	h.NativeDelete(w, req)
}
