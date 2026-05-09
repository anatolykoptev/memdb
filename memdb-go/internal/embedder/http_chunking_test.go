package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	otelsdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// makeChunkTestServer builds a fake embed-server that:
//   - counts how many HTTP calls it receives (via *atomic.Int64)
//   - on each call, returns embeddings where vecs[i][0] = globalOffset + i
//     (globalOffset is the index of the first text in the original slice,
//     derived from the "X-Chunk-Offset" header the test sets, or from a
//     counter that we manage separately)
//
// Because the fake server cannot know the global offset without help from the
// test, callers pass a startIndex channel that the server reads from before
// each request.  The channel must be buffered with one value per expected call.
func makeChunkTestServer(t *testing.T, callCount *atomic.Int64, startIndexCh <-chan int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)

		var req testEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		start := 0
		if startIndexCh != nil {
			start = <-startIndexCh
		}

		data := make([]testEmbedData, len(req.Input))
		for i := range req.Input {
			vec := make([]float32, 4)
			vec[0] = float32(start + i)
			data[i] = testEmbedData{Embedding: vec, Index: i}
		}
		resp := testEmbedResponse{Data: data}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv
}

// TestHTTPEmbedder_ChunksLargeInput: 100 texts, chunkSize=32 → 4 HTTP calls (32+32+32+4),
// result length 100.
func TestHTTPEmbedder_ChunksLargeInput(t *testing.T) {
	var callCount atomic.Int64

	// Pre-fill start indices: chunk 0→0, chunk 1→32, chunk 2→64, chunk 3→96
	startIdxCh := make(chan int, 4)
	startIdxCh <- 0
	startIdxCh <- 32
	startIdxCh <- 64
	startIdxCh <- 96

	srv := makeChunkTestServer(t, &callCount, startIdxCh)
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model", 4, testLogger())
	e.chunkSize = 32

	texts := make([]string, 100)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	got, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if got, want := callCount.Load(), int64(4); got != want {
		t.Errorf("HTTP calls: got %d, want %d", got, want)
	}
	if len(got) != 100 {
		t.Errorf("result length: got %d, want 100", len(got))
	}
}

// TestHTTPEmbedder_NoChunkUnderCap: 16 texts, chunkSize=32 → exactly 1 HTTP call.
func TestHTTPEmbedder_NoChunkUnderCap(t *testing.T) {
	var callCount atomic.Int64

	startIdxCh := make(chan int, 1)
	startIdxCh <- 0

	srv := makeChunkTestServer(t, &callCount, startIdxCh)
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model", 4, testLogger())
	e.chunkSize = 32

	texts := make([]string, 16)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	got, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if calls := callCount.Load(); calls != 1 {
		t.Errorf("HTTP calls: got %d, want 1", calls)
	}
	if len(got) != 16 {
		t.Errorf("result length: got %d, want 16", len(got))
	}
}

// TestHTTPEmbedder_PreservesOrder: 50 texts; mock returns vec[0]=global index;
// assert vecs[i][0]==i for all i.
func TestHTTPEmbedder_PreservesOrder(t *testing.T) {
	var callCount atomic.Int64

	// 50 texts, chunkSize=32 → 2 chunks: 0..31, 32..49
	startIdxCh := make(chan int, 2)
	startIdxCh <- 0
	startIdxCh <- 32

	srv := makeChunkTestServer(t, &callCount, startIdxCh)
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "test-model", 4, testLogger())
	e.chunkSize = 32

	texts := make([]string, 50)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	got, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("result length: got %d, want 50", len(got))
	}
	for i, vec := range got {
		if len(vec) == 0 {
			t.Errorf("[%d] empty vector", i)
			continue
		}
		if vec[0] != float32(i) {
			t.Errorf("[%d] vec[0] = %f, want %f", i, vec[0], float32(i))
		}
	}
}

// TestHTTPEmbedder_FailsAtomicallyOnSubBatchError: chunkSize=10, 25 texts,
// mock fails on the 2nd sub-batch; expect error returned, no partial result.
func TestHTTPEmbedder_FailsAtomicallyOnSubBatchError(t *testing.T) {
	var callCount atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 2 {
			// Second chunk: return HTTP 500
			http.Error(w, `{"error":"oom"}`, http.StatusInternalServerError)
			return
		}
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
	e.chunkSize = 10

	texts := make([]string, 25)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	result, err := e.Embed(context.Background(), texts)
	if err == nil {
		t.Fatal("expected error on 2nd sub-batch failure, got nil")
	}
	if result != nil {
		t.Errorf("expected nil partial result on error, got %v", result)
	}
}

// TestHTTPEmbedder_ChunkSizeFromEnv: MEMDB_EMBED_CHUNK_SIZE=8 → chunkSize=8.
func TestHTTPEmbedder_ChunkSizeFromEnv(t *testing.T) {
	t.Setenv("MEMDB_EMBED_CHUNK_SIZE", "8")

	e := NewHTTPEmbedder("http://localhost:9999", "test-model", 4, testLogger())
	if e.chunkSize != 8 {
		t.Errorf("chunkSize: got %d, want 8", e.chunkSize)
	}
}

// TestHTTPEmbedder_ChunksPerCallMetricRecorded: 100 texts at chunkSize=32
// → ChunksPerCall histogram has 1 data point with value 4.
func TestHTTPEmbedder_ChunksPerCallMetricRecorded(t *testing.T) {
	// Set up a fresh OTel SDK provider with a manual reader.
	// This must happen before any embedderChunkMetrics() call for this test.
	reader := otelsdkmetric.NewManualReader()
	provider := otelsdkmetric.NewMeterProvider(otelsdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	defer provider.Shutdown(context.Background()) //nolint:errcheck

	// Reset the chunk metrics singleton so this test gets its own instruments.
	resetChunkMetrics()

	var callCount atomic.Int64
	startIdxCh := make(chan int, 4)
	startIdxCh <- 0
	startIdxCh <- 32
	startIdxCh <- 64
	startIdxCh <- 96

	srv := makeChunkTestServer(t, &callCount, startIdxCh)
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "jina-code-v2", 4, testLogger())
	e.chunkSize = 32

	texts := make([]string, 100)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}
	if _, err := e.Embed(context.Background(), texts); err != nil {
		t.Fatalf("Embed error: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	// Find the ChunksPerCall histogram and verify 1 data point with sum=4.
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "memdb.embedder.chunks_per_call" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[int64])
			if !ok {
				t.Fatalf("chunks_per_call is not Int64Histogram, got %T", m.Data)
			}
			if len(hist.DataPoints) != 1 {
				t.Fatalf("chunks_per_call: expected 1 data point, got %d", len(hist.DataPoints))
			}
			dp := hist.DataPoints[0]
			if dp.Count != 1 {
				t.Errorf("chunks_per_call count: got %d, want 1", dp.Count)
			}
			if dp.Sum != 4 {
				t.Errorf("chunks_per_call sum: got %d, want 4", dp.Sum)
			}
			found = true
		}
	}
	if !found {
		t.Error("metric memdb.embedder.chunks_per_call not found in collected data")
	}
}


