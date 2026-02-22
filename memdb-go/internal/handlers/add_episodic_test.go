package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallEpisodicSummarizer_Success(t *testing.T) {
	// Mock LLM server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "This is a mock summary. It is 3-5 sentences long. Bob wants coffee.",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// Temporarily override globals targeting the mock server
	origURL := llmProxyURL
	origModel := llmDefaultModel
	llmProxyURL = ts.URL
	llmDefaultModel = "test-model"
	defer func() {
		llmProxyURL = origURL
		llmDefaultModel = origModel
	}()

	summary, err := callEpisodicSummarizer(context.Background(), "user: I like coffee\nassistant: got it", "general")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "This is a mock summary. It is 3-5 sentences long. Bob wants coffee." {
		t.Errorf("expected mock summary, got %q", summary)
	}
}

func TestCallEpisodicSummarizer_ErrorFallback(t *testing.T) {
	// Error server (returns 500)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	origURL := llmProxyURL
	llmProxyURL = ts.URL
	defer func() { llmProxyURL = origURL }()

	summary, err := callEpisodicSummarizer(context.Background(), "user: I like coffee", "general")
	if err == nil {
		t.Errorf("expected error from 500 response, got none")
	}
	if summary != "" {
		t.Errorf("expected empty string on error, got %q", summary)
	}
}
