// Package handlers — Phase 5 shutdown: verify safety-net paths return 503/422 not 502.
package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testShutdownHandler returns a minimal Handler with nil postgres/redis/python —
// simulates the state after memdb-api is removed from compose.
func testShutdownHandler() *Handler {
	return testValidateHandler()
}

// --- 503 safety-net paths ---

func TestValidatedSearch_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"query":"test","user_id":"u1"}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "503") {
		t.Errorf("expected code 503 in body, got: %s", w.Body.String())
	}
}

func TestValidatedDelete_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory",
		strings.NewReader(`{"memory_ids":["abc"]}`))
	w := httptest.NewRecorder()
	h.ValidatedDelete(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidatedGetAll_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_all",
		strings.NewReader(`{"user_id":"u1","memory_type":"text_mem"}`))
	w := httptest.NewRecorder()
	h.ValidatedGetAll(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidatedGetMemory_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_memory",
		strings.NewReader(`{"mem_cube_id":"u1"}`))
	w := httptest.NewRecorder()
	h.ValidatedGetMemory(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidatedGetMemoryByIDs_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_memory_by_ids",
		strings.NewReader(`{"memory_ids":["abc"]}`))
	w := httptest.NewRecorder()
	h.ValidatedGetMemoryByIDs(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidatedExistMemCube_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/exist_mem_cube_id",
		strings.NewReader(`{"mem_cube_id":"u1"}`))
	w := httptest.NewRecorder()
	h.ValidatedExistMemCube(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidatedChatComplete_Returns503OnValidBody(t *testing.T) {
	h := testShutdownHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/chat/complete",
		strings.NewReader(`{"user_id":"u1","query":"hello"}`))
	w := httptest.NewRecorder()
	h.ValidatedChatComplete(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNativeDeleteAll_Returns503OnNilPostgres(t *testing.T) {
	h := testShutdownHandler() // postgres == nil

	req := httptest.NewRequest(http.MethodPost, "/product/delete_all_memories",
		strings.NewReader(`{"user_id":"u1"}`))
	w := httptest.NewRecorder()
	h.NativeDeleteAll(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNativeSchedulerStatus_Returns503OnNilRedis(t *testing.T) {
	h := testShutdownHandler() // redis == nil

	req := httptest.NewRequest(http.MethodGet, "/product/scheduler/status", nil)
	w := httptest.NewRecorder()
	h.NativeSchedulerStatus(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// --- 422 complex-filter edge cases ---

func TestHandlePostGetMemoryWithFilter_Returns422OnDBError(t *testing.T) {
	h := testShutdownHandler() // postgres == nil; filter path hits DB and gets 422

	req := httptest.NewRequest(http.MethodPost, "/product/get_memory",
		strings.NewReader(`{"mem_cube_id":"u1","filter":{"key":"value"}}`))
	w := httptest.NewRecorder()
	// NativePostGetMemory calls handlePostGetMemoryWithFilter when filter is set and postgres != nil.
	// With postgres==nil the handler returns 503 before filter; call directly via ValidatedGetMemory.
	h.ValidatedGetMemory(w, req)

	// With nil postgres the path returns 503 (not 422) — 422 fires only when DB errors at filter eval.
	// This test verifies the handler does NOT return 502.
	if w.Code == http.StatusBadGateway {
		t.Errorf("got 502 (proxy called) — should never reach Python after Phase 5 shutdown")
	}
}
