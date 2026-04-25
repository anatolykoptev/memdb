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

// newCountingRerankServer returns a rerank-shaped HTTP server that
// increments a counter on every request and writes back synthetic
// scores. Caller owns Close.
func newCountingRerankServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
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
// single missing pair triggers the full live CE path.
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
// that an anchor without ce_score_topk also falls back to live CE.
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
// MEMDB_CE_PRECOMPUTE=false escape hatch.
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
// representations of the cached array.
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

// TestRerankMemoryItemsPrecomputed_TooFewItemsDefersToLive ensures the
// lookup short-circuit is bypassed for a single-item batch (lookup needs
// at least one anchor + one neighbour). The live path still runs because
// upstream postProcessResults guards on len(text) > 1, but we confirm
// here that the precompute wrapper itself does not attempt the lookup
// reorder when there's nothing to reorder.
func TestRerankMemoryItemsPrecomputed_TooFewItemsDefersToLive(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	items := []map[string]any{
		{
			"id":     "anchor",
			"memory": "a",
			"metadata": map[string]any{
				"relativity": 0.9,
				// Cache populated, but irrelevant — wrapper must not
				// attempt to use it for a 1-item batch.
				"ce_score_topk": []any{
					map[string]any{"neighbor_id": "ghost", "score": float64(0.99)},
				},
			},
		},
	}
	out := rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)
	// The 1-item batch defers to live rerank, which calls the HTTP
	// server once. The key invariant is that the wrapper does NOT mark
	// the item as cross_encoder_reranked via the lookup branch (which
	// would have been a logic bug).
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 live CE call (deferred), got %d", got)
	}
	if len(out) != 1 || out[0]["id"] != "anchor" {
		t.Fatalf("expected single-item passthrough, got %v", out)
	}
}

// TestRerankMemoryItemsPrecomputed_StaleNeighbour asserts that a cached
// ce_score_topk containing malformed entries (empty neighbor_id — proxy for
// an expired/soft-deleted neighbour) emits the "stale" metric outcome and
// falls back to live CE rather than serving a corrupted ranking.
//
// Note: stale detection is a partial-parse heuristic: we flag entries whose
// neighbor_id is the empty string or whose score is not a numeric type as
// stale. A full per-search DB lookup to confirm deletion would be too costly;
// this approach catches the most common production case (BGE rewrite replaced
// the stored IDs) without adding a read per search call.
func TestRerankMemoryItemsPrecomputed_StaleNeighbour(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	// Anchor whose ce_score_topk has one valid entry and one with an empty
	// neighbor_id (simulating a neighbour that was soft-deleted and whose
	// stored ID was subsequently cleared or never set).
	items := []map[string]any{
		{
			"id":     "anchor",
			"memory": "anchor text",
			"metadata": map[string]any{
				"relativity": 0.9,
				"ce_score_topk": []any{
					map[string]any{"neighbor_id": "valid-neighbour", "score": float64(0.85)},
					map[string]any{"neighbor_id": "", "score": float64(0.70)}, // stale / expired
				},
			},
		},
		{"id": "valid-neighbour", "memory": "valid", "metadata": map[string]any{"relativity": 0.7}},
	}

	_ = rerankMemoryItemsPrecomputed(context.Background(), client, "q", items)

	// Must fall back to live CE exactly once (stale path, not clean miss).
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 live CE call (stale path), got %d", got)
	}
	// Verify extractCEScoreTopKWithStale directly: partial parse returns scores
	// for the valid entry but sets hasStale=true.
	scores, hasStale := extractCEScoreTopKWithStale(items[0])
	if !hasStale {
		t.Error("expected hasStale=true for cache with empty neighbor_id entry")
	}
	if scores == nil {
		t.Error("expected non-nil scores map (valid entry should parse)")
	}
	if _, ok := scores["valid-neighbour"]; !ok {
		t.Error("expected valid-neighbour in scores map")
	}
}

// TestExtractCEScoreTopKWithStale_AllMalformed checks that a cache where
// every entry has an empty neighbor_id returns (nil, true).
func TestExtractCEScoreTopKWithStale_AllMalformed(t *testing.T) {
	item := map[string]any{
		"id": "anchor",
		"metadata": map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "", "score": float64(0.9)},
				map[string]any{"neighbor_id": "", "score": float64(0.8)},
			},
		},
	}
	scores, hasStale := extractCEScoreTopKWithStale(item)
	if scores != nil {
		t.Errorf("expected nil scores for all-malformed cache, got %v", scores)
	}
	if !hasStale {
		t.Error("expected hasStale=true for all-malformed cache")
	}
}

// BenchmarkRerankMemoryItemsPrecomputed_Hit measures the lookup path
// cost. Lets reviewers spot a regression in the anchor-cache walk
// (target: << 1ms p95 for a 10-item batch).
func BenchmarkRerankMemoryItemsPrecomputed_Hit(b *testing.B) {
	_ = os.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	defer os.Unsetenv("MEMDB_CE_PRECOMPUTE")

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
