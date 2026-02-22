package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProcessLLM_CanAnswer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "{\"can_answer\": true, \"retrieval_phrases\": []}",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := IterativeConfig{APIURL: ts.URL, Model: "test"}
	decision, err := callExpansionLLM(context.Background(), "Who is Bob?", "1. Bob is nice", 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.CanAnswer {
		t.Error("expected canAns to be true")
	}
	if len(decision.RetrievalPhrases) != 0 {
		t.Errorf("expected 0 phrases, got %v", decision.RetrievalPhrases)
	}
}

func TestProcessLLM_NeedMore(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "{\"can_answer\": false, \"retrieval_phrases\": [\"Bob job\", \"Bob age\"]}",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	cfg := IterativeConfig{APIURL: ts.URL, Model: "test"}
	decision, err := callExpansionLLM(context.Background(), "Who is Bob?", "1. Bob is nice", 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if decision.CanAnswer {
		t.Error("expected canAns to be false")
	}
	if len(decision.RetrievalPhrases) != 2 || decision.RetrievalPhrases[0] != "Bob job" {
		t.Errorf("expected phrases ['Bob job', 'Bob age'], got %v", decision.RetrievalPhrases)
	}
}

func TestProcessLLM_Fallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return bad json to force fallback
		_, _ = w.Write([]byte(`{ bad }`))
	}))
	defer ts.Close()

	cfg := IterativeConfig{APIURL: ts.URL, Model: "test"}
	_, err := callExpansionLLM(context.Background(), "error test", "", 0, cfg)
	if err == nil {
		t.Error("expected error from bad json")
	}
}
