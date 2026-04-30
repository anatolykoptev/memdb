package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testEmbedRequest / testEmbedResponse are wire types for the fake embed-server
// used in tests. The internal request/response types now live in go-kit/embed,
// so we define equivalent local structs for test assertions only.
type testEmbedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type testEmbedResponse struct {
	Data []testEmbedData `json:"data"`
}

type testEmbedData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

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
		var req testEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Input) != 2 {
			t.Errorf("expected 2 inputs, got %d", len(req.Input))
		}
		if req.Model != "test-model" {
			t.Errorf("expected model test-model, got %s", req.Model)
		}
		resp := testEmbedResponse{
			Data: []testEmbedData{
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

// TestHTTPEmbedder_DimMismatch verifies G7-style validation: a server that
// returns vectors of unexpected dimension surfaces a typed *DimMismatchError
// instead of silently corrupting downstream pgvector writes.
func TestHTTPEmbedder_DimMismatch(t *testing.T) {
	// Server returns 5-dim vectors; embedder configured for 3 → must error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testEmbedResponse{
			Data: []testEmbedData{
				{Embedding: []float32{0.1, 0.2, 0.3, 0.4, 0.5}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "rogue-model", 3, testLogger())
	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected DimMismatchError, got nil")
	}
	dme, ok := err.(*DimMismatchError)
	if !ok {
		t.Fatalf("want *DimMismatchError, got %T (%v)", err, err)
	}
	if dme.Got != 5 || dme.Want != 3 {
		t.Errorf("want got=5 want=3, got got=%d want=%d", dme.Got, dme.Want)
	}
	if dme.Model != "rogue-model" {
		t.Errorf("want model=rogue-model, got %q", dme.Model)
	}
	if dme.Index != 0 {
		t.Errorf("want index=0, got %d", dme.Index)
	}
}

// TestHTTPEmbedder_DimZeroDisablesCheck verifies that dim=0 (auto-detect
// convention from go-kit) skips validation, so callers that don't know
// the dim ahead of time aren't blocked.
func TestHTTPEmbedder_DimZeroDisablesCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testEmbedResponse{
			Data: []testEmbedData{
				{Embedding: []float32{0.1, 0.2, 0.3, 0.4}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "any", 0, testLogger())
	got, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error with dim=0: %v", err)
	}
	if len(got) != 1 || len(got[0]) != 4 {
		t.Errorf("unexpected result shape: %v", got)
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

// TestHTTPEmbedder_Migration_Parity verifies that the go-kit/embed delegate produces
// the same output as the previous direct implementation for a known embed response.
// This is the parity test ensuring the foundation migration is behaviour-equivalent.
func TestHTTPEmbedder_Migration_Parity(t *testing.T) {
	want := [][]float32{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
		{0.7, 0.8, 0.9},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testEmbedResponse{
			Data: []testEmbedData{
				{Embedding: want[0], Index: 0},
				{Embedding: want[1], Index: 1},
				{Embedding: want[2], Index: 2},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "multilingual-e5-large", 3, testLogger())
	got, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("parity: Embed error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("parity: len mismatch: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Errorf("parity: [%d] dim mismatch: want %d, got %d", i, len(want[i]), len(got[i]))
			continue
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("parity: [%d][%d] = %f, want %f", i, j, got[i][j], want[i][j])
			}
		}
	}
}
