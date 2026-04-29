package rerank

import (
	"context"
	"errors"
	"testing"
	"time"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
)

// TestOTelObserver_HooksDoNotPanic exercises every Observer method with the
// memdb-go OTel meter wired underneath. The intent is regression coverage:
// a future change that drops one of the OTel instruments must not cause a
// nil-pointer panic in the rerank hot path.
//
// We do not assert metric values here — go-kit/otel-prometheus is the
// runtime collector, and unit-test fakes for it would dwarf this file.
// The smoke check (Step 5) verifies counters are visible on /metrics.
func TestOTelObserver_HooksDoNotPanic(t *testing.T) {
	obs := newOTelObserver("test-model")
	ctx := context.Background()

	obs.OnBeforeCall(ctx, "q", 10)
	obs.OnAfterCall(ctx, gokitrerank.StatusOk, 50*time.Millisecond, 10)
	obs.OnAfterCall(ctx, gokitrerank.StatusDegraded, 100*time.Millisecond, 0)
	obs.OnAfterCall(ctx, gokitrerank.StatusFallback, 200*time.Millisecond, 5)
	obs.OnAfterCall(ctx, gokitrerank.StatusSkipped, 0, 0)
	obs.OnRetry(ctx, 1, errors.New("boom"))
	obs.OnCircuitTransition(ctx, gokitrerank.CircuitClosed, gokitrerank.CircuitOpen)
	obs.OnCircuitTransition(ctx, gokitrerank.CircuitOpen, gokitrerank.CircuitHalfOpen)
	obs.OnCircuitTransition(ctx, gokitrerank.CircuitHalfOpen, gokitrerank.CircuitClosed)
	obs.OnCacheHit(ctx, 7)
	obs.OnTruncate(ctx, "doc-42", 800, 512)
}

// TestOTelObserver_Singleton verifies the metrics struct is initialised
// exactly once across multiple observer instances. The sync.Once guard is
// load-bearing — without it, OTel emits "duplicate instrument" errors at
// scrape time when both server_init paths construct a Client.
func TestOTelObserver_Singleton(t *testing.T) {
	a := rerankObsMx()
	b := rerankObsMx()
	if a != b {
		t.Fatalf("expected the same metrics struct across calls (sync.Once), got distinct pointers")
	}
}
