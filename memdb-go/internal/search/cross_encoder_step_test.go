package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
)

// ceTestRequest mirrors the Cohere /v1/rerank wire request body — declared
// locally because the former internal `crossEncoderRequest` type lived in
// cross_encoder_rerank.go (deleted in the migration to go-kit/rerank).
type ceTestRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type ceTestResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type ceTestResponse struct {
	Results []ceTestResult `json:"results"`
}

// TestPostProcessResults_CrossEncoderCalledWhenURLSet verifies that step 6.05
// invokes embed-server /v1/rerank when the rerank client is configured, and
// that the returned order matches the CE scores.
func TestPostProcessResults_CrossEncoderCalledWhenURLSet(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Decode to count documents
		var req ceTestRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Flip order: assign highest score to the LAST doc so after reorder
		// it sits at the head.
		results := make([]ceTestResult, 0, len(req.Documents))
		for i := 0; i < len(req.Documents); i++ {
			results = append(results, ceTestResult{
				Index:          i,
				RelevanceScore: float64(i + 1), // doc 0 → 1, doc 1 → 2, doc 2 → 3
			})
		}
		_ = json.NewEncoder(w).Encode(ceTestResponse{Results: results})
	}))
	defer ts.Close()

	svc := &SearchService{
		postgres: &mockPostgres{}, embedder: &mockEmbedder{}, logger: discardLogger(),
		RerankClient: rerank.New(rerank.Config{
			URL:     ts.URL,
			Model:   "test-ce",
			Timeout: 2 * time.Second,
		}, nil),
	}

	queryVec := []float32{0.1, 0.2, 0.3}
	text := []map[string]any{
		{"id": "A", "memory": "first doc about cats", "metadata": map[string]any{"relativity": 0.9}},
		{"id": "B", "memory": "second doc about dogs", "metadata": map[string]any{"relativity": 0.8}},
		{"id": "C", "memory": "third doc about pasta", "metadata": map[string]any{"relativity": 0.7}},
	}
	embByID := map[string][]float32{
		"A": {0.1, 0.2, 0.3},
		"B": {0.1, 0.2, 0.3},
		"C": {0.1, 0.2, 0.3},
	}

	retText, _, _, _, _, _, _ := svc.postProcessResults(
		context.Background(), queryVec,
		embByID, nil, nil,
		text, nil, nil, nil,
		SearchParams{Query: "q", TopK: 10},
	)

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected 1 CE call, got %d", n)
	}
	if len(retText) != 3 {
		t.Fatalf("expected 3 text items, got %d", len(retText))
	}
	// Reversed by the mock, so C (was last) should be first.
	if retText[0]["id"] != "C" {
		t.Errorf("expected C first (CE-reordered), got %v", retText[0]["id"])
	}
	if retText[2]["id"] != "A" {
		t.Errorf("expected A last (CE-reordered), got %v", retText[2]["id"])
	}
}

// TestPostProcessResults_CrossEncoderSkippedWhenURLEmpty verifies that the
// default (no-CE) configuration skips the step completely and returns cosine
// ordering.
func TestPostProcessResults_CrossEncoderSkippedWhenURLEmpty(t *testing.T) {
	svc := &SearchService{
		postgres: &mockPostgres{}, embedder: &mockEmbedder{}, logger: discardLogger(),
		// RerankClient nil → disabled via Available()
	}

	queryVec := []float32{1, 0, 0}
	// Items with embeddings differing on first dim — cosine should keep their
	// current ordering (the first item has the most "1, 0, 0" alignment).
	text := []map[string]any{
		{"id": "A", "memory": "first", "metadata": map[string]any{"relativity": 0.0}},
		{"id": "B", "memory": "second", "metadata": map[string]any{"relativity": 0.0}},
	}
	embByID := map[string][]float32{
		"A": {1, 0, 0},
		"B": {0, 1, 0},
	}

	retText, _, _, _, _, _, _ := svc.postProcessResults(
		context.Background(), queryVec,
		embByID, nil, nil,
		text, nil, nil, nil,
		SearchParams{Query: "q", TopK: 10},
	)

	if len(retText) != 2 {
		t.Fatalf("expected 2 text items, got %d", len(retText))
	}
	// A should still be first (higher cosine, no CE reorder).
	if retText[0]["id"] != "A" {
		t.Errorf("expected A first without CE, got %v", retText[0]["id"])
	}
	// No CE flag should be set.
	if meta, ok := retText[0]["metadata"].(map[string]any); ok {
		if _, set := meta["cross_encoder_reranked"]; set {
			t.Errorf("cross_encoder_reranked should NOT be set when URL empty")
		}
	}
}

// TestPostProcessResults_ReturnsCrossEncoderDuration verifies the new return
// value (ceRerankDur) is populated when CE runs.
func TestPostProcessResults_ReturnsCrossEncoderDuration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Small synthetic delay so dur > 0.
		time.Sleep(5 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(ceTestResponse{Results: []ceTestResult{
			{Index: 0, RelevanceScore: 1.0},
			{Index: 1, RelevanceScore: 0.5},
		}})
	}))
	defer ts.Close()

	svc := &SearchService{
		postgres: &mockPostgres{}, embedder: &mockEmbedder{}, logger: discardLogger(),
		RerankClient: rerank.New(rerank.Config{
			URL:     ts.URL,
			Model:   "x",
			Timeout: 2 * time.Second,
		}, nil),
	}

	queryVec := []float32{0.1, 0.2, 0.3}
	text := []map[string]any{
		{"id": "A", "memory": "a", "metadata": map[string]any{"relativity": 0.5}},
		{"id": "B", "memory": "b", "metadata": map[string]any{"relativity": 0.5}},
	}
	embByID := map[string][]float32{"A": {0.1, 0.2, 0.3}, "B": {0.1, 0.2, 0.3}}

	_, _, _, _, _, _, ceDur := svc.postProcessResults(
		context.Background(), queryVec,
		embByID, nil, nil,
		text, nil, nil, nil,
		SearchParams{Query: "q", TopK: 10},
	)

	if ceDur <= 0 {
		t.Errorf("expected ceDur > 0 when CE runs, got %v", ceDur)
	}
}
