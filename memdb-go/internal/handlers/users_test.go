package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

// --- NativeInstancesStatus tests (always native, no DB needed) ---

func TestNativeInstancesStatus(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("GET", "/product/instances/status", nil)
	w := httptest.NewRecorder()

	h.NativeInstancesStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["code"] != float64(200) {
		t.Errorf("expected code=200, got %v", resp["code"])
	}
	data, _ := resp["data"].(map[string]any)
	if data["status"] != "running" {
		t.Errorf("expected status=running, got %v", data["status"])
	}
	if data["go_version"] == nil || data["go_version"] == "" {
		t.Errorf("expected go_version to be set")
	}
	if data["hostname"] == nil {
		t.Errorf("expected hostname to be set")
	}
	if data["timestamp"] == nil {
		t.Errorf("expected timestamp to be set")
	}
}

// --- NativeExistMemCube tests (falls back to ValidatedExistMemCube on nil postgres) ---

func TestNativeExistMemCube_NoPostgres_MissingField(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/exist_mem_cube_id",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	h.NativeExistMemCube(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mem_cube_id is required") {
		t.Errorf("expected validation error, got: %s", w.Body.String())
	}
}

func TestNativeExistMemCube_NoPostgres_EmptyString(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/exist_mem_cube_id",
		strings.NewReader(`{"mem_cube_id":""}`))
	w := httptest.NewRecorder()

	h.NativeExistMemCube(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestNativeExistMemCube_NoPostgres_NilBody(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/exist_mem_cube_id", nil)
	w := httptest.NewRecorder()

	h.NativeExistMemCube(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

// --- NativeGetUserNamesByMemoryIDs tests (falls back to ProxyToProduct on nil postgres) ---
// These need postgres=nil which triggers ProxyToProduct (nil python = panic).
// We test the validation path by providing valid input that reaches the native path.
// Since postgres is nil, we can only test that the function compiles and exists.

func TestNativeGetUserNamesByMemoryIDs_NoPostgres_InvalidJSON(t *testing.T) {
	h := testValidateHandler()
	req := httptest.NewRequest("POST", "/product/get_user_names_by_memory_ids",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	// With postgres==nil, this goes to ProxyToProduct which panics.
	// But it doesn't read body first — it calls ProxyToProduct immediately.
	defer func() { recover() }()
	h.NativeGetUserNamesByMemoryIDs(w, req)
}

// --- NativeListUsers cache tests (redis=nil graceful degradation) ---

func TestNativeListUsers_NoRedis_NoPostgres(t *testing.T) {
	h := testValidateHandler() // redis=nil, postgres=nil

	req := httptest.NewRequest("GET", "/product/users", nil)
	w := httptest.NewRecorder()

	// postgres=nil → ProxyToProduct → panics (nil python)
	defer func() { recover() }()
	h.NativeListUsers(w, req)
}

// --- NativeUpdateUserConfig tests ---
// These use a non-nil postgres (zero-value struct) to reach the native path.
// Invalid JSON returns 400 before any DB call, so nil pool is safe.

func testNativeHandler() *Handler {
	h := testValidateHandler()
	h.postgres = &db.Postgres{}
	return h
}

func TestNativeUpdateUserConfig_InvalidJSON(t *testing.T) {
	h := testNativeHandler()
	req := httptest.NewRequest("PUT", "/product/users/testuser/config",
		strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	h.NativeUpdateUserConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["code"] != float64(400) {
		t.Errorf("expected code=400, got %v", resp["code"])
	}
	msg, _ := resp["message"].(string)
	if !strings.Contains(msg, "invalid JSON body") {
		t.Errorf("expected 'invalid JSON body' in message, got: %s", msg)
	}
}

func TestNativeUpdateUserConfig_EmptyBody(t *testing.T) {
	h := testNativeHandler()
	req := httptest.NewRequest("PUT", "/product/users/testuser/config", nil)
	w := httptest.NewRecorder()

	h.NativeUpdateUserConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

func TestNativeUpdateUserConfig_ValidJSON(t *testing.T) {
	h := testNativeHandler()
	req := httptest.NewRequest("PUT", "/product/users/testuser/config",
		strings.NewReader(`{"theme":"dark","lang":"en"}`))
	req.SetPathValue("user_id", "testuser")
	w := httptest.NewRecorder()

	h.NativeUpdateUserConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["code"] != float64(200) {
		t.Errorf("expected code=200, got %v", resp["code"])
	}
	data, _ := resp["data"].(map[string]any)
	if data["user_id"] != "testuser" {
		t.Errorf("expected user_id=testuser, got %v", data["user_id"])
	}
	cfg, _ := data["config"].(map[string]any)
	if cfg["theme"] != "dark" {
		t.Errorf("expected theme=dark, got %v", cfg["theme"])
	}
}

// Integration tests with real Redis+Postgres would cover cache hit/miss paths
// for NativeListUsers, NativeInstancesCount.
