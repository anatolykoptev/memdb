package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// stubMemoryUpdater is a test double for memoryUpdater.
type stubMemoryUpdater struct {
	err error
}

func (s *stubMemoryUpdater) UpdateMemoryByID(_ context.Context, _, _ string, _ []byte, _ string) error {
	return s.err
}

func TestNativeUpdateMemory_NotFound_Returns404(t *testing.T) {
	h := testValidateHandler()
	h.embedder = &stubEmbedder{}
	setPostgresNonNil(h) // satisfies postgres != nil guard; actual DB call is intercepted by memUpdaterField
	h.memUpdaterField = &stubMemoryUpdater{
		err: fmt.Errorf("%w: id=abc cube=a.com", db.ErrMemoryNotFound),
	}

	payload, _ := json.Marshal(map[string]any{
		"memory_id": "abc", "user_id": "a.com", "text": "hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeUpdateMemory(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "memory not found") {
		t.Errorf("body = %s, want 'memory not found'", w.Body.String())
	}
}

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
