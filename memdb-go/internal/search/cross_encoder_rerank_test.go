package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCrossEncoderRerank_ReordersByScore verifies that the reranker
// reorders items according to the scores returned by the server.
func TestCrossEncoderRerank_ReordersByScore(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Decode request to ensure it's well-formed
		var req crossEncoderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Query != "what is a cat" {
			t.Errorf("bad query: %q", req.Query)
		}
		if len(req.Documents) != 3 {
			t.Errorf("expected 3 docs, got %d", len(req.Documents))
		}
		// Return deliberate out-of-order scores: index 2 is best, index 0 is worst.
		resp := crossEncoderResponse{
			Model: "bge-reranker-v2-m3",
			Results: []crossEncoderResult{
				{Index: 2, RelevanceScore: 5.77},
				{Index: 1, RelevanceScore: -5.48},
				{Index: 0, RelevanceScore: -11.00},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := CrossEncoderConfig{
		URL:     ts.URL,
		Model:   "bge-reranker-v2-m3",
		Timeout: 2 * time.Second,
	}

	items := []map[string]any{
		{"id": "A", "memory": "pasta comes in many shapes", "metadata": map[string]any{"relativity": 0.7}},
		{"id": "B", "memory": "cats purr when content", "metadata": map[string]any{"relativity": 0.6}},
		{"id": "C", "memory": "a cat is a small domestic feline mammal", "metadata": map[string]any{"relativity": 0.5}},
	}

	res := CrossEncoderRerank(context.Background(), "what is a cat", items, "memory", cfg)
	if len(res) != 3 {
		t.Fatalf("expected 3 items, got %d", len(res))
	}
	if res[0]["id"] != "C" {
		t.Errorf("expected C first (best CE score), got %v", res[0]["id"])
	}
	if res[1]["id"] != "B" {
		t.Errorf("expected B second, got %v", res[1]["id"])
	}
	if res[2]["id"] != "A" {
		t.Errorf("expected A third (worst), got %v", res[2]["id"])
	}

	// metadata.relativity should be overwritten with CE score
	meta, _ := res[0]["metadata"].(map[string]any)
	if score, _ := meta["relativity"].(float64); score != 5.77 {
		t.Errorf("expected relativity=5.77 for C, got %v", meta["relativity"])
	}
	if _, ok := meta["cross_encoder_reranked"].(bool); !ok {
		t.Errorf("expected cross_encoder_reranked flag in metadata")
	}
}

// TestCrossEncoderRerank_EmptyInput verifies empty input is returned unchanged.
func TestCrossEncoderRerank_EmptyInput(t *testing.T) {
	cfg := CrossEncoderConfig{URL: "http://unused", Timeout: time.Second}
	res := CrossEncoderRerank(context.Background(), "q", nil, "memory", cfg)
	if res != nil && len(res) != 0 {
		t.Errorf("expected nil/empty, got %v", res)
	}

	items := []map[string]any{}
	res = CrossEncoderRerank(context.Background(), "q", items, "memory", cfg)
	if len(res) != 0 {
		t.Errorf("expected empty, got %d items", len(res))
	}
}

// TestCrossEncoderRerank_NoURL verifies that an unset URL returns input unchanged
// without making any network call.
func TestCrossEncoderRerank_NoURL(t *testing.T) {
	cfg := CrossEncoderConfig{URL: "", Model: "x", Timeout: time.Second}
	items := []map[string]any{
		{"id": "1", "memory": "first", "metadata": map[string]any{"relativity": 0.9}},
		{"id": "2", "memory": "second", "metadata": map[string]any{"relativity": 0.8}},
	}
	res := CrossEncoderRerank(context.Background(), "q", items, "memory", cfg)
	if len(res) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res))
	}
	if res[0]["id"] != "1" || res[1]["id"] != "2" {
		t.Errorf("expected order preserved, got %v, %v", res[0]["id"], res[1]["id"])
	}
}

// TestCrossEncoderRerank_HTTPError verifies fallback on 5xx.
func TestCrossEncoderRerank_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := CrossEncoderConfig{URL: ts.URL, Model: "x", Timeout: time.Second}
	items := []map[string]any{
		{"id": "1", "memory": "first", "metadata": map[string]any{"relativity": 0.9}},
		{"id": "2", "memory": "second", "metadata": map[string]any{"relativity": 0.8}},
	}
	res := CrossEncoderRerank(context.Background(), "q", items, "memory", cfg)
	if len(res) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res))
	}
	if res[0]["id"] != "1" {
		t.Errorf("expected fallback order preserved, got %v first", res[0]["id"])
	}
}

// TestCrossEncoderRerank_Timeout verifies fallback when server hangs beyond timeout.
func TestCrossEncoderRerank_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := CrossEncoderConfig{URL: ts.URL, Model: "x", Timeout: 20 * time.Millisecond}
	items := []map[string]any{
		{"id": "1", "memory": "first", "metadata": map[string]any{"relativity": 0.9}},
		{"id": "2", "memory": "second", "metadata": map[string]any{"relativity": 0.8}},
	}
	// Must not panic and must return items unchanged.
	res := CrossEncoderRerank(context.Background(), "q", items, "memory", cfg)
	if len(res) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res))
	}
	if res[0]["id"] != "1" {
		t.Errorf("expected fallback order preserved, got %v first", res[0]["id"])
	}
}

// TestCrossEncoderRerank_MaxDocsCapsPayload verifies MaxDocs limits the number
// of documents sent over the wire.
func TestCrossEncoderRerank_MaxDocsCapsPayload(t *testing.T) {
	observedDocs := -1
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req crossEncoderRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode: %v", err)
		}
		observedDocs = len(req.Documents)
		// Echo back identity scores so top-N items keep their input order.
		results := make([]crossEncoderResult, 0, observedDocs)
		for i := 0; i < observedDocs; i++ {
			results = append(results, crossEncoderResult{
				Index:          i,
				RelevanceScore: float64(observedDocs - i),
			})
		}
		_ = json.NewEncoder(w).Encode(crossEncoderResponse{Results: results})
	}))
	defer ts.Close()

	cfg := CrossEncoderConfig{URL: ts.URL, Model: "x", Timeout: time.Second, MaxDocs: 10}
	items := make([]map[string]any, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]any{
			"id":       "item-" + string(rune('a'+i%26)),
			"memory":   "doc",
			"metadata": map[string]any{"relativity": 0.5},
		}
	}
	res := CrossEncoderRerank(context.Background(), "q", items, "memory", cfg)
	if observedDocs != 10 {
		t.Errorf("expected 10 docs sent (MaxDocs=10), got %d", observedDocs)
	}
	if len(res) != 100 {
		t.Errorf("expected 100 items total returned (rerank-only-top-N), got %d", len(res))
	}
}

// TestCrossEncoderRerank_ItemsWithoutTextKey verifies items missing the text
// key are handled gracefully.
func TestCrossEncoderRerank_ItemsWithoutTextKey(t *testing.T) {
	observed := -1
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req crossEncoderRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		observed = len(req.Documents)
		// Return identity order (highest score for first).
		results := make([]crossEncoderResult, 0, observed)
		for i := 0; i < observed; i++ {
			results = append(results, crossEncoderResult{Index: i, RelevanceScore: float64(observed - i)})
		}
		_ = json.NewEncoder(w).Encode(crossEncoderResponse{Results: results})
	}))
	defer ts.Close()

	cfg := CrossEncoderConfig{URL: ts.URL, Model: "x", Timeout: time.Second}
	items := []map[string]any{
		{"id": "A", "memory": "has text", "metadata": map[string]any{}},
		{"id": "B", "metadata": map[string]any{}}, // missing "memory"
		{"id": "C", "memory": "also has text", "metadata": map[string]any{}},
	}
	res := CrossEncoderRerank(context.Background(), "q", items, "memory", cfg)
	if observed != 2 {
		t.Errorf("expected 2 docs sent (skipping the one without text), got %d", observed)
	}
	// All 3 items returned — ones without text key keep original position relative to each other.
	if len(res) != 3 {
		t.Errorf("expected 3 items returned, got %d", len(res))
	}
}
