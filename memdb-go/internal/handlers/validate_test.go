package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testHandler creates a Handler with nil python client (fine for validation-only tests
// since validation errors return before proxy is called).
func testValidateHandler() *Handler {
	return &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// --- Search validation tests ---

func TestValidatedSearch_MissingBody(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search", nil)
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request body is required") {
		t.Errorf("expected body-required error, got: %s", w.Body.String())
	}
}

func TestValidatedSearch_InvalidJSON(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("expected JSON error, got: %s", w.Body.String())
	}
}

func TestValidatedSearch_MissingRequiredFields(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "query is required") {
		t.Errorf("expected query error, got: %s", body)
	}
	if !strings.Contains(body, "user_id is required") {
		t.Errorf("expected user_id error, got: %s", body)
	}
}

func TestValidatedSearch_EmptyQuery(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"query":"  ","user_id":"memos"}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "query is required") {
		t.Errorf("expected query error, got: %s", w.Body.String())
	}
}

func TestValidatedSearch_InvalidTopK(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"query":"test","user_id":"memos","top_k":0}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "top_k must be >= 1") {
		t.Errorf("expected top_k error, got: %s", w.Body.String())
	}
}

func TestValidatedSearch_InvalidDedup(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"query":"test","user_id":"memos","dedup":"invalid"}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "dedup must be one of") {
		t.Errorf("expected dedup error, got: %s", w.Body.String())
	}
}

func TestValidatedSearch_NegativeRelativity(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"query":"test","user_id":"memos","relativity":-1}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "relativity must be >= 0") {
		t.Errorf("expected relativity error, got: %s", w.Body.String())
	}
}

func TestValidatedSearch_NegativePrefTopK(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"query":"test","user_id":"memos","pref_top_k":-1}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pref_top_k must be >= 0") {
		t.Errorf("expected pref_top_k error, got: %s", w.Body.String())
	}
}

// --- Add validation tests ---

func TestNativeAdd_Validation_MissingUserID(t *testing.T) {
	h := testValidateHandler()

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

func TestNativeAdd_Validation_InvalidAsyncMode(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"memos","async_mode":"bad"}`))
	w := httptest.NewRecorder()
	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "async_mode must be one of") {
		t.Errorf("expected async_mode error, got: %s", w.Body.String())
	}
}

func TestNativeAdd_Validation_InvalidMode(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"memos","mode":"turbo"}`))
	w := httptest.NewRecorder()
	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mode must be one of") {
		t.Errorf("expected mode error, got: %s", w.Body.String())
	}
}

// --- Feedback validation tests ---

func TestValidatedFeedback_MissingFields(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/feedback", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedFeedback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "user_id is required") {
		t.Errorf("expected user_id error, got: %s", body)
	}
	if !strings.Contains(body, "feedback_content is required") {
		t.Errorf("expected feedback_content error, got: %s", body)
	}
	if !strings.Contains(body, "history is required") {
		t.Errorf("expected history error, got: %s", body)
	}
}

// --- Delete validation tests ---

func TestValidatedDelete_NoTargets(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "at least one of") {
		t.Errorf("expected target error, got: %s", w.Body.String())
	}
}

func TestValidatedDelete_EmptyArrays(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/delete_memory",
		strings.NewReader(`{"memory_ids":[],"file_ids":[]}`))
	w := httptest.NewRecorder()
	h.ValidatedDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- GetAll validation tests ---

func TestValidatedGetAll_MissingFields(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_all", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedGetAll(w, req)

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

func TestValidatedGetAll_InvalidMemoryType(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_all",
		strings.NewReader(`{"user_id":"memos","memory_type":"bad_type"}`))
	w := httptest.NewRecorder()
	h.ValidatedGetAll(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "memory_type must be one of") {
		t.Errorf("expected memory_type enum error, got: %s", w.Body.String())
	}
}

// --- Chat validation tests ---

func TestValidatedChat_MissingFields(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/chat", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedChat(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "user_id is required") {
		t.Errorf("expected user_id error, got: %s", body)
	}
	if !strings.Contains(body, "query is required") {
		t.Errorf("expected query error, got: %s", body)
	}
}

func TestValidatedChatComplete_InvalidTopK(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/chat/complete",
		strings.NewReader(`{"user_id":"memos","query":"hello","top_k":-5}`))
	w := httptest.NewRecorder()
	h.ValidatedChatComplete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "top_k must be >= 1") {
		t.Errorf("expected top_k error, got: %s", w.Body.String())
	}
}

// --- GetMemoryByIDs validation tests ---

func TestValidatedGetMemoryByIDs_Missing(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_memory_by_ids",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedGetMemoryByIDs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "memory_ids is required") {
		t.Errorf("expected memory_ids error, got: %s", w.Body.String())
	}
}

func TestValidatedGetMemoryByIDs_EmptyArray(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/get_memory_by_ids",
		strings.NewReader(`{"memory_ids":[]}`))
	w := httptest.NewRecorder()
	h.ValidatedGetMemoryByIDs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- ExistMemCube validation tests ---

func TestValidatedExistMemCube_Missing(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/exist_mem_cube_id",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ValidatedExistMemCube(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mem_cube_id is required") {
		t.Errorf("expected mem_cube_id error, got: %s", w.Body.String())
	}
}

// --- Helper tests ---

func TestReadBody_EmptyBody(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(""))
	w := httptest.NewRecorder()
	_, ok := h.readBody(w, req)

	if ok {
		t.Error("expected readBody to return false for empty body")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestReadBody_NilBody(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	w := httptest.NewRecorder()
	_, ok := h.readBody(w, req)

	if ok {
		t.Error("expected readBody to return false for nil body")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestDecodeJSON_Invalid(t *testing.T) {
	h := testValidateHandler()

	w := httptest.NewRecorder()
	var dst searchRequest
	ok := h.decodeJSON(w, []byte("not json"), &dst)

	if ok {
		t.Error("expected decodeJSON to return false for invalid JSON")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- Multiple errors accumulated in one response ---

func TestValidatedSearch_MultipleErrors(t *testing.T) {
	h := testValidateHandler()

	req := httptest.NewRequest(http.MethodPost, "/product/search",
		strings.NewReader(`{"top_k":0,"dedup":"bad","relativity":-1}`))
	w := httptest.NewRecorder()
	h.ValidatedSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "query is required") {
		t.Errorf("missing query error in: %s", body)
	}
	if !strings.Contains(body, "user_id is required") {
		t.Errorf("missing user_id error in: %s", body)
	}
	if !strings.Contains(body, "top_k must be >= 1") {
		t.Errorf("missing top_k error in: %s", body)
	}
	if !strings.Contains(body, "dedup must be one of") {
		t.Errorf("missing dedup error in: %s", body)
	}
	if !strings.Contains(body, "relativity must be >= 0") {
		t.Errorf("missing relativity error in: %s", body)
	}
}
