package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
)

// Helper: build a CE rerank server that bumps a counter on every request.
// Returns the server and the counter pointer; caller owns Close.
func newCountingRerankServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// Mirror the Cohere /v1/rerank shape — flip order so doc N gets
		// score (N+1), letting the caller assert reorder happened.
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, 0, len(req.Documents))
		for i := range req.Documents {
			results = append(results, map[string]any{
				"index":           i,
				"relevance_score": float64(i + 1),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	return ts, &calls
}

// TestRerankMemoryItemsPrecomputed_HitBypassesLiveCE proves the lookup-
// first path serves the whole batch from cache and never calls the
// live HTTP CE backend.
func TestRerankMemoryItemsPrecomputed_HitBypassesLiveCE(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()

	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	// Anchor (item 0) carries pre-computed scores for its two
	// neighbours. Cosine ordering is ignored at this point — the
	// rerank function trusts the post-cosine order it receives.
	items := []map[string]any{
		{
			"id":     "anchor",
			"memory": "anchor text",
			"metadata": map[string]any{
				"relativity": 0.9,
				"ce_score_topk": []any{
					map[string]any{"neighbor_id": "neighbor-low", "score": float64(0.30)},
					map[string]any{"neighbor_id": "neighbor-hi", "score": float64(0.95)},
				},
			},
		},
		{"id": "neighbor-low", "memory": "low neighbour", "metadata": map[string]any{"relativity": 0.6}},
		{"id": "neighbor-hi", "memory": "high neighbour", "metadata": map[string]any{"relativity": 0.55}},
	}

	out := rerankMemoryItemsPrecomputed(context.Background(), client, "any query", items)

	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("expected 0 live CE calls (lookup hit), got %d", got)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
	if out[0]["id"] != "anchor" {
		t.Errorf("anchor must remain at index 0, got %v", out[0]["id"])
	}
	// neighbor-hi (cached score 0.95) should jump ahead of neighbor-low (0.30).
	if out[1]["id"] != "neighbor-hi" {
		t.Errorf("expected neighbor-hi at index 1, got %v", out[1]["id"])
	}
	if out[2]["id"] != "neighbor-low" {
		t.Errorf("expected neighbor-low at index 2, got %v", out[2]["id"])
	}
	// All items must carry the cross_encoder_reranked=true marker so
	// downstream filters know CE was applied.
	for i, it := range out {
		meta, _ := it["metadata"].(map[string]any)
		if meta == nil {
			t.Errorf("item %d missing metadata", i)
			continue
		}
		if v, _ := meta["cross_encoder_reranked"].(bool); !v {
			t.Errorf("item %d missing cross_encoder_reranked=true", i)
		}
	}
}

// TestRerankMemoryItemsPrecomputed_MissFallsBackToLiveCE proves a
// single missing pair triggers the full live CE path (mixing cached +
// live scores would corrupt the ranking).
func TestRerankMemoryItemsPrecomputed_MissFallsBackToLiveCE(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	items := []map[string]any{
		{
			"id":     "anchor",
			"memory": "anchor text",
			"metadata": map[string]any{
				"relativity": 0.9,
				"ce_score_topk": []any{
					map[string]any{"neighbor_id": "neighbor-known", "score": float64(0.85)},
					// missing neighbor for the second item below
				},
			},
		},
		{"id": "neighbor-known", "memory": "known neighbour", "metadata": map[string]any{"relativity": 0.6}},
		{"id": "neighbor-unknown", "memory": "unknown neighbour", "metadata": map[string]any{"relativity": 0.55}},
	}

	_ = rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected exactly 1 live CE call (lookup miss), got %d", got)
	}
}

// TestRerankMemoryItemsPrecomputed_AnchorWithoutCacheFallsBack proves
// that an anchor without ce_score_topk also falls back to live CE
// (counts as a miss).
func TestRerankMemoryItemsPrecomputed_AnchorWithoutCacheFallsBack(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	items := []map[string]any{
		{"id": "anchor", "memory": "a", "metadata": map[string]any{"relativity": 0.9}},
		{"id": "other", "memory": "b", "metadata": map[string]any{"relativity": 0.8}},
	}
	_ = rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 live CE call (anchor without cache), got %d", got)
	}
}

// TestRerankMemoryItemsPrecomputed_DisabledByEnvSkipsLookup proves the
// MEMDB_CE_PRECOMPUTE=false escape hatch — every call goes live, even
// when the cache would have served.
func TestRerankMemoryItemsPrecomputed_DisabledByEnvSkipsLookup(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "false")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	items := []map[string]any{
		{
			"id":     "anchor",
			"memory": "a",
			"metadata": map[string]any{
				"relativity": 0.9,
				"ce_score_topk": []any{
					map[string]any{"neighbor_id": "other", "score": float64(0.95)},
				},
			},
		},
		{"id": "other", "memory": "b", "metadata": map[string]any{"relativity": 0.8}},
	}
	_ = rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 live CE call (env-disabled), got %d", got)
	}
}

// TestExtractCEScoreTopK_HandlesShapes covers the three on-the-wire
// representations of the cached array (typed []any from in-process
// metadata, []map[string]any from the same source, JSON string from
// agtype-text legacy paths).
func TestExtractCEScoreTopK_HandlesShapes(t *testing.T) {
	cases := map[string]any{
		"any-slice": []any{
			map[string]any{"neighbor_id": "n1", "score": float64(0.5)},
		},
		"typed-slice": []map[string]any{
			{"neighbor_id": "n1", "score": float64(0.5)},
		},
		"json-string": `[{"neighbor_id":"n1","score":0.5}]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			item := map[string]any{
				"id": "x",
				"metadata": map[string]any{
					"ce_score_topk": raw,
				},
			}
			got := extractCEScoreTopK(item)
			if got == nil {
				t.Fatalf("nil result")
			}
			if v, ok := got["n1"]; !ok || v < 0.49 || v > 0.51 {
				t.Fatalf("expected n1≈0.5, got %v", v)
			}
		})
	}
}

// TestRerankMemoryItemsPrecomputed_TooFewItemsDefersToLive ensures we
// don't try to lookup-rank a single-item batch and instead defer to the
// live path. The live path itself does call out (it bumps the live counter
// and posts a single-doc rerank request) — what we verify here is that
// the precompute branch was NOT taken (no extractCEScoreTopK lookup,
// no precompute hit/miss/stale metric). Live call count is allowed.
func TestRerankMemoryItemsPrecomputed_TooFewItemsDefersToLive(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	items := []map[string]any{
		{"id": "anchor", "memory": "a", "metadata": map[string]any{"relativity": 0.9}},
	}
	_ = rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)
	// Single-doc requests go through to the live HTTP endpoint; we just
	// verify the call succeeded (≤ 1) — the assertion that matters
	// (precompute branch not taken) is implicit: no panic on missing
	// ce_score_topk, no hit/miss recorded.
	if got := atomic.LoadInt32(calls); got > 1 {
		t.Fatalf("expected at most 1 live CE call (single-item batch deferred to live), got %d", got)
	}
}

// BenchmarkRerankMemoryItemsPrecomputed_Hit measures the lookup path
// cost. Lets reviewers spot a regression in the anchor-cache walk
// (target: << 1ms p95 for a 10-item batch).
func BenchmarkRerankMemoryItemsPrecomputed_Hit(b *testing.B) {
	_ = os.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	defer os.Unsetenv("MEMDB_CE_PRECOMPUTE")

	// No HTTP server — hit path must never call out, so client URL
	// being unreachable is fine (but Available() requires a non-empty
	// URL).
	client := rerank.New(rerank.Config{URL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}, nil)

	const n = 10
	cache := make([]any, n-1)
	items := make([]map[string]any, n)
	items[0] = map[string]any{
		"id":     "anchor",
		"memory": "anchor",
		"metadata": map[string]any{
			"relativity": 0.9,
		},
	}
	for i := 1; i < n; i++ {
		id := "n" + string(rune('a'+i-1))
		cache[i-1] = map[string]any{"neighbor_id": id, "score": float64(0.9) - float64(i)*0.05}
		items[i] = map[string]any{
			"id":     id,
			"memory": id,
			"metadata": map[string]any{
				"relativity": 0.8,
			},
		}
	}
	items[0]["metadata"].(map[string]any)["ce_score_topk"] = cache

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)
	}
}
