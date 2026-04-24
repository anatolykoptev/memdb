package handlers

// chat_validation_test.go — request-level validation tests for chat endpoints.
// Currently covers the answer_style enum guard. Validation runs before any
// downstream service nil-checks, so the shared logger-only handler in
// add_validation_test.go is sufficient here too.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNativeChatComplete_UnknownAnswerStyle ensures unknown answer_style values
// are rejected with 400 BEFORE any backend call is attempted (which would otherwise
// nil-pointer-panic on the unconfigured handler).
func TestNativeChatComplete_UnknownAnswerStyle(t *testing.T) {
	h := &testHandlerWithLogger
	body := `{"user_id":"shared","query":"hello","answer_style":"loud"}`
	req := httptest.NewRequest(http.MethodPost, "/product/chat/complete", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeChatComplete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown answer_style, got %d: %s", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, "unknown answer_style") {
		t.Errorf("error body missing 'unknown answer_style': %s", got)
	}
	if !strings.Contains(got, "factual") || !strings.Contains(got, "conversational") {
		t.Errorf("error body must list valid values 'factual'/'conversational': %s", got)
	}
}

// TestNativeChatStream_UnknownAnswerStyle mirrors the complete endpoint guard;
// validation lives in the shared validateChatRequest helper, but we exercise both
// HTTP handlers to lock the contract at the API surface.
func TestNativeChatStream_UnknownAnswerStyle(t *testing.T) {
	h := &testHandlerWithLogger
	body := `{"user_id":"shared","query":"hello","answer_style":"verbose"}`
	req := httptest.NewRequest(http.MethodPost, "/product/chat/stream", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.NativeChatStream(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown answer_style, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown answer_style") {
		t.Errorf("error body missing 'unknown answer_style': %s", w.Body.String())
	}
}

// TestNativeChatComplete_AcceptedAnswerStyles ensures the three legal values
// (empty, "factual", "conversational") all pass validation and progress past
// the 400 guard. The handler may then panic / proxy on the unconfigured stack;
// we only assert no answer_style 400 fires.
func TestNativeChatComplete_AcceptedAnswerStyles(t *testing.T) {
	h := &testHandlerWithLogger
	for _, style := range []string{"", "factual", "conversational"} {
		t.Run("style="+style, func(t *testing.T) {
			body := `{"user_id":"shared","query":"hello","answer_style":"` + style + `"}`
			req := httptest.NewRequest(http.MethodPost, "/product/chat/complete", strings.NewReader(body))
			w := httptest.NewRecorder()

			defer func() {
				recover() // absorb downstream nil-pointer panic (no python client)
				if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "answer_style") {
					t.Errorf("style %q should not trigger answer_style 400, got: %s", style, w.Body.String())
				}
			}()

			h.NativeChatComplete(w, req)
		})
	}
}
