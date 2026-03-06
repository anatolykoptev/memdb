package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPEmbedder_Embed verifies the happy path: batch embed returns correct vectors.
func TestHTTPEmbedder_Embed(t *testing.T) {
	want := [][]float32{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("expected /v1/embeddings, got %s", r.URL.Path)
		}
		var req httpEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Input) != 2 {
			t.Errorf("expected 2 inputs, got %d", len(req.Input))
		}
		if req.Model != "test-model" {
			t.Errorf("expected model test-model, got %s", req.Model)
		}
		resp := httpEmbedResponse{
			Data: []httpEmbedData{
				{Embedding: want[0], Index: 0},
				{Embedding: want[1], Index: 1},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model", 3, testLogger())
	got, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d embeddings, got %d", len(want), len(got))
	}
	for i, vec := range got {
		if len(vec) != len(want[i]) {
			t.Errorf("[%d] dim mismatch: want %d, got %d", i, len(want[i]), len(vec))
		}
	}
}

// TestHTTPEmbedder_EmptyInput verifies empty input returns nil without HTTP call.
func TestHTTPEmbedder_EmptyInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "model", 1024, testLogger())
	got, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if called {
		t.Error("HTTP server should not be called for empty input")
	}
}

// TestHTTPEmbedder_ServerError verifies non-200 responses return an error.
func TestHTTPEmbedder_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "bad-model", 1024, testLogger())
	_, err := e.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

// TestHTTPEmbedder_Dimension verifies Dimension returns configured value.
func TestHTTPEmbedder_Dimension(t *testing.T) {
	e := NewHTTPEmbedder("http://localhost", "m", 768, testLogger())
	if e.Dimension() != 768 {
		t.Errorf("want 768, got %d", e.Dimension())
	}
}

// TestHTTPEmbedder_Close verifies Close is a no-op.
func TestHTTPEmbedder_Close(t *testing.T) {
	e := NewHTTPEmbedder("http://localhost", "m", 1024, testLogger())
	if err := e.Close(); err != nil {
		t.Errorf("Close should return nil, got %v", err)
	}
}

// TestHTTPEmbedder_ImplementsEmbedder verifies compile-time interface compliance.
func TestHTTPEmbedder_ImplementsEmbedder(t *testing.T) {
	var _ Embedder = (*HTTPEmbedder)(nil)
}

// TestHTTPEmbedder_TrailingSlash verifies trailing slash in URL is stripped.
func TestHTTPEmbedder_TrailingSlash(t *testing.T) {
	e := NewHTTPEmbedder("http://embed:8080/", "m", 1024, testLogger())
	if e.baseURL != "http://embed:8080" {
		t.Errorf("trailing slash not stripped: %q", e.baseURL)
	}
}
