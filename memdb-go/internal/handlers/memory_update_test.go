package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeUpdateMemory_MissingFields(t *testing.T) {
	h := testValidateHandler()
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing memory_id", map[string]any{"user_id": "a.com", "text": "hi"}},
		{"missing user_id", map[string]any{"memory_id": "abc", "text": "hi"}},
		{"missing text", map[string]any{"memory_id": "abc", "user_id": "a.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, _ := json.Marshal(c.body)
			req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.NativeUpdateMemory(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestNativeUpdateMemory_NoPostgres(t *testing.T) {
	h := testValidateHandler() // nil postgres + nil embedder
	payload, _ := json.Marshal(map[string]any{
		"memory_id": "abc", "user_id": "a.com", "text": "hi",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeUpdateMemory(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
