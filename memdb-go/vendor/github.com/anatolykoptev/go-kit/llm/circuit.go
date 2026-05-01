// Package llm — circuit.go: CircuitBreaker for LLM provider calls.
//
// Mirrors the embed/circuit.go pattern (FailThreshold consecutive failures →
// Open; OpenDuration cooldown → HalfOpen probe; first probe success → Closed,
// failure → reopens). Trimmed of the per-backend metrics gauge that lives in
// embed; LLM providers are typically single-endpoint per Client so the model
// label is sufficient.
//
// Why: under burst load (parallel ingest + parallel query workers) the free-
// tier Gemini Flash proxy throttles individual keys, surfacing as 429 / 5xx /
// timeouts. Without a breaker, every caller pays the per-call timeout (15-30s)
// and pile up retries against a degraded backend. With a breaker, after N
// consecutive failures we fail-fast for OpenDuration, giving the provider
// time to recover and shedding queue pressure on our side.
//
// 2026-05-01 telemetry that motivated this port: 80 d4_rewrite + 67
// d10_enhance + 45 d7_decompose timeouts in a single chat-50 LoCoMo run on
// memdb-go. Each timeout = a degraded retrieval = a wrong answer downstream.
package llm

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCircuitOpen is returned by callers that integrate the circuit guard when
// the breaker is in the Open state and short-circuits the call.
var ErrCircuitOpen = errors.New("llm: circuit breaker open")

// CircuitState — three-state Closed/Open/HalfOpen machine.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // healthy; calls pass through
	CircuitOpen                         // tripped; calls fail-fast for OpenDuration
	CircuitHalfOpen                     // probing; HalfOpenProbes calls allowed
)

// String for log/diagnostic use; not parsed.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// CircuitConfig — tunable thresholds. Zero values fill from defaults.
type CircuitConfig struct {
	// FailThreshold — consecutive failures before tripping Open. Default 5.
	FailThreshold int
	// OpenDuration — cooldown before transitioning Open → HalfOpen. Default 30s.
	OpenDuration time.Duration
	// HalfOpenProbes — concurrent probe slots in HalfOpen. Default 1.
	HalfOpenProbes int
}

func defaultCircuitConfig() CircuitConfig {
	return CircuitConfig{
		FailThreshold:  5,
		OpenDuration:   30 * time.Second,
		HalfOpenProbes: 1,
	}
}

// CircuitBreaker — thread-safe state machine. Reads use RLock; transitions
// hold Lock. The probe slot counter is atomic so HalfOpen contention does
// not require the write lock.
type CircuitBreaker struct {
	cfg          CircuitConfig
	mu           sync.RWMutex
	state        CircuitState
	consecFails  int
	openedAt     time.Time
	halfOpenCnt  int32 // atomic; current concurrent HalfOpen probes
	model        string
	onTransition func(from, to CircuitState)
}

// NewCircuitBreaker constructs the breaker. model is a label used by the
// optional callback; pass "" if no model is associated.
func NewCircuitBreaker(cfg CircuitConfig, model string, onTransition func(CircuitState, CircuitState)) *CircuitBreaker {
	def := defaultCircuitConfig()
	if cfg.FailThreshold <= 0 {
		cfg.FailThreshold = def.FailThreshold
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = def.OpenDuration
	}
	if cfg.HalfOpenProbes <= 0 {
		cfg.HalfOpenProbes = def.HalfOpenProbes
	}
	return &CircuitBreaker{
		cfg:          cfg,
		state:        CircuitClosed,
		model:        model,
		onTransition: onTransition,
	}
}

// Allow reports whether the current request may proceed. Side effects: may
// transition Open → HalfOpen if the cooldown has elapsed.
func (cb *CircuitBreaker) Allow() bool {
	if cb == nil {
		return true
	}
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
		// Cooldown elapsed — try transitioning to HalfOpen.
		cb.mu.Lock()
		if cb.state == CircuitOpen && time.Since(cb.openedAt) >= cb.cfg.OpenDuration {
			cb.doTransition(CircuitHalfOpen)
		}
		cb.mu.Unlock()
		return cb.acquireHalfOpenSlot()

	case CircuitHalfOpen:
		return cb.acquireHalfOpenSlot()
	}
	return true
}

// acquireHalfOpenSlot atomically claims one of HalfOpenProbes concurrent slots.
func (cb *CircuitBreaker) acquireHalfOpenSlot() bool {
	cnt := atomic.AddInt32(&cb.halfOpenCnt, 1)
	if int(cnt) <= cb.cfg.HalfOpenProbes {
		return true
	}
	atomic.AddInt32(&cb.halfOpenCnt, -1)
	return false
}

// MarkSuccess — call succeeded. HalfOpen → Closed; resets fail counter.
func (cb *CircuitBreaker) MarkSuccess() {
	if cb == nil {
		return
	}
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

// MarkFailure — call failed. Closed → Open at FailThreshold; HalfOpen → Open
// immediately (probe failed → backend still degraded).
func (cb *CircuitBreaker) MarkFailure() {
	if cb == nil {
		return
	}
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

// State — current state. Safe for concurrent reads.
func (cb *CircuitBreaker) State() CircuitState {
	if cb == nil {
		return CircuitClosed
	}
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// doTransition — caller MUST hold cb.mu.Lock().
func (cb *CircuitBreaker) doTransition(to CircuitState) {
	from := cb.state
	cb.state = to
	if cb.onTransition != nil && from != to {
		fn := cb.onTransition
		f, t := from, to
		// Run user callback in a goroutine so a misbehaving callback
		// (panic, blocking) cannot deadlock the breaker. The state has
		// already been updated, so callbacks see consistent state.
		go func() {
			defer func() { _ = recover() }()
			fn(f, t)
		}()
	}
}
