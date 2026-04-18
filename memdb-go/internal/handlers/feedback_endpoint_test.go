package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidatedFeedback_Validation(t *testing.T) {
	h := &Handler{}
	tests := []struct {
		name string
		body string
		code int
	}{
		{"missing user_id", `{"feedback_content":"x","history":[]}`, http.StatusBadRequest},
		{"missing content", `{"user_id":"u","history":[]}`, http.StatusBadRequest},
		{"missing history", `{"user_id":"u","feedback_content":"x"}`, http.StatusBadRequest},
		{"bad history JSON", `{"user_id":"u","feedback_content":"x","history":{"not":"array"}}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/product/feedback", bytes.NewBufferString(tt.body))
			w := httptest.NewRecorder()
			h.ValidatedFeedback(w, r)
			if w.Code != tt.code {
				t.Fatalf("want %d, got %d: %s", tt.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestValidatedFeedback_BackendNotReady(t *testing.T) {
	h := handlerForWiringTest(t, false)
	body := `{"user_id":"u","feedback_content":"x","history":[]}`
	r := httptest.NewRequest("POST", "/product/feedback", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ValidatedFeedback(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != float64(503) {
		t.Fatalf("want code=503 in envelope, got %v", resp["code"])
	}
}
