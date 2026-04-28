package rerank

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubItem is a tiny in-memory Item used by chain + strategy tests so we
// don't need the search-package adapter here.
type stubItem struct {
	id    string
	score float64
	text  string
	meta  map[string]any
}

func (s *stubItem) ID() string                  { return s.id }
func (s *stubItem) Score() float64              { return s.score }
func (s *stubItem) SetScore(v float64)          { s.score = v }
func (s *stubItem) SetMeta(key string, v any)   { s.meta[key] = v }
func (s *stubItem) GetMeta(key string) (any, bool) {
	v, ok := s.meta[key]
	return v, ok
}
func (s *stubItem) EmbeddingText() string { return s.text }

func newStub(id string, score float64) *stubItem {
	return &stubItem{id: id, score: score, text: id, meta: map[string]any{}}
}

// stubReranker reverses the slice and stamps a flag — handy to verify
// chain ordering and per-strategy mutation visibility.
type stubReranker struct {
	name    string
	flag    string
	wantErr error
}

func (s stubReranker) Name() string { return s.name }
func (s stubReranker) Rerank(ctx context.Context, _ string, items []Item) ([]Item, error) {
	if s.wantErr != nil {
		return nil, s.wantErr
	}
	out := make([]Item, len(items))
	for i := range items {
		out[i] = items[len(items)-1-i]
		out[i].SetMeta(s.flag, true)
	}
	RecordSuccess(ctx, s.name)
	return out, nil
}

func TestChain_RunsInOrder(t *testing.T) {
	items := []Item{newStub("a", 0.5), newStub("b", 0.6), newStub("c", 0.7)}
	c := Chain{
		stubReranker{name: "first", flag: "first_did_it"},
		stubReranker{name: "second", flag: "second_did_it"},
	}
	out, err := c.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("chain returned error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
	// first reverses (c,b,a), second reverses again (a,b,c).
	if out[0].ID() != "a" || out[1].ID() != "b" || out[2].ID() != "c" {
		t.Errorf("unexpected order: %v %v %v", out[0].ID(), out[1].ID(), out[2].ID())
	}
	// Both flags must be set on every item — visibility across the chain.
	for _, it := range out {
		if v, _ := it.GetMeta("first_did_it"); v != true {
			t.Errorf("item %s missing first_did_it flag", it.ID())
		}
		if v, _ := it.GetMeta("second_did_it"); v != true {
			t.Errorf("item %s missing second_did_it flag", it.ID())
		}
	}
}

func TestChain_ContinuesOnError(t *testing.T) {
	items := []Item{newStub("a", 0.5), newStub("b", 0.6)}
	c := Chain{
		stubReranker{name: "broken", wantErr: errors.New("boom")},
		stubReranker{name: "ok", flag: "ok"},
	}
	out, err := c.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("chain must not propagate strategy errors: got %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items even after error, got %d", len(out))
	}
	// "ok" still ran on the original input — should have reversed (b, a).
	if out[0].ID() != "b" || out[1].ID() != "a" {
		t.Errorf("expected ok to run on original input after broken errored")
	}
}

func TestChain_SkippedNilOutput(t *testing.T) {
	// A reranker that returns nil should be treated as "skipped" — chain
	// continues with the previous slice.
	items := []Item{newStub("a", 0.5), newStub("b", 0.6)}
	skipper := stubSkipper{name: "skipper"}
	c := Chain{skipper, stubReranker{name: "ok", flag: "ok"}}
	out, err := c.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("chain returned error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out))
	}
	// ok reversed → b, a
	if out[0].ID() != "b" {
		t.Errorf("expected ok to run after skipper")
	}
}

type stubSkipper struct{ name string }

func (s stubSkipper) Name() string { return s.name }
func (s stubSkipper) Rerank(_ context.Context, _ string, _ []Item) ([]Item, error) {
	return nil, nil
}

func TestChain_EmptyInput(t *testing.T) {
	c := Chain{stubReranker{name: "x", flag: "x"}}
	out, err := c.Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}

func TestChain_NameIsChain(t *testing.T) {
	c := Chain{}
	if c.Name() != "chain" {
		t.Errorf("Chain.Name() should be 'chain'")
	}
}

// sleepReranker sleeps for d in Rerank — used to verify per-strategy
// timing attribution in RerankWithTimings.
type sleepReranker struct {
	name string
	d    time.Duration
}

func (s sleepReranker) Name() string { return s.name }
func (s sleepReranker) Rerank(ctx context.Context, _ string, items []Item) ([]Item, error) {
	time.Sleep(s.d)
	RecordSuccess(ctx, s.name)
	return items, nil
}

// TestChain_TracksPerStrategyDurations pins the C1 contract: ChainResult
// exposes per-strategy wall-clock timings keyed by Reranker.Name() so
// service_postprocess can attribute ce/llm cost separately instead of
// double-counting the chain total.
func TestChain_TracksPerStrategyDurations(t *testing.T) {
	const slept = 5 * time.Millisecond // generous to avoid flake on slow runners
	c := Chain{
		sleepReranker{name: "alpha", d: slept},
		sleepReranker{name: "beta", d: slept},
		sleepReranker{name: "gamma", d: slept},
	}
	items := []Item{newStub("a", 0.5)}
	res, err := c.RerankWithTimings(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("RerankWithTimings err: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil ChainResult")
	}
	if len(res.Durations) != 3 {
		t.Fatalf("expected 3 duration entries, got %d", len(res.Durations))
	}
	var total time.Duration
	for _, name := range []string{"alpha", "beta", "gamma"} {
		d, ok := res.Durations[name]
		if !ok {
			t.Errorf("missing duration entry for %q", name)
			continue
		}
		if d < slept {
			t.Errorf("strategy %q duration %v < expected sleep %v", name, d, slept)
		}
		total += d
	}
	// Sequential execution: total ≈ 3 * slept (within a generous slack
	// to absorb scheduler jitter).
	if total < 3*slept {
		t.Errorf("total duration %v < 3*slept %v — strategies should be sequential", total, 3*slept)
	}
}

// TestChain_TimingsRecordedOnError verifies a strategy that errors still
// gets a duration entry — operators need timing even for the error path.
func TestChain_TimingsRecordedOnError(t *testing.T) {
	c := Chain{
		stubReranker{name: "broken", wantErr: errors.New("boom")},
		stubReranker{name: "ok", flag: "ok"},
	}
	res, err := c.RerankWithTimings(context.Background(), "q", []Item{newStub("a", 0.5), newStub("b", 0.6)})
	if err != nil {
		t.Fatalf("RerankWithTimings err: %v", err)
	}
	if _, ok := res.Durations["broken"]; !ok {
		t.Error("expected duration entry for errored strategy")
	}
	if _, ok := res.Durations["ok"]; !ok {
		t.Error("expected duration entry for ok strategy")
	}
}

// TestChain_RerankDelegatesToTimings confirms the legacy Chain.Rerank
// API still returns the final items by delegating to RerankWithTimings.
func TestChain_RerankDelegatesToTimings(t *testing.T) {
	items := []Item{newStub("a", 0.5), newStub("b", 0.6)}
	c := Chain{stubReranker{name: "x", flag: "x"}}
	out, err := c.Rerank(context.Background(), "q", items)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out))
	}
	if out[0].ID() != "b" {
		t.Errorf("expected reversed order from stubReranker, got %s first", out[0].ID())
	}
}
