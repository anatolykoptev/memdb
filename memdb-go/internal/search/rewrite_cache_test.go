package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetGlobalRewriteCache resets the process-global rewriteCache singleton so
// each test starts from a clean state. Registers a cleanup to restore it.
func resetGlobalRewriteCache(t *testing.T) {
	t.Helper()
	oldCache := globalRewriteCache
	oldOnce := globalRewriteCacheOnce
	t.Cleanup(func() {
		globalRewriteCache = oldCache
		globalRewriteCacheOnce = oldOnce
	})
	globalRewriteCache = nil
	globalRewriteCacheOnce = sync.Once{}
}

// writeChatCompletionResp writes a minimal OpenAI-compatible chat completion
// JSON response with the given content string.
func writeChatCompletionResp(w http.ResponseWriter, content string) {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// TestRewriteCache_HitReturnsCached verifies that a second Get on a key that
// was previously Set returns the exact same value without any transformation.
func TestRewriteCache_HitReturnsCached(t *testing.T) {
	c := &rewriteCache{
		cap:       100,
		entries:   make(map[string]*rewriteCacheEntry, 100),
		evictRing: make([]string, 100),
	}

	key := rewriteCacheKey("what did Alice say", "model-x")
	const want = "User's statement from Alice about the project"

	c.Set(key, want)
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if got != want {
		t.Fatalf("cache value mismatch: want %q, got %q", want, got)
	}
}

// TestRewriteCache_MissCallsLLM verifies that the first call to
// RewriteQueryForRetrieval hits the LLM and the second call for the same
// query+model is served from cache (LLM must NOT be called a second time).
func TestRewriteCache_MissCallsLLM(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	resetGlobalRewriteCache(t)

	var callCount atomic.Int32
	const rewritten = "User statement about Alice and the project last Monday"
	content := `{"rewritten": "` + rewritten + `", "confidence": 0.9}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		writeChatCompletionResp(w, content)
	}))
	defer srv.Close()

	cfg := QueryRewriteConfig{APIURL: srv.URL, Model: "m"}
	nowISO := time.Now().UTC().Format(time.RFC3339)
	query := "what did I tell Alice about the project last week"

	// First call — must reach the LLM.
	res1 := RewriteQueryForRetrieval(context.Background(), query, nowISO, cfg)
	if !res1.Used {
		t.Fatalf("first call: expected Used=true, got false; err=%v", res1.Err)
	}
	if callCount.Load() != 1 {
		t.Fatalf("first call: expected 1 LLM call, got %d", callCount.Load())
	}

	// Second call — same query+model → must be served from cache.
	res2 := RewriteQueryForRetrieval(context.Background(), query, nowISO, cfg)
	if !res2.Used {
		t.Fatalf("second call: expected Used=true (cache hit), got false; err=%v", res2.Err)
	}
	if callCount.Load() != 1 {
		t.Fatalf("second call: LLM was called again (%d total calls), expected cache hit", callCount.Load())
	}
	if res2.Rewritten != rewritten {
		t.Fatalf("second call: rewritten mismatch: want %q, got %q", rewritten, res2.Rewritten)
	}
}

// TestRewriteCache_DisabledByEnv verifies that MEMDB_REWRITE_CACHE_SIZE=0
// disables the cache — every call reaches the LLM, but D4 still functions
// normally (no silent bypass of query rewrite logic).
func TestRewriteCache_DisabledByEnv(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	t.Setenv("MEMDB_REWRITE_CACHE_SIZE", "0")
	resetGlobalRewriteCache(t)

	var callCount atomic.Int32
	const rewritten = "User's message to Alice about project during prior week"
	content := `{"rewritten": "` + rewritten + `", "confidence": 0.85}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		writeChatCompletionResp(w, content)
	}))
	defer srv.Close()

	cfg := QueryRewriteConfig{APIURL: srv.URL, Model: "m"}
	nowISO := time.Now().UTC().Format(time.RFC3339)
	query := "what did I tell Alice about the project last week"

	res1 := RewriteQueryForRetrieval(context.Background(), query, nowISO, cfg)
	if !res1.Used {
		t.Fatalf("call 1: D4 should still work when cache disabled; err=%v", res1.Err)
	}
	res2 := RewriteQueryForRetrieval(context.Background(), query, nowISO, cfg)
	if !res2.Used {
		t.Fatalf("call 2: D4 should still work when cache disabled; err=%v", res2.Err)
	}

	if callCount.Load() != 2 {
		t.Fatalf("expected 2 LLM calls (cache disabled), got %d", callCount.Load())
	}
}

// TestRewriteCache_DifferentModelsDifferentKeys verifies that the same query
// with two different model names produces independent cache entries so there
// is no cross-model key collision.
func TestRewriteCache_DifferentModelsDifferentKeys(t *testing.T) {
	t.Setenv("MEMDB_QUERY_REWRITE", "true")
	resetGlobalRewriteCache(t)

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		writeChatCompletionResp(w, `{"rewritten": "rewrite", "confidence": 0.9}`)
	}))
	defer srv.Close()

	query := "what did I tell Alice about the project last week"
	nowISO := time.Now().UTC().Format(time.RFC3339)

	// Call with model-A.
	cfgA := QueryRewriteConfig{APIURL: srv.URL, Model: "model-A"}
	RewriteQueryForRetrieval(context.Background(), query, nowISO, cfgA)

	// Call with model-B — different model, same query → must hit LLM again.
	cfgB := QueryRewriteConfig{APIURL: srv.URL, Model: "model-B"}
	RewriteQueryForRetrieval(context.Background(), query, nowISO, cfgB)

	if callCount.Load() != 2 {
		t.Fatalf("expected 2 LLM calls (different models = different cache keys), got %d — possible key collision", callCount.Load())
	}
}
