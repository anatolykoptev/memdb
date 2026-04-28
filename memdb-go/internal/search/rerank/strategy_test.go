package rerank

import (
	"context"
	"errors"
	"testing"
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
