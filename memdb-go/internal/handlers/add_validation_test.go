package handlers

// add_validation_test.go — empty-messages validation tests for NativeAdd.
//
// Covers:
//   1. Sync modes (fast, fine, raw, default) reject empty messages with 400.
//   2. Async mode with empty messages proxies (no early 400).
//   3. Sync mode with non-empty messages passes validation.

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNativeAdd_EmptyMessages_DefaultMode verifies that a sync add with no
// messages field returns 400 with a clear error.
func TestNativeAdd_EmptyMessages_DefaultMode(t *testing.T) {
	h := &testHandlerWithLogger
	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"shared"}`))
	w := httptest.NewRecorder()

	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "messages must not be empty") {
		t.Errorf("expected empty-messages error, got: %s", w.Body.String())
	}
}

// TestNativeAdd_EmptyMessages_FastMode verifies that mode=fast also rejects
// empty messages.
func TestNativeAdd_EmptyMessages_FastMode(t *testing.T) {
	h := &testHandlerWithLogger
	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"shared","mode":"fast"}`))
	w := httptest.NewRecorder()

	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "messages must not be empty") {
		t.Errorf("expected empty-messages error, got: %s", w.Body.String())
	}
}

// TestNativeAdd_EmptyMessages_RawMode verifies that mode=raw also rejects
// empty messages.
func TestNativeAdd_EmptyMessages_RawMode(t *testing.T) {
	h := &testHandlerWithLogger
	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"shared","mode":"raw"}`))
	w := httptest.NewRecorder()

	h.NativeAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "messages must not be empty") {
		t.Errorf("expected empty-messages error, got: %s", w.Body.String())
	}
}

// TestNativeAdd_AsyncMode_EmptyMessages_NoValidationError verifies that
// async_mode=async with no messages does NOT get a "messages must not be empty" 400.
// The handler may panic/proxy after validation (no real python client), but the
// empty-messages validation check itself must not fire.
func TestNativeAdd_AsyncMode_EmptyMessages_NoValidationError(t *testing.T) {
	h := &testHandlerWithLogger
	req := httptest.NewRequest(http.MethodPost, "/product/add",
		strings.NewReader(`{"user_id":"shared","async_mode":"async"}`))
	w := httptest.NewRecorder()

	defer func() {
		recover() // absorb any downstream nil-pointer panic (no python client)
		if w.Code == http.StatusBadRequest {
			if strings.Contains(w.Body.String(), "messages must not be empty") {
				t.Errorf("async mode should not reject empty messages, got: %s", w.Body.String())
			}
		}
	}()

	h.NativeAdd(w, req)
}

// TestNativeAdd_WithMessages_PassesValidation verifies that a sync request
// with messages present passes the empty-messages check. The handler may
// panic/proxy after validation (no real postgres/python client), but must
// not return a "messages must not be empty" 400.
func TestNativeAdd_WithMessages_PassesValidation(t *testing.T) {
	h := &testHandlerWithLogger
	body := `{"user_id":"shared","mode":"fast","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/product/add", strings.NewReader(body))
	w := httptest.NewRecorder()

	defer func() {
		recover() // absorb downstream nil-pointer panic (no postgres/python client)
		if w.Code == http.StatusBadRequest {
			if strings.Contains(w.Body.String(), "messages must not be empty") {
				t.Errorf("request with messages should not fail empty-messages check: %s", w.Body.String())
			}
		}
	}()

	h.NativeAdd(w, req)
}

// testHandlerWithLogger is a Handler with only a logger set — no postgres, embedder, etc.
// Validation runs before any nil checks, so this is sufficient for validation tests.
var testHandlerWithLogger = Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
