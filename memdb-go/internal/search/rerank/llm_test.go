package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMJudge_NoURLSkips(t *testing.T) {
	j := LLMJudge{Config: LLMConfig{APIURL: ""}}
	items := []Item{newStub("a", 0.5), newStub("b", 0.6)}
	out, err := j.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected passthrough")
	}
}

func TestLLMJudge_SingleItemSkips(t *testing.T) {
	j := LLMJudge{Config: LLMConfig{APIURL: "http://localhost"}}
	out, err := j.Rerank(context.Background(), "q", []Item{newStub("a", 0.5)})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 item")
	}
}

func TestLLMJudge_RescoresAndCaches(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `[{"id":"item2","score":0.9},{"id":"item1","score":0.1}]`}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	j := LLMJudge{Config: LLMConfig{APIURL: ts.URL, Model: "test-model"}}
	items := []Item{newStub("item1", 0.5), newStub("item2", 0.4)}
	out, err := j.Rerank(context.Background(), "find item2", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].ID() != "item2" {
		t.Errorf("expected item2 first after LLM rerank, got %s", out[0].ID())
	}
	if v, _ := out[0].GetMeta("llm_reranked"); v != true {
		t.Errorf("expected llm_reranked=true on item2")
	}

	// Second call with identical input → cache hit, no extra HTTP calls.
	out2, _ := j.Rerank(context.Background(), "find item2", []Item{newStub("item1", 0.5), newStub("item2", 0.4)})
	if calls != 1 {
		t.Errorf("expected 1 HTTP call due to cache, got %d", calls)
	}
	if out2[0].ID() != "item2" {
		t.Errorf("cache returned wrong order, got %s first", out2[0].ID())
	}
}

func TestLLMJudge_ErrorFallsBack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	j := LLMJudge{Config: LLMConfig{APIURL: ts.URL, Model: "x"}}
	items := []Item{newStub("a", 0.5), newStub("b", 0.4)}
	out, err := j.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected fallback to preserve length")
	}
	if out[0].ID() != "a" {
		t.Errorf("expected fallback to preserve original order, got %s", out[0].ID())
	}
}

func TestLLMJudge_NameStable(t *testing.T) {
	j := LLMJudge{}
	if j.Name() != "llm_judge" {
		t.Errorf("LLMJudge.Name() must be 'llm_judge'")
	}
}
