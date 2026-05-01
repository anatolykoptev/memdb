package sparse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCircuitOpen is returned by callBackendResilient when the circuit
// breaker is in the Open state and has blocked the call.
var ErrCircuitOpen = errors.New("sparse: circuit breaker open")

// CircuitConfig configures a CircuitBreaker instance.
type CircuitConfig struct {
	// FailThreshold is the number of consecutive failures that trip the
	// circuit from Closed to Open. Default: 5.
	FailThreshold int
	// OpenDuration is how long the circuit stays Open before transitioning
	// to HalfOpen for probe requests. Default: 30s.
	OpenDuration time.Duration
	// HalfOpenProbes is the number of requests allowed through when in
	// HalfOpen state. Default: 1.
	HalfOpenProbes int
	// FailRateWindow is reserved for future fail-rate counting (currently
	// consecutive-failure counting is used). Default: 10s.
	FailRateWindow time.Duration
}

func defaultCircuitConfig() CircuitConfig {
	return CircuitConfig{
		FailThreshold:  5,
		OpenDuration:   30 * time.Second,
		HalfOpenProbes: 1,
		FailRateWindow: 10 * time.Second,
	}
}

// CircuitBreaker is a thread-safe Closed/Open/HalfOpen state machine.
// Reads use RLock; writes (transitions) use Lock. Mirrors embed.CircuitBreaker
// — kept package-local to avoid cross-package dependency on embed/.
type CircuitBreaker struct {
	cfg          CircuitConfig
	mu           sync.RWMutex
	state        CircuitState
	consecFails  int
	openedAt     time.Time
	halfOpenCnt  int32
	onTransition func(from, to CircuitState)
	model        string
}

// NewCircuitBreaker constructs a CircuitBreaker with the given config and
// an optional transition callback. The callback is invoked (via safeCall)
// on every state change; pass nil to skip.
func NewCircuitBreaker(cfg CircuitConfig, model string, onTransition func(CircuitState, CircuitState)) *CircuitBreaker {
	if cfg.FailThreshold <= 0 {
		cfg.FailThreshold = defaultCircuitConfig().FailThreshold
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = defaultCircuitConfig().OpenDuration
	}
	if cfg.HalfOpenProbes <= 0 {
		cfg.HalfOpenProbes = defaultCircuitConfig().HalfOpenProbes
	}
	if cfg.FailRateWindow <= 0 {
		cfg.FailRateWindow = defaultCircuitConfig().FailRateWindow
	}
	return &CircuitBreaker{
		cfg:          cfg,
		state:        CircuitClosed,
		model:        model,
		onTransition: onTransition,
	}
}

// Allow reports whether the current request may proceed.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.RLock()
	state := cb.state
	openedAt := cb.openedAt
	cb.mu.RUnlock()

	switch state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(openedAt) < cb.cfg.OpenDuration {
			return false
		}
		cb.mu.Lock()
		if cb.state == CircuitOpen && time.Since(cb.openedAt) >= cb.cfg.OpenDuration {
			cb.doTransition(CircuitHalfOpen)
		}
		cb.mu.Unlock()
		return cb.acquireHalfOpenSlot()
	case CircuitHalfOpen:
		return cb.acquireHalfOpenSlot()
	default:
		return true
	}
}

// acquireHalfOpenSlot atomically claims one of the allowed probe slots.
func (cb *CircuitBreaker) acquireHalfOpenSlot() bool {
	cnt := atomic.AddInt32(&cb.halfOpenCnt, 1)
	if int(cnt) <= cb.cfg.HalfOpenProbes {
		return true
	}
	atomic.AddInt32(&cb.halfOpenCnt, -1)
	return false
}

// MarkSuccess notifies the breaker that the call succeeded.
func (cb *CircuitBreaker) MarkSuccess() {
	cb.mu.Lock()
	switch cb.state {
	case CircuitHalfOpen:
		cb.consecFails = 0
		atomic.StoreInt32(&cb.halfOpenCnt, 0)
		cb.doTransition(CircuitClosed)
	case CircuitClosed:
		cb.consecFails = 0
	}
	cb.mu.Unlock()
}

// MarkFailure notifies the breaker that the call failed.
func (cb *CircuitBreaker) MarkFailure() {
	cb.mu.Lock()
	switch cb.state {
	case CircuitClosed:
		cb.consecFails++
		if cb.consecFails >= cb.cfg.FailThreshold {
			cb.openedAt = time.Now()
			atomic.StoreInt32(&cb.halfOpenCnt, 0)
			cb.doTransition(CircuitOpen)
		}
	case CircuitHalfOpen:
		cb.openedAt = time.Now()
		atomic.StoreInt32(&cb.halfOpenCnt, 0)
		cb.doTransition(CircuitOpen)
	}
	cb.mu.Unlock()
}

// State returns the current CircuitState. Safe for concurrent reads.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// doTransition changes cb.state and fires the onTransition callback.
// Caller MUST hold cb.mu write lock.
func (cb *CircuitBreaker) doTransition(to CircuitState) {
	from := cb.state
	cb.state = to
	recordCircuitState(cb.model, to)
	if cb.onTransition != nil && from != to {
		fn := cb.onTransition
		f, t := from, to
		safeCall(func() { fn(f, t) })
	}
}

// makeCircuitHook builds an onTransition callback that records metrics and
// fires obs.OnCircuitTransition.
func makeCircuitHook(model string, obs Observer) func(CircuitState, CircuitState) {
	return func(from, to CircuitState) {
		recordCircuitTransition(model, from, to)
		safeCall(func() { obs.OnCircuitTransition(context.Background(), from, to) })
	}
}
