package embedder

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// redirectTransport intercepts outgoing requests and sends them to a local
// test server instead of the real VoyageAI endpoint.
type redirectTransport struct {
	targetURL string
	base      http.RoundTripper
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parsed := strings.TrimPrefix(t.targetURL, "http://")
	req.URL.Scheme = "http"
	req.URL.Host = parsed
	return t.base.RoundTrip(req)
}

// newTestClient creates a VoyageClient that directs all requests to the given
// httptest server.
func newTestClient(t *testing.T, handler http.Handler) *VoyageClient {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	client := NewVoyageClient("test-api-key", "voyage-4-lite", slog.Default())
	client.httpClient = &http.Client{
		Transport: &redirectTransport{
			targetURL: ts.URL,
			base:      http.DefaultTransport,
		},
	}
	return client
}

func TestEmbed_Success(t *testing.T) {
	var capturedAuth string
	var capturedBody voyageRequest

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture authorization header.
		capturedAuth = r.Header.Get("Authorization")

		// Capture and decode request body.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(body, &capturedBody); err != nil {
			t.Fatalf("unmarshalling request body: %v", err)
		}

		// Verify Content-Type.
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want %q", ct, "application/json")
		}

		// Return valid response with 2 embeddings.
		resp := voyageResponse{
			Data: []voyageEmbedding{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				{Embedding: []float32{0.4, 0.5, 0.6}, Index: 1},
			},
			Model: "voyage-4-lite",
		}
		resp.Usage.TotalTokens = 10

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encoding response: %v", err)
		}
	})

	client := newTestClient(t, handler)

	embeddings, err := client.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed returned unexpected error: %v", err)
	}

	// Verify auth header.
	if capturedAuth != "Bearer test-api-key" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer test-api-key")
	}

	// Verify request body.
	if capturedBody.Model != "voyage-4-lite" {
		t.Errorf("request model = %q, want %q", capturedBody.Model, "voyage-4-lite")
	}
	if capturedBody.InputType != "query" {
		t.Errorf("request input_type = %q, want %q", capturedBody.InputType, "query")
	}
	if len(capturedBody.Input) != 2 {
		t.Fatalf("request input length = %d, want 2", len(capturedBody.Input))
	}
	if capturedBody.Input[0] != "hello" || capturedBody.Input[1] != "world" {
		t.Errorf("request input = %v, want [hello world]", capturedBody.Input)
	}

	// Verify response parsing.
	if len(embeddings) != 2 {
		t.Fatalf("embeddings length = %d, want 2", len(embeddings))
	}

	wantFirst := []float32{0.1, 0.2, 0.3}
	wantSecond := []float32{0.4, 0.5, 0.6}

	for i, want := range wantFirst {
		if embeddings[0][i] != want {
			t.Errorf("embeddings[0][%d] = %f, want %f", i, embeddings[0][i], want)
		}
	}
	for i, want := range wantSecond {
		if embeddings[1][i] != want {
			t.Errorf("embeddings[1][%d] = %f, want %f", i, embeddings[1][i], want)
		}
	}
}

func TestEmbed_ErrorStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
	})

	client := newTestClient(t, handler)

	embeddings, err := client.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("Embed should have returned an error for 401 status")
	}
	if embeddings != nil {
		t.Errorf("embeddings should be nil on error, got %v", embeddings)
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Errorf("error should mention status 401, got: %v", err)
	}
}

func TestEmbed_InvalidJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	})

	client := newTestClient(t, handler)

	embeddings, err := client.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("Embed should have returned an error for invalid JSON")
	}
	if embeddings != nil {
		t.Errorf("embeddings should be nil on error, got %v", embeddings)
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("error should mention unmarshal, got: %v", err)
	}
}

func TestEmbed_EmptyInput(t *testing.T) {
	httpCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		w.WriteHeader(http.StatusOK)
	})

	client := newTestClient(t, handler)

	embeddings, err := client.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed returned unexpected error: %v", err)
	}
	if embeddings != nil {
		t.Errorf("embeddings should be nil for empty input, got %v", embeddings)
	}
	if httpCalled {
		t.Error("HTTP server should not have been called for empty input")
	}
}
