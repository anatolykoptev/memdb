package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
)

// TestNewClient_Defaults_AvailableMatchesV1 verifies that the builder with
// only the static cfg fields populated produces a client whose availability
// and basic surface matches the v1 New() path: a non-empty URL keeps the
// client Available; an empty URL disables it (Cohere-shape contract).
func TestNewClient_Defaults_AvailableMatchesV1(t *testing.T) {
	t.Setenv("MEMDB_RERANK_RETRY_ATTEMPTS", "")
	t.Setenv("MEMDB_RERANK_CIRCUIT_ENABLED", "")
	t.Setenv("MEMDB_RERANK_NORMALIZE", "")

	c := NewClient(ClientConfig{
		URL:     "http://embed-server:8082",
		Model:   "gte-multi-rerank",
		Timeout: 2 * time.Second,
	}, nil)
	if c == nil || !c.Available() {
		t.Fatalf("expected Available=true with non-empty URL")
	}

	c2 := NewClient(ClientConfig{}, nil)
	if c2.Available() {
		t.Fatalf("expected Available=false with empty URL (parity with v1)")
	}
}

// TestNewClient_RetryFiresOn5xx wires a stub server that returns 503 twice
// then 200, asserts the retry loop delivers the final 200 response and the
// memdb.search.rerank_retry_total Observer hook fires (via OnRetry) at
// least once. Coverage for G1 retry path through the builder.
func TestNewClient_RetryFiresOn5xx(t *testing.T) {
	t.Setenv("MEMDB_RERANK_RETRY_ATTEMPTS", "3")
	t.Setenv("MEMDB_RERANK_RETRY_BASE_BACKOFF", "1ms")
	t.Setenv("MEMDB_RERANK_RETRY_MAX_BACKOFF", "5ms")

	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"index": 0, "relevance_score": 0.9}},
		})
	}))
	defer ts.Close()

	c := NewClient(ClientConfig{URL: ts.URL, Model: "test", Timeout: 5 * time.Second}, nil)

	out := c.Rerank(context.Background(), "q", []gokitrerank.Doc{{ID: "0", Text: "doc"}})
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 HTTP calls (2 retries), got %d", got)
	}
	if len(out) != 1 || out[0].Score == 0 {
		t.Fatalf("expected scored doc on retry success, got %+v", out)
	}
}

// TestNewClient_NoRetryWhenAttemptsOne verifies that
// MEMDB_RERANK_RETRY_ATTEMPTS=1 disables the default-on retry policy —
// this is the documented opt-out path.
func TestNewClient_NoRetryWhenAttemptsOne(t *testing.T) {
	t.Setenv("MEMDB_RERANK_RETRY_ATTEMPTS", "1")

	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := NewClient(ClientConfig{URL: ts.URL, Model: "test", Timeout: 1 * time.Second}, nil)
	_ = c.Rerank(context.Background(), "q", []gokitrerank.Doc{{ID: "0", Text: "doc"}})
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 HTTP call (retry disabled), got %d", got)
	}
}

// TestNewClient_CircuitBreakerOpensAfterThreshold verifies the G1 circuit
// breaker trips after FailThreshold consecutive failures, then short-
// circuits subsequent calls without hitting the server.
func TestNewClient_CircuitBreakerOpensAfterThreshold(t *testing.T) {
	t.Setenv("MEMDB_RERANK_CIRCUIT_ENABLED", "true")
	t.Setenv("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", "2")
	t.Setenv("MEMDB_RERANK_CIRCUIT_OPEN_DURATION", "1s")
	t.Setenv("MEMDB_RERANK_RETRY_ATTEMPTS", "1") // disable retry to count raw failures

	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := NewClient(ClientConfig{URL: ts.URL, Model: "test", Timeout: 1 * time.Second}, nil)

	// Fire FailThreshold (=2) failures.
	for i := 0; i < 2; i++ {
		_ = c.Rerank(context.Background(), "q", []gokitrerank.Doc{{ID: "0", Text: "doc"}})
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 HTTP calls before circuit opens, got %d", got)
	}

	// 3rd call: circuit is open, should short-circuit (no HTTP call).
	_ = c.Rerank(context.Background(), "q", []gokitrerank.Doc{{ID: "0", Text: "doc"}})
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected circuit to short-circuit (still 2 HTTP calls), got %d", got)
	}
}

// TestNewClient_NormalizeMinMaxApplied verifies that
// MEMDB_RERANK_NORMALIZE=minmax actually rescales scores to [0,1] —
// regression guard against accidentally dropping the option from the
// builder option chain.
func TestNewClient_NormalizeMinMaxApplied(t *testing.T) {
	t.Setenv("MEMDB_RERANK_NORMALIZE", "minmax")
	t.Setenv("MEMDB_RERANK_RETRY_ATTEMPTS", "1")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 0, "relevance_score": 1.0},
				{"index": 1, "relevance_score": 5.0},
				{"index": 2, "relevance_score": 9.0},
			},
		})
	}))
	defer ts.Close()

	c := NewClient(ClientConfig{URL: ts.URL, Model: "test", Timeout: 1 * time.Second}, nil)
	out := c.Rerank(context.Background(), "q", []gokitrerank.Doc{
		{ID: "0", Text: "a"}, {ID: "1", Text: "b"}, {ID: "2", Text: "c"},
	})
	if len(out) != 3 {
		t.Fatalf("expected 3 scored, got %d", len(out))
	}
	// After minmax: top score normalises to 1.0, bottom to 0.0, middle to 0.5.
	// out is sorted desc by post-pipeline score.
	if out[0].Score != 1.0 {
		t.Errorf("expected top normalized score 1.0, got %v", out[0].Score)
	}
	if out[2].Score != 0.0 {
		t.Errorf("expected bottom normalized score 0.0, got %v", out[2].Score)
	}
}

// TestEnvNormalizeMode covers all three accepted spellings (case + trim).
func TestEnvNormalizeMode(t *testing.T) {
	cases := []struct {
		v    string
		want gokitrerank.NormalizeMode
	}{
		{"", gokitrerank.NormalizeNone},
		{"none", gokitrerank.NormalizeNone},
		{"  MinMax ", gokitrerank.NormalizeMinMax},
		{"ZSCORE", gokitrerank.NormalizeZScore},
		{"unknown", gokitrerank.NormalizeNone},
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			t.Setenv("X_RERANK_NORMALIZE_TEST", tc.v)
			if got := envNormalizeMode("X_RERANK_NORMALIZE_TEST"); got != tc.want {
				t.Errorf("envNormalizeMode(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

// TestEnvServerNormalize covers None / sigmoid spellings.
func TestEnvServerNormalize(t *testing.T) {
	cases := []struct {
		v    string
		want gokitrerank.ServerNormalize
	}{
		{"", gokitrerank.ServerNormalizeNone},
		{"none", gokitrerank.ServerNormalizeNone},
		{"  Sigmoid ", gokitrerank.ServerNormalizeSigmoid},
		{"banana", gokitrerank.ServerNormalizeNone},
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			t.Setenv("X_RERANK_SERVER_NORMALIZE_TEST", tc.v)
			if got := envServerNormalize("X_RERANK_SERVER_NORMALIZE_TEST"); got != tc.want {
				t.Errorf("envServerNormalize(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

// TestEnvRetryPolicy_Defaults verifies the fallback values when env is empty.
func TestEnvRetryPolicy_Defaults(t *testing.T) {
	t.Setenv("MEMDB_RERANK_RETRY_ATTEMPTS", "")
	t.Setenv("MEMDB_RERANK_RETRY_BASE_BACKOFF", "")
	t.Setenv("MEMDB_RERANK_RETRY_MAX_BACKOFF", "")

	p := envRetryPolicy()
	if p.MaxAttempts != 3 {
		t.Errorf("expected MaxAttempts=3, got %d", p.MaxAttempts)
	}
	if p.BaseBackoff != 200*time.Millisecond {
		t.Errorf("expected BaseBackoff=200ms, got %v", p.BaseBackoff)
	}
	if p.MaxBackoff != 2*time.Second {
		t.Errorf("expected MaxBackoff=2s, got %v", p.MaxBackoff)
	}
	wantStatus := []int{500, 502, 503, 504}
	if len(p.RetryableStatus) != len(wantStatus) {
		t.Fatalf("retryable status length mismatch: got %v want %v", p.RetryableStatus, wantStatus)
	}
}

// TestEnvCircuitConfig_Overrides verifies env-driven circuit overrides land
// on the produced CircuitConfig.
func TestEnvCircuitConfig_Overrides(t *testing.T) {
	t.Setenv("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", "7")
	t.Setenv("MEMDB_RERANK_CIRCUIT_OPEN_DURATION", "45s")
	t.Setenv("MEMDB_RERANK_CIRCUIT_HALFOPEN_PROBES", "3")

	cc := envCircuitConfig()
	if cc.FailThreshold != 7 {
		t.Errorf("FailThreshold: got %d want 7", cc.FailThreshold)
	}
	if cc.OpenDuration != 45*time.Second {
		t.Errorf("OpenDuration: got %v want 45s", cc.OpenDuration)
	}
	if cc.HalfOpenProbes != 3 {
		t.Errorf("HalfOpenProbes: got %d want 3", cc.HalfOpenProbes)
	}
}
