package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeFeedback_BackendNotReady(t *testing.T) {
	h := &Handler{logger: slog.Default()}
	req := httptest.NewRequest("POST", "/product/feedback", bytes.NewBufferString(`{"user_id":"u","feedback_content":"x","history":[]}`))
	w := httptest.NewRecorder()
	h.NativeFeedback(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (no postgres/embedder/llm), got %d: %s", w.Code, w.Body.String())
	}
}

// TestValidateFeedbackRequest exercises the validation branches in isolation,
// bypassing the canNative gate (which blocks on nil dependencies).
func TestValidateFeedbackRequest(t *testing.T) {
	hist := json.RawMessage(`[]`)
	mk := func(user, content string, withHist bool) feedbackRequest {
		r := feedbackRequest{}
		if user != "" {
			r.UserID = &user
		}
		if content != "" {
			r.FeedbackContent = &content
		}
		if withHist {
			r.History = &hist
		}
		return r
	}
	tests := []struct {
		name    string
		req     feedbackRequest
		wantErr string
	}{
		{"missing user_id", mk("", "fb", true), "user_id is required"},
		{"missing content", mk("u", "", true), "feedback_content is required"},
		{"missing history", mk("u", "fb", false), "history is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateFeedbackRequest(tt.req)
			if len(errs) == 0 {
				t.Fatalf("expected validation error, got none")
			}
			found := false
			for _, e := range errs {
				if e == tt.wantErr {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected error %q in %v", tt.wantErr, errs)
			}
		})
	}

	t.Run("valid request", func(t *testing.T) {
		if errs := validateFeedbackRequest(mk("u", "fb", true)); len(errs) != 0 {
			t.Fatalf("expected no errors, got %v", errs)
		}
	})
}
