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

// TestHandlePostGetMemoryWithFilter_Returns503BeforeDBOnNilPostgres documents a known
// coverage gap: the 422 branch inside handlePostGetMemoryWithFilter fires only when
// h.postgres.GetMemoriesByFilter returns a non-nil error. Exercising that path requires
// a live (or mocked) pgx pool that returns an error, which is not available in unit tests
// because db.Postgres uses a concrete struct with an unexported pool field (no interface to stub).
//
// The DB-error fallback is covered by integration / live-PG tests. This note replaces the previous
// misleading test that only asserted != 502 while never actually reaching the filter DB path
// (nil postgres → 503 was returned before the filter path was entered).
//
// To add a real unit test here: extract a postgresFilterer interface from db.Postgres
// (GetMemoriesByFilter method), inject it into Handler, and provide a failFilterStore stub.
func TestHandlePostGetMemoryWithFilter_Returns503BeforeDBOnNilPostgres(t *testing.T) {
	h := testShutdownHandler() // postgres == nil

	// With postgres==nil NativePostGetMemory falls through to ValidatedGetMemory
	// which returns 503 — the filter DB path is never reached.
	req := httptest.NewRequest(http.MethodPost, "/product/get_memory",
		strings.NewReader(`{"mem_cube_id":"u1","filter":{"type":{"=":"LongTermMemory"}}}`))
	w := httptest.NewRecorder()
	h.NativePostGetMemory(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on nil postgres, got %d: %s", w.Code, w.Body.String())
	}
	if w.Code == http.StatusBadGateway {
		t.Errorf("got 502 — python proxy must never be called after Phase 5 shutdown")
	}
}
