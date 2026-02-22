package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMRerank_Basic(t *testing.T) {
	// Setup mock LLM server
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Decode request
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)

		// Create mock response
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "[{\"id\": \"item2\", \"score\": 0.9}, {\"id\": \"item1\", \"score\": 0.1}]",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := LLMRerankConfig{
		APIURL: ts.URL,
		APIKey: "test-key",
		Model:  "test-model",
	}

	items := []map[string]any{
		{"id": "item1", "memory": "fact A", "metadata": map[string]any{}},
		{"id": "item2", "memory": "fact B", "metadata": map[string]any{}},
	}

	ctx := context.Background()

	// Call 1
	res := LLMRerank(ctx, "find B", items, cfg)

	if len(res) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res))
	}
	// Highest score should be first (item2)
	if res[0]["id"] != "item2" {
		t.Errorf("expected item2 to be first, got %v", res[0]["id"])
	}
	if calls != 1 {
		t.Errorf("expected 1 API call, got %d", calls)
	}

	// Call 2 (identical query + items) -> should use in-memory cache
	res2 := LLMRerank(ctx, "find B", items, cfg)
	if len(res2) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res2))
	}
	if res2[0]["id"] != "item2" {
		t.Errorf("expected item2 to be first, got %v", res2[0]["id"])
	}
	if calls != 1 {
		t.Errorf("expected still 1 API call due to cache, got %d", calls)
	}
}

func TestLLMRerank_ErrorFallback(t *testing.T) {
	// Error server (returns 500)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := LLMRerankConfig{APIURL: ts.URL, Model: "test"}
	items := []map[string]any{
		{"id": "item1", "memory": "fact A", "metadata": map[string]any{}},
		{"id": "item2", "memory": "fact B", "metadata": map[string]any{}},
	}
	ctx := context.Background()

	// Should fallback cleanly and return original items order
	res := LLMRerank(ctx, "fallback test", items, cfg)
	if len(res) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res))
	}
	if res[0]["id"] != "item1" {
		t.Errorf("expected fallback to preserve order (item1 first), got %v", res[0]["id"])
	}
}
