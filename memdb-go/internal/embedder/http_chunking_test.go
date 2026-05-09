package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestHTTPEmbedder_ChunkSizeEnvPropagatesToGoKit verifies that
// MEMDB_EMBED_CHUNK_SIZE is forwarded to the underlying go-kit Client as
// WithChunkSize so operator deployments keep working without env rename.
//
// Behavioral assertion: set env=8, send 16 texts → go-kit chunks into 2
// calls of 8, confirming chunkSize=8 reached the shared Client.
func TestHTTPEmbedder_ChunkSizeEnvPropagatesToGoKit(t *testing.T) {
	t.Setenv("MEMDB_EMBED_CHUNK_SIZE", "8")

	var callCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		var req testEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data := make([]testEmbedData, len(req.Input))
		for i := range req.Input {
			data[i] = testEmbedData{Embedding: []float32{float32(i)}, Index: i}
		}
		resp := testEmbedResponse{Data: data}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model", 1, testLogger())

	texts := make([]string, 16)
	for i := range texts {
		texts[i] = "text"
	}
	if _, err := e.Embed(context.Background(), texts); err != nil {
		t.Fatalf("Embed error: %v", err)
	}

	// go-kit's E5 chunking fires when len(texts) > chunkSize.
	// With chunkSize=8, 16 texts → 2 HTTP calls of 8.
	if got := callCount.Load(); got != 2 {
		t.Errorf("HTTP calls: got %d, want 2 (chunkSize=8 not propagated to go-kit)", got)
	}
}
