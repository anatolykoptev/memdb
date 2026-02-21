package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallProfileLLM_Success(t *testing.T) {
	// Mock LLM server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "Alice is a software engineer. She likes cats.",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	profiler := &Profiler{
		llmProxyURL: ts.URL,
		llmProxyKey: "test",
		llmModel:    "test-model",
	}

	res, err := profiler.callProfileLLM(context.Background(), "- fact 1\n- fact 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "Alice is a software engineer. She likes cats." {
		t.Errorf("expected mock profile, got %q", res)
	}
}

func TestCallProfileLLM_ErrorFallback(t *testing.T) {
	// Error server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	profiler := &Profiler{
		llmProxyURL: ts.URL,
		llmProxyKey: "test",
		llmModel:    "test-model",
	}

	res, err := profiler.callProfileLLM(context.Background(), "- fact 1")
	if err == nil {
		t.Errorf("expected error from 500 response, got none")
	}
	if res != "" {
		t.Errorf("expected empty string on error, got %q", res)
	}
}
