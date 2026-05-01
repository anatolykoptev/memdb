package sparse

import (
	"context"
	"time"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState uint8

const (
	// CircuitClosed is the normal operating state — calls pass through.
	CircuitClosed CircuitState = iota
	// CircuitOpen means the breaker has tripped — calls are short-circuited.
	CircuitOpen
	// CircuitHalfOpen means the breaker is probing for recovery.
	CircuitHalfOpen
)

// String returns the human-readable label for the circuit state.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Observer receives lifecycle callbacks from the sparse client. All methods
// must be non-blocking. Panics are recovered by safeCall. Implement only
// the callbacks you care about; embed noopObserver for the rest.
type Observer interface {
	// OnBeforeEmbed fires before the backend call is made.
	// n is the number of texts being embedded.
	OnBeforeEmbed(ctx context.Context, model string, n int)
	// OnAfterEmbed fires after the backend call completes (success or
	// error). n is the number of texts in the result.
	OnAfterEmbed(ctx context.Context, status Status, dur time.Duration, n int)
	// OnRetry fires each time a request is retried.
	OnRetry(ctx context.Context, attempt int, err error)
	// OnCircuitTransition fires when the circuit breaker changes state.
	OnCircuitTransition(ctx context.Context, from, to CircuitState)
	// OnCacheHit fires when a cache hit short-circuits a backend call.
	// n is the number of texts whose vectors were served from cache.
	OnCacheHit(ctx context.Context, n int)
}

// noopObserver is the default Observer — all callbacks are no-ops.
type noopObserver struct{}

func (noopObserver) OnBeforeEmbed(_ context.Context, _ string, _ int)                 {}
func (noopObserver) OnAfterEmbed(_ context.Context, _ Status, _ time.Duration, _ int) {}
func (noopObserver) OnRetry(_ context.Context, _ int, _ error)                        {}
func (noopObserver) OnCircuitTransition(_ context.Context, _, _ CircuitState)         {}
func (noopObserver) OnCacheHit(_ context.Context, _ int)                              {}

// safeCall invokes fn and recovers any panic it raises. A panic in user
// observer code MUST NOT kill an in-flight sparse-embed request.
func safeCall(fn func()) {
	defer func() { _ = recover() }()
	fn()
}
