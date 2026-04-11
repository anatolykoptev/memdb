package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
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

	client := llm.NewClient(ts.URL, "", "test-model", nil, slog.Default())
	summary, err := callEpisodicSummarizer(context.Background(), client, "user: I like coffee\nassistant: got it", "general")
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

	client := llm.NewClient(ts.URL, "", "test-model", nil, slog.Default())
	summary, err := callEpisodicSummarizer(context.Background(), client, "user: I like coffee", "general")
	if err == nil {
		t.Errorf("expected error from 500 response, got none")
	}
	if summary != "" {
		t.Errorf("expected empty string on error, got %q", summary)
	}
}
