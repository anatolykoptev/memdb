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

// TestRerankPrecomputed_PartialHit_LiveOnlyForMissing — M14 Y2 core contract.
// 5 items: anchor + 3 cached + 2 uncached → live CE fires exactly once
// with only the 2 uncached documents. Final order reflects mixed scores.
func TestRerankPrecomputed_PartialHit_LiveOnlyForMissing(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")

	// Live server records the documents it received and returns fixed scores.
	var receivedDocs []string
	var liveCallCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&liveCallCount, 1)
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedDocs = req.Documents
		// Score: doc[0] = 0.70, doc[1] = 0.60
		results := make([]map[string]any, 0, len(req.Documents))
		for i := range req.Documents {
			results = append(results, map[string]any{
				"index":           i,
				"relevance_score": 0.70 - float64(i)*0.10,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	// anchor: cache covers c1, c2, c3 but NOT m1, m2
	anchor := &stubItem{
		id: "anchor", score: 0.9, text: "anchor",
		meta: map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "c1", "score": float64(0.88)},
				map[string]any{"neighbor_id": "c2", "score": float64(0.50)},
				map[string]any{"neighbor_id": "c3", "score": float64(0.20)},
			},
		},
	}
	c1 := newStub("c1", 0.6)
	c2 := newStub("c2", 0.5)
	c3 := newStub("c3", 0.4)
	c3.text = "c3"
	m1 := newStub("m1", 0.55) // uncached — text required for live call
	m2 := newStub("m2", 0.45) // uncached
	m1.text = "m1"
	m2.text = "m2"

	ce := CrossEncoder{Client: client}
	out, err := ce.Rerank(context.Background(), "q", []Item{anchor, c1, c2, c3, m1, m2})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Live call fires exactly ONCE (partial hit, not full miss).
	if n := atomic.LoadInt32(&liveCallCount); n != 1 {
		t.Errorf("expected 1 live call, got %d", n)
	}
	// Live call received only the 2 uncached documents.
	if len(receivedDocs) != 2 {
		t.Errorf("expected 2 docs sent to live CE, got %d: %v", len(receivedDocs), receivedDocs)
	}
	// Anchor stays at index 0.
	if out[0].ID() != "anchor" {
		t.Errorf("anchor must be at index 0, got %s", out[0].ID())
	}
	// c1 (cached 0.88) must be ahead of m1 (live ~0.70).
	pos := make(map[string]int, len(out))
	for i, it := range out {
		pos[it.ID()] = i
	}
	if pos["c1"] >= pos["m1"] {
		t.Errorf("c1 (cached 0.88) should rank above m1 (live 0.70): c1=%d m1=%d", pos["c1"], pos["m1"])
	}
	if pos["c3"] <= pos["m2"] {
		t.Errorf("c3 (cached 0.20) should rank below m2 (live 0.60): c3=%d m2=%d", pos["c3"], pos["m2"])
	}
	// All 6 items returned.
	if len(out) != 6 {
		t.Errorf("expected 6 items, got %d", len(out))
	}
	// cross_encoder_reranked flag on all.
	for _, it := range out {
		if v, _ := it.GetMeta("cross_encoder_reranked"); v != true {
			t.Errorf("item %s missing cross_encoder_reranked flag", it.ID())
		}
	}
}

// TestRerankPrecomputed_PartialHit_PartialMetric — outcome="partial" fires
// when some items hit cache and others require a live CE call.
func TestRerankPrecomputed_PartialHit_PartialMetric(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, _ := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	var outcomesSeen []string
	ce := CrossEncoder{
		Client: client,
		OnPrecompute: func(_ context.Context, oc string) {
			outcomesSeen = append(outcomesSeen, oc)
		},
	}

	// anchor cache covers only "cached" — "uncached" must go live.
	anchor := &stubItem{
		id: "anchor", score: 0.9, text: "anchor",
		meta: map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "cached", "score": float64(0.80)},
			},
		},
	}
	_, err := ce.Rerank(context.Background(), "q",
		[]Item{anchor, newStub("cached", 0.7), newStub("uncached", 0.6)})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(outcomesSeen) != 1 || outcomesSeen[0] != "partial" {
		t.Errorf("expected outcome=[partial], got %v", outcomesSeen)
	}
}

// TestRerankPrecomputed_FullHit_Parity — when ALL items hit cache the
// behaviour is byte-for-byte identical to the pre-M14 full-hit path:
// zero live calls, anchor at 0, items sorted by cached score DESC.
func TestRerankPrecomputed_FullHit_Parity(t *testing.T) {
	t.Setenv("MEMDB_CE_PRECOMPUTE", "true")
	ts, calls := newCountingRerankServer(t)
	defer ts.Close()
	client := rerank.New(rerank.Config{URL: ts.URL, Model: "test", Timeout: 2 * time.Second}, nil)

	var outcomes []string
	ce := CrossEncoder{
		Client: client,
		OnPrecompute: func(_ context.Context, oc string) { outcomes = append(outcomes, oc) },
	}

	anchor := &stubItem{
		id: "anchor", score: 0.9, text: "anchor",
		meta: map[string]any{
			"ce_score_topk": []any{
				map[string]any{"neighbor_id": "lo", "score": float64(0.20)},
				map[string]any{"neighbor_id": "hi", "score": float64(0.90)},
				map[string]any{"neighbor_id": "mid", "score": float64(0.55)},
			},
		},
	}
	out, err := ce.Rerank(context.Background(), "q",
		[]Item{anchor, newStub("lo", 0.4), newStub("hi", 0.5), newStub("mid", 0.45)})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("expected 0 live calls on full hit, got %d", n)
	}
	if len(outcomes) != 1 || outcomes[0] != "hit" {
		t.Errorf("expected outcome=[hit], got %v", outcomes)
	}
	if out[0].ID() != "anchor" {
		t.Errorf("anchor must stay at index 0, got %s", out[0].ID())
	}
	order := []string{out[1].ID(), out[2].ID(), out[3].ID()}
	expected := []string{"hi", "mid", "lo"}
	for i, want := range expected {
		if order[i] != want {
			t.Errorf("position %d: want %s got %s", i+1, want, order[i])
		}
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
