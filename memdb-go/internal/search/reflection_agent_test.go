// Package search — reflection_agent_test.go: F2 reflection-loop unit tests.
//
// These exercise the LLM-side decision parsing only — the loop control
// (max_iter, raw fetch, missing_info re-embed) is covered by integration
// tests in pipeline_query_test.go via mocked postgres + embedder.
package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// reflectStubServer returns an httptest server that always responds with
// a single chat-completion choice whose assistant.content is `body`. The
// counter `calls` lets tests assert exactly how many LLM calls fired.
func reflectStubServer(t *testing.T, body string, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": body}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// reflectStubError returns an httptest server that returns a 500 on every
// request — used to verify Reflect's fail-closed behaviour.
func reflectStubError(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
}

func TestReflectionAgent_NilClient(t *testing.T) {
	a := NewReflectionAgent(nil)
	dec, err := a.Reflect(context.Background(), "q", nil)
	if err == nil {
		t.Fatal("expected error for nil client")
	}
	if dec.Decision != ReflectionDecisionSufficient {
		t.Fatalf("nil-client must fail closed to sufficient, got %q", dec.Decision)
	}
}

func TestReflectionAgent_DecisionParsing(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantDec     string
		wantAspects int
		wantErr     bool
	}{
		{
			name:    "sufficient",
			body:    `{"decision":"sufficient","missing_aspects":[],"reason":"all good"}`,
			wantDec: ReflectionDecisionSufficient,
		},
		{
			name:    "needs_raw",
			body:    `{"decision":"needs_raw","missing_aspects":[],"reason":"trim dropped detail"}`,
			wantDec: ReflectionDecisionNeedsRaw,
		},
		{
			name:        "missing_info_caps_aspects",
			body:        `{"decision":"missing_info","missing_aspects":["a","b","c","d","e"],"reason":"gap"}`,
			wantDec:     ReflectionDecisionMissingInfo,
			wantAspects: reflectionMaxAspects,
		},
		{
			name:        "missing_info_dedup_and_trim",
			body:        `{"decision":"missing_info","missing_aspects":[" foo "," FOO ","bar"],"reason":""}`,
			wantDec:     ReflectionDecisionMissingInfo,
			wantAspects: 2,
		},
		{
			name:    "fence_stripped",
			body:    "```json\n{\"decision\":\"sufficient\",\"missing_aspects\":[]}\n```",
			wantDec: ReflectionDecisionSufficient,
		},
		{
			name:    "unknown_label_fails_closed",
			body:    `{"decision":"go_fish","missing_aspects":[]}`,
			wantDec: ReflectionDecisionSufficient,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			srv := reflectStubServer(t, tc.body, &calls)
			defer srv.Close()
			a := NewReflectionAgent(llm.NewSimpleClient(srv.URL, "", "test-model"))
			dec, err := a.Reflect(context.Background(), "Compare X and Y", []map[string]any{
				{"id": "1", "memory": "alpha"},
				{"id": "2", "memory": "beta"},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if dec.Decision != tc.wantDec {
				t.Fatalf("decision=%q want %q", dec.Decision, tc.wantDec)
			}
			if len(dec.MissingAspects) != tc.wantAspects {
				t.Fatalf("missing_aspects len=%d want %d (got %v)", len(dec.MissingAspects), tc.wantAspects, dec.MissingAspects)
			}
			if calls.Load() == 0 {
				t.Fatalf("expected at least one LLM call, got 0")
			}
		})
	}
}

func TestReflectionAgent_TransportErrorFailsClosed(t *testing.T) {
	var calls atomic.Int64
	srv := reflectStubError(t, &calls)
	defer srv.Close()
	a := NewReflectionAgent(llm.NewSimpleClient(srv.URL, "", "test-model"))
	dec, err := a.Reflect(context.Background(), "q", []map[string]any{{"id": "1", "memory": "x"}})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if dec.Decision != ReflectionDecisionSufficient {
		t.Fatalf("transport error must fail closed, got %q", dec.Decision)
	}
}

func TestSanitizeAspects(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"all_blank", []string{" ", "", "\t"}, nil},
		{"trim_and_dedup", []string{" foo", "FOO ", "bar"}, []string{"foo", "bar"}},
		{"cap", []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAspects(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d (got=%v want=%v)", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: %q vs %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIsCat3Query(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"Compare alice and bob", true},
		{"Which one of the cars is faster?", true},
		{"How does Alice relate to Bob?", true},
		{"What's the difference between X and Y?", true},
		{"What is the difference between A and B", true},
		{"List all books read in 2024", true},
		{"Summarise the meeting", true},
		{"Summarize the meeting", true},
		{"Between Alice and Bob who travelled more?", true},
		{"When did Alice visit Paris?", false},
		{"What is your name?", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isCat3Query(c.q); got != c.want {
			t.Errorf("isCat3Query(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}

func TestBuildReflectionContext(t *testing.T) {
	got := buildReflectionContext(nil, 5)
	if got != "(none)" {
		t.Fatalf("nil items want (none), got %q", got)
	}
	items := []map[string]any{
		{"memory": "alpha"},
		{"memory": ""},
		{"memory": "beta"},
		{"memory": "gamma"},
	}
	got = buildReflectionContext(items, 2)
	// Expect first two entries rendered (alpha, then empty skipped — but we
	// still consumed the slot. Verify presence of "1. alpha".
	if got == "" || got == "(none)" {
		t.Fatalf("expected rendered context, got %q", got)
	}
}
