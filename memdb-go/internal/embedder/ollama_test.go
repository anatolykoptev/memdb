package embedder

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestOllamaClient_Embed verifies the happy path: server returns embeddings for all inputs.
func TestOllamaClient_Embed(t *testing.T) {
	want := [][]float32{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed, got %s", r.URL.Path)
		}
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(req.Input) != 2 {
			t.Errorf("expected 2 inputs, got %d", len(req.Input))
		}
		resp := ollamaEmbedResponse{Model: req.Model, Embeddings: want}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "nomic-embed-text", testLogger())
	got, err := c.Embed(context.Background(), []string{"hello", "world"})
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

// TestOllamaClient_EmptyInput verifies that empty input returns nil without HTTP call.
func TestOllamaClient_EmptyInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "nomic-embed-text", testLogger())
	got, err := c.Embed(context.Background(), nil)
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

// TestOllamaClient_ServerError verifies that non-200 responses return an error.
func TestOllamaClient_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "unknown-model", testLogger())
	_, err := c.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

// TestOllamaClient_CountMismatch verifies that mismatched embedding count returns error.
func TestOllamaClient_CountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaEmbedResponse{
			Model:      "nomic-embed-text",
			Embeddings: [][]float32{{0.1, 0.2}}, // only 1 embedding for 2 inputs
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "nomic-embed-text", testLogger())
	_, err := c.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for count mismatch")
	}
}

// TestOllamaClient_Dimension verifies default and overridden dimensions.
func TestOllamaClient_Dimension(t *testing.T) {
	c := NewOllamaClient("", "", testLogger())
	if c.Dimension() != ollamaDefaultDim {
		t.Errorf("default dim: want %d, got %d", ollamaDefaultDim, c.Dimension())
	}
	if ollamaDefaultDim != 1024 {
		t.Errorf("default dim must be 1024 to match pgvector schema, got %d", ollamaDefaultDim)
	}

	c2 := NewOllamaClient("", "", testLogger(), WithOllamaDimension(768))
	if c2.Dimension() != 768 {
		t.Errorf("override dim: want 768, got %d", c2.Dimension())
	}
}

// TestOllamaClient_Defaults verifies that empty URL/model use defaults.
func TestOllamaClient_Defaults(t *testing.T) {
	c := NewOllamaClient("", "", testLogger())
	if c.baseURL != ollamaDefaultURL {
		t.Errorf("default URL: want %q, got %q", ollamaDefaultURL, c.baseURL)
	}
	if c.model != ollamaDefaultModel {
		t.Errorf("default model: want %q, got %q", ollamaDefaultModel, c.model)
	}
}

// TestOllamaClient_TrailingSlash verifies that trailing slash in URL is stripped.
func TestOllamaClient_TrailingSlash(t *testing.T) {
	c := NewOllamaClient("http://ollama:11434/", "nomic-embed-text", testLogger())
	if c.baseURL != "http://ollama:11434" {
		t.Errorf("trailing slash not stripped: %q", c.baseURL)
	}
}

// TestOllamaClient_Timeout verifies that WithOllamaTimeout applies correctly.
func TestOllamaClient_Timeout(t *testing.T) {
	c := NewOllamaClient("", "", testLogger(), WithOllamaTimeout(5*time.Second))
	if c.httpClient.Timeout != 5*time.Second {
		t.Errorf("timeout: want 5s, got %v", c.httpClient.Timeout)
	}
}

// TestOllamaClient_Close verifies that Close is a no-op.
func TestOllamaClient_Close(t *testing.T) {
	c := NewOllamaClient("", "", testLogger())
	if err := c.Close(); err != nil {
		t.Errorf("Close should return nil, got %v", err)
	}
}

// TestOllamaClient_ImplementsEmbedder verifies compile-time interface compliance.
func TestOllamaClient_ImplementsEmbedder(t *testing.T) {
	var _ Embedder = (*OllamaClient)(nil)
}

// TestOllamaClient_TextPrefix verifies that WithTextPrefix prepends to every input.
func TestOllamaClient_TextPrefix(t *testing.T) {
	var capturedInputs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		capturedInputs = req.Input
		resp := ollamaEmbedResponse{
			Embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "test", testLogger(), WithTextPrefix("passage: "))
	_, err := c.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(capturedInputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(capturedInputs))
	}
	if capturedInputs[0] != "passage: hello" {
		t.Errorf("input[0]: want %q, got %q", "passage: hello", capturedInputs[0])
	}
	if capturedInputs[1] != "passage: world" {
		t.Errorf("input[1]: want %q, got %q", "passage: world", capturedInputs[1])
	}
}

// TestOllamaClient_EmptyPrefix verifies that empty prefix sends texts unchanged.
func TestOllamaClient_EmptyPrefix(t *testing.T) {
	var capturedInputs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaEmbedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		capturedInputs = req.Input
		resp := ollamaEmbedResponse{Embeddings: [][]float32{{0.1}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "test", testLogger(), WithTextPrefix(""))
	_, err := c.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if capturedInputs[0] != "hello" {
		t.Errorf("empty prefix should not modify input: got %q", capturedInputs[0])
	}
}

// TestOllamaClient_NormalizeL2 verifies that WithNormalizeL2 produces unit vectors.
func TestOllamaClient_NormalizeL2(t *testing.T) {
	// Return a non-normalized vector [3, 4] — L2 norm = 5, normalized = [0.6, 0.8]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaEmbedResponse{
			Embeddings: [][]float32{{3.0, 4.0}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "test", testLogger(), WithNormalizeL2(true))
	got, err := c.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}

	vec := got[0]
	// Check unit length: sum of squares should be ~1.0
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	if sumSq < 0.999 || sumSq > 1.001 {
		t.Errorf("L2 norm: want ~1.0, got %f (vec=%v)", sumSq, vec)
	}
	// Check values: [3/5, 4/5] = [0.6, 0.8]
	if vec[0] < 0.599 || vec[0] > 0.601 {
		t.Errorf("vec[0]: want ~0.6, got %f", vec[0])
	}
	if vec[1] < 0.799 || vec[1] > 0.801 {
		t.Errorf("vec[1]: want ~0.8, got %f", vec[1])
	}
}

// TestOllamaClient_NormalizeL2_Disabled verifies that without WithNormalizeL2
// the raw (non-unit) vector is returned as-is.
func TestOllamaClient_NormalizeL2_Disabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaEmbedResponse{
			Embeddings: [][]float32{{3.0, 4.0}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(srv.URL, "test", testLogger()) // no WithNormalizeL2
	got, err := c.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if got[0][0] != 3.0 || got[0][1] != 4.0 {
		t.Errorf("without normalize: want [3 4], got %v", got[0])
	}
}
