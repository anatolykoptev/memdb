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
// Implements all methods of the memoryUpdater interface with no-op CE cache stubs.
type stubMemoryUpdater struct {
	err error
}

func (s *stubMemoryUpdater) UpdateMemoryByID(_ context.Context, _, _ string, _ []byte, _ string) error {
	return s.err
}

func (s *stubMemoryUpdater) ClearCEScoresTopK(_ context.Context, _ string) error { return nil }
func (s *stubMemoryUpdater) ClearCEScoresTopKForNeighbor(_ context.Context, _ string) error {
	return nil
}

// captureUpdater records the cube + properties the handler writes, and
// implements memoryUserIDReader for the preservation path.
type captureUpdater struct {
	stubMemoryUpdater
	storedUserID string
	gotCube      string
	gotProps     []byte
}

func (c *captureUpdater) GetMemoryUserID(_ context.Context, _, _ string) (string, error) {
	return c.storedUserID, nil
}

func (c *captureUpdater) UpdateMemoryByID(_ context.Context, _, cube string, props []byte, _ string) error {
	c.gotCube, c.gotProps = cube, props
	return nil
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

func TestNativeUpdateMemory_WritableCubeIDs(t *testing.T) {
	h := testValidateHandler()
	h.embedder = &stubEmbedder{}
	setPostgresNonNil(h)
	up := &captureUpdater{}
	h.memUpdaterField = up

	payload, _ := json.Marshal(map[string]any{
		"memory_id": "abc", "user_id": "go-wowa",
		"writable_cube_ids": []string{"example.com"}, "text": "hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeUpdateMemory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if up.gotCube != "example.com" {
		t.Errorf("cube = %q, want example.com", up.gotCube)
	}
	var props map[string]any
	if err := json.Unmarshal(up.gotProps, &props); err != nil {
		t.Fatalf("props unmarshal: %v", err)
	}
	if props["user_id"] != "go-wowa" {
		t.Errorf("props user_id = %v, want go-wowa", props["user_id"])
	}
	if props["user_name"] != "example.com" {
		t.Errorf("props user_name = %v, want example.com", props["user_name"])
	}
}

func TestNativeUpdateMemory_PreservesStoredUserID(t *testing.T) {
	h := testValidateHandler()
	h.embedder = &stubEmbedder{}
	setPostgresNonNil(h)
	up := &captureUpdater{storedUserID: "go-wowa"}
	h.memUpdaterField = up

	payload, _ := json.Marshal(map[string]any{
		"memory_id": "abc", "user_id": "example.com", "text": "hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeUpdateMemory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var props map[string]any
	if err := json.Unmarshal(up.gotProps, &props); err != nil {
		t.Fatalf("props unmarshal: %v", err)
	}
	if props["user_id"] != "go-wowa" {
		t.Errorf("props user_id = %v, want preserved go-wowa", props["user_id"])
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
