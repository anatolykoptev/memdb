package rerank

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLLMJudgeCacheKey_DiffersByModel verifies that the LLM judge cache
// distinguishes calls by Config.Model. Two judges with identical query
// and candidates but different models must NOT share cached scores.
//
// We exercise the full Rerank entrypoint through an httptest server
// returning a no-op score map, then confirm that flipping Config.Model
// triggers a second LLM hit (cache miss).
func TestLLMJudgeCacheKey_DiffersByModel(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Minimal OpenAI-shape chat-completion response carrying our score JSON.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[{\"id\":\"a\",\"score\":0.9},{\"id\":\"b\",\"score\":0.1}]"}}]}`))
	}))
	defer srv.Close()

	items := []Item{
		&stubItem{id: "a", score: 0.5, text: "alpha", meta: map[string]any{}},
		&stubItem{id: "b", score: 0.5, text: "beta", meta: map[string]any{}},
	}

	jA := LLMJudge{Config: LLMConfig{APIURL: srv.URL, APIKey: "k", Model: "model-a"}}
	if _, err := jA.Rerank(context.Background(), "q", items); err != nil {
		t.Fatalf("model-a rerank: %v", err)
	}
	if _, err := jA.Rerank(context.Background(), "q", items); err != nil {
		t.Fatalf("model-a rerank #2: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 LLM call after warm cache, got %d", calls)
	}

	jB := LLMJudge{Config: LLMConfig{APIURL: srv.URL, APIKey: "k", Model: "model-b"}}
	if _, err := jB.Rerank(context.Background(), "q", items); err != nil {
		t.Fatalf("model-b rerank: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected cache miss on model swap (2 calls total), got %d", calls)
	}
}

// TestLLMJudgeCacheKey_DiffersByEmbeddingText confirms that mutating a
// candidate's EmbeddingText (e.g. a re-summarised memory under the same
// ID) busts the cache. Without folding sha256(text) into the key, the
// LLM's earlier judgement of stale text would silently apply to the
// new content.
func TestLLMJudgeCacheKey_DiffersByEmbeddingText(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[{\"id\":\"a\",\"score\":0.9},{\"id\":\"b\",\"score\":0.1}]"}}]}`))
	}))
	defer srv.Close()

	mkItems := func(textA string) []Item {
		return []Item{
			&stubItem{id: "a", score: 0.5, text: textA, meta: map[string]any{}},
			&stubItem{id: "b", score: 0.5, text: "beta", meta: map[string]any{}},
		}
	}
	j := LLMJudge{Config: LLMConfig{APIURL: srv.URL, APIKey: "k", Model: "m"}}

	if _, err := j.Rerank(context.Background(), "q", mkItems("alpha")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := j.Rerank(context.Background(), "q", mkItems("alpha-rewritten")); err != nil {
		t.Fatalf("second: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected cache miss on text mutation (2 calls), got %d", calls)
	}
}
