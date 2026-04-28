package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
)

// newCountingRerankServer mirrors the test helper from the parent search
// package so the strategy can be exercised end-to-end without the
// adapter.
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

func TestCrossEncoder_NilClientSkips(t *testing.T) {
	ce := CrossEncoder{Client: nil}
	items := []Item{newStub("a", 0.5), newStub("b", 0.4)}
	out, err := ce.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected passthrough")
	}
}

func TestCrossEncoder_LookupHitBypassesLive(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	anchor := &stubItem{
		id: "anchor", score: 0.9, text: "anchor",
		meta: map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "low", "score": float64(0.30)},
				map[string]any{"neighbor_id": "hi", "score": float64(0.95)},
			},
		},
	}
	low := newStub("low", 0.6)
	hi := newStub("hi", 0.55)

	ce := CrossEncoder{Client: client}
	out, err := ce.Rerank(context.Background(), "q", []Item{anchor, low, hi})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("expected 0 live calls (lookup hit), got %d", got)
	}
	if out[0].ID() != "anchor" {
		t.Errorf("anchor must stay at index 0, got %s", out[0].ID())
	}
	// hi (cached 0.95) ahead of low (0.30).
	if out[1].ID() != "hi" {
		t.Errorf("expected hi at index 1, got %s", out[1].ID())
	}
	if out[2].ID() != "low" {
		t.Errorf("expected low at index 2, got %s", out[2].ID())
	}
	for _, it := range out {
		if v, _ := it.GetMeta("cross_encoder_reranked"); v != true {
			t.Errorf("item %s missing cross_encoder_reranked=true", it.ID())
		}
	}
}

func TestCrossEncoder_MissFallsBackToLive(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	anchor := &stubItem{
		id: "anchor", score: 0.9, text: "anchor",
		meta: map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "known", "score": float64(0.85)},
			},
		},
	}
	ce := CrossEncoder{Client: client}
	_, err := ce.Rerank(context.Background(), "q",
		[]Item{anchor, newStub("known", 0.6), newStub("unknown", 0.55)})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 live call (lookup miss), got %d", got)
	}
}

func TestCrossEncoder_DisabledByEnv(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "false")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	anchor := &stubItem{
		id: "anchor", score: 0.9, text: "anchor",
		meta: map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "other", "score": float64(0.95)},
			},
		},
	}
	ce := CrossEncoder{Client: client}
	_, _ = ce.Rerank(context.Background(), "q", []Item{anchor, newStub("other", 0.8)})
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 live call when env-disabled, got %d", got)
	}
}

func TestCrossEncoder_HookFires(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, _ := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	var liveCalls, precomputeCalls int32
	var lastOutcome string
	ce := CrossEncoder{
		Client:       client,
		OnLiveCall:   func(_ context.Context) { atomic.AddInt32(&liveCalls, 1) },
		OnPrecompute: func(_ context.Context, oc string) { atomic.AddInt32(&precomputeCalls, 1); lastOutcome = oc },
	}
	// Anchor without ce_score_topk → miss path → live call + precompute miss.
	_, _ = ce.Rerank(context.Background(), "q",
		[]Item{newStub("anchor", 0.9), newStub("other", 0.8)})

	if atomic.LoadInt32(&liveCalls) != 1 {
		t.Errorf("expected 1 live hook firing, got %d", atomic.LoadInt32(&liveCalls))
	}
	if atomic.LoadInt32(&precomputeCalls) != 1 {
		t.Errorf("expected 1 precompute hook firing, got %d", atomic.LoadInt32(&precomputeCalls))
	}
	if lastOutcome != "miss" {
		t.Errorf("expected outcome 'miss', got %q", lastOutcome)
	}
}
