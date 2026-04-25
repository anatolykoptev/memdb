package search

// cot_decomposer_test.go — unit tests for D11 CoTDecomposer.
//
// Coverage:
//   - shouldDecompose heuristic gate (positive + negative cases).
//   - normalizeSubqueries dedupe / trim / cap / atomic detection.
//   - Decompose end-to-end with stub LLM (success, error fallback, parse
//     failure, cache hit).
//   - Constructor clamps MaxSubQueries / Timeout to spec ranges.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShouldDecompose_PositiveCases(t *testing.T) {
	t.Parallel()
	cases := []string{
		"What did Caroline do in Boston after she met Emma at the conference?",
		"When did Alice visit Paris and what did Bob say about her trip?",
		"Did Sarah finish the project before John joined the team last March?",
	}
	for _, q := range cases {
		if !shouldDecompose(q) {
			t.Errorf("expected shouldDecompose(%q)=true", q)
		}
	}
}

func TestShouldDecompose_NegativeCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
	}{
		{"too short", "what did Caroline do?"},
		{"no temporal connector", "tell me about the very long meeting yesterday with Alice"},
		{"single entity", "what did Caroline do in the city after she finished the work today"},
		{"empty", ""},
		{"whitespace only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if shouldDecompose(tc.query) {
				t.Errorf("expected shouldDecompose(%q)=false", tc.query)
			}
		})
	}
}

func TestNormalizeSubqueries(t *testing.T) {
	t.Parallel()
	t.Run("trims and dedupes", func(t *testing.T) {
		got := normalizeSubqueries([]string{"  hello  ", "hello", "Hello", "world"}, "orig", 3)
		if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("drops empties", func(t *testing.T) {
		got := normalizeSubqueries([]string{"", "  ", "x"}, "orig", 3)
		if len(got) != 1 || got[0] != "x" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("caps to maxN", func(t *testing.T) {
		got := normalizeSubqueries([]string{"a", "b", "c", "d", "e"}, "orig", 3)
		if len(got) != 3 {
			t.Errorf("len=%d, want 3", len(got))
		}
	})
	t.Run("atomic single-element", func(t *testing.T) {
		got := normalizeSubqueries([]string{"orig"}, "orig", 3)
		if len(got) != 1 || got[0] != "orig" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("nil on empty input", func(t *testing.T) {
		if got := normalizeSubqueries(nil, "orig", 3); got != nil {
			t.Errorf("got %v", got)
		}
	})
}

func TestNewCoTDecomposer_Clamps(t *testing.T) {
	t.Parallel()
	t.Run("max subqueries below 1 → 3", func(t *testing.T) {
		d := NewCoTDecomposer(CoTDecomposerConfig{MaxSubQueries: 0})
		if d.cfg.MaxSubQueries != 3 {
			t.Errorf("got %d, want 3", d.cfg.MaxSubQueries)
		}
	})
	t.Run("max subqueries above 5 → 5", func(t *testing.T) {
		d := NewCoTDecomposer(CoTDecomposerConfig{MaxSubQueries: 99})
		if d.cfg.MaxSubQueries != 5 {
			t.Errorf("got %d, want 5", d.cfg.MaxSubQueries)
		}
	})
	t.Run("timeout below 500ms → 500ms", func(t *testing.T) {
		d := NewCoTDecomposer(CoTDecomposerConfig{Timeout: 10 * time.Millisecond})
		if d.cfg.Timeout != 500*time.Millisecond {
			t.Errorf("got %v", d.cfg.Timeout)
		}
	})
	t.Run("timeout above 10s → 10s", func(t *testing.T) {
		d := NewCoTDecomposer(CoTDecomposerConfig{Timeout: time.Hour})
		if d.cfg.Timeout != 10*time.Second {
			t.Errorf("got %v", d.cfg.Timeout)
		}
	})
}

func TestDecompose_DisabledReturnsOriginal(t *testing.T) {
	t.Parallel()
	d := NewCoTDecomposer(CoTDecomposerConfig{Enabled: false, APIURL: "http://x"})
	got := d.Decompose(context.Background(), nil,
		"What did Caroline do in Boston after she met Emma at the conference?")
	if len(got) != 1 || got[0] == "" {
		t.Errorf("got %v", got)
	}
}

func TestDecompose_HeuristicSkip(t *testing.T) {
	t.Parallel()
	d := NewCoTDecomposer(CoTDecomposerConfig{Enabled: true, APIURL: "http://x"})
	// Short query — gate skips before any LLM call (URL points nowhere safely).
	got := d.Decompose(context.Background(), nil, "what about lunch?")
	if len(got) != 1 || got[0] != "what about lunch?" {
		t.Errorf("got %v", got)
	}
}

// stubLLMServer returns an httptest.Server that emits a chat completion with
// the given content as choices[0].message.content. Caller closes via t.Cleanup.
func stubLLMServer(t *testing.T, content string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDecompose_SuccessAndCacheHit(t *testing.T) {
	t.Parallel()
	srv := stubLLMServer(t, `["When did Caroline meet Emma?","What did Caroline do in Boston?"]`, http.StatusOK)
	d := NewCoTDecomposer(CoTDecomposerConfig{
		Enabled: true, APIURL: srv.URL, Model: "stub", MaxSubQueries: 3,
	})
	q := "What did Caroline do in Boston after she met Emma at the conference?"
	got := d.Decompose(context.Background(), nil, q)
	if len(got) < 2 {
		t.Fatalf("expected ≥2 sub-queries, got %v", got)
	}
	if got[0] != q {
		t.Errorf("expected original at index 0, got %q", got[0])
	}
	// Second call should hit the cache (same result, no panic, no new HTTP).
	got2 := d.Decompose(context.Background(), nil, q)
	if len(got2) != len(got) {
		t.Errorf("cache mismatch: %v vs %v", got, got2)
	}
}

func TestDecompose_LLMErrorFallback(t *testing.T) {
	t.Parallel()
	srv := stubLLMServer(t, `irrelevant`, http.StatusInternalServerError)
	d := NewCoTDecomposer(CoTDecomposerConfig{
		Enabled: true, APIURL: srv.URL, Model: "stub", MaxSubQueries: 3,
	})
	q := "What did Caroline do in Boston after she met Emma at the conference?"
	got := d.Decompose(context.Background(), nil, q)
	if len(got) != 1 || got[0] != q {
		t.Errorf("expected fallback to original, got %v", got)
	}
}

func TestDecompose_ParseFailureFallback(t *testing.T) {
	t.Parallel()
	srv := stubLLMServer(t, `not a json array`, http.StatusOK)
	d := NewCoTDecomposer(CoTDecomposerConfig{
		Enabled: true, APIURL: srv.URL, Model: "stub", MaxSubQueries: 3,
	})
	q := "What did Caroline do in Boston after she met Emma at the conference?"
	got := d.Decompose(context.Background(), nil, q)
	if len(got) != 1 || got[0] != q {
		t.Errorf("expected fallback to original, got %v", got)
	}
}

func TestDecompose_NilReceiverSafe(t *testing.T) {
	t.Parallel()
	var d *CoTDecomposer
	got := d.Decompose(context.Background(), nil, "any query here at all")
	if len(got) != 1 {
		t.Errorf("nil receiver should return single-element slice, got %v", got)
	}
}

func TestCotCacheKey_Stable(t *testing.T) {
	t.Parallel()
	a := cotCacheKey("Hello World")
	b := cotCacheKey("  hello world  ")
	if a != b {
		t.Errorf("cache key not normalized: %s vs %s", a, b)
	}
}
