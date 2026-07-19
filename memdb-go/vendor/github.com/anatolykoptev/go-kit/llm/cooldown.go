// Package llm — cooldown.go: per-model quota-aware cooldown for the endpoint
// fallback chain (WithEndpoints).
//
// Why: free-tier LLM quotas are GLOBAL across the fleet (e.g. cerebras 1M
// tok/day). When a chain's primary model is exhausted it returns 429 (or a
// 503 marking auth/quota unavailable) on EVERY call. Without cooldown each call
// pays the dead-primary hop (1 RTT) and logs one error line. After
// FailThreshold observed quota-fails this puts the model in a short cooldown and
// the chain SKIPS it, going straight to the next healthy model — dropping both
// the per-call dead hop and ~99% of this path's log noise at steady state.
//
// This is the per-model reactive circuit-skip pattern used by LiteLLM (each
// deployment has health + cooldown status; repeated 429 stops sending to that
// deployment for recovery_timeout, honouring retry_after). It is orthogonal to
// and composes with the existing outer WithCircuitBreaker (which is keyed on the
// single construction model and wraps the WHOLE client — wrong granularity for
// a multi-model chain).
//
// Invariants:
//   - Default-off: no WithModelCooldown option → c.cooldown is nil → zero state,
//     zero behaviour change vs the no-cooldown path.
//   - Never fail-closed: the executeInner loop never skips the LAST non-cooled
//     candidate; if EVERY model is cooled it still attempts the primary
//     (degraded > dead) and surfaces the real upstream error.
//   - Concurrency-safe: the map is RWMutex-guarded (clients live in multi-
//     goroutine services). Mirrors CircuitBreaker's RWMutex discipline.
package llm

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cooldown defaults — applied when a CooldownConfig field is zero.
const (
	defaultCooldownFailThreshold = 2
	defaultCooldownDuration      = 60 * time.Second
	defaultCooldownMax           = 10 * time.Minute
)

// CooldownConfig tunes the per-model cooldown. Zero values fill from defaults.
type CooldownConfig struct {
	// FailThreshold — consecutive quota-class fails on a model before it is put
	// in cooldown. Default 2 (need two, not one, to ride out a transient 429).
	FailThreshold int
	// Default — cooldown duration when the upstream gives no Retry-After.
	// Default 60s.
	Default time.Duration
	// Max — clamp on Retry-After (defends against absurd server values like
	// "Retry-After: 999999999"). Default 10m.
	Max time.Duration
}

func (cfg CooldownConfig) withDefaults() CooldownConfig {
	if cfg.FailThreshold <= 0 {
		cfg.FailThreshold = defaultCooldownFailThreshold
	}
	if cfg.Default <= 0 {
		cfg.Default = defaultCooldownDuration
	}
	if cfg.Max <= 0 {
		cfg.Max = defaultCooldownMax
	}
	return cfg
}

// modelCooldown tracks per-model cooldown state. Thread-safe: reads (cooling)
// take RLock, writes (recordFailure/recordSuccess) take Lock.
type modelCooldown struct {
	mu    sync.RWMutex
	until map[string]time.Time // model -> earliest-retry instant
	fails map[string]int       // model -> consecutive quota-fails
	cfg   CooldownConfig
	clock func() time.Time // injectable for tests; default time.Now
	// onChange fires on cooldown entry (cooling=true) and on recovery
	// (cooling=false). Optional. Dispatched async with panic recovery (see
	// fireCooldownObserver), so a panicking or blocking observer cannot crash or
	// stall the request path — but it must still be cheap, and ordering across
	// events is not guaranteed (goroutine scheduling).
	onChange func(model string, cooling bool, d time.Duration)
}

func newModelCooldown(cfg CooldownConfig) *modelCooldown {
	return &modelCooldown{
		until: make(map[string]time.Time),
		fails: make(map[string]int),
		cfg:   cfg.withDefaults(),
		clock: time.Now,
	}
}

func (m *modelCooldown) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// cooling reports whether model is currently in cooldown. nil-safe (returns
// false) so a missing cooldown is never a reason to skip.
//
// TTL-driven recovery: a model is SKIPPED while cooled (executeInner never
// attempts it), so it never sees a 200 and recordSuccess never fires for it.
// Without this, an expired-but-uncleared until entry would (a) leak in the map
// and (b) leave the recovery observer edge (cooling=false) un-fired, so the
// llm_model_cooldown_active gauge would stay stuck at 1 for hours after the
// quota actually recovered. So when cooling() observes an expired window it
// CLEANS the state (delete until, reset fails) and fires the recovery edge —
// making cooling() the recovery authority for the skipped-model path, the
// mirror of recordSuccess for the attempted-model path.
//
// Lock upgrade: the active-window common case stays on the RLock fast path
// (no write). Only the expired case upgrades to a write Lock, re-checks under
// it (a concurrent recordFailure may have re-cooled with a fresh window, or a
// concurrent cooling() may have already cleaned the entry — either way we must
// not clobber fresh state or double-fire), deletes, then fires the observer
// OUTSIDE the lock. Mirrors recordFailure/recordSuccess discipline.
func (m *modelCooldown) cooling(model string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	until, ok := m.until[model]
	if !ok {
		m.mu.RUnlock()
		return false
	}
	if m.now().Before(until) {
		m.mu.RUnlock()
		return true // active window — fast path, no write
	}
	m.mu.RUnlock()

	// Expired window: upgrade to a write lock and clean up the lapsed state.
	m.mu.Lock()
	// Re-check under the write lock: between RUnlock and Lock another goroutine
	// may have re-cooled this model (fresh window) or already cleaned it.
	until, ok = m.until[model]
	if !ok {
		m.mu.Unlock()
		return false // already cleaned by a concurrent cooling()
	}
	if m.now().Before(until) {
		m.mu.Unlock()
		return true // re-cooled with a fresh window by a concurrent recordFailure
	}
	// Still our expired entry — clean it and fire the recovery edge.
	delete(m.until, model)
	delete(m.fails, model)
	onChange := m.onChange
	m.mu.Unlock()

	if onChange != nil {
		fireCooldownObserver(onChange, model, false, 0)
	}
	return false
}

// recordFailure is called from executeInner ONLY for quota-class errors. It
// bumps the consecutive-fail counter; on reaching FailThreshold it enters
// cooldown for retryAfter (clamped to cfg.Max) or cfg.Default when retryAfter
// is zero. nil-safe no-op.
func (m *modelCooldown) recordFailure(model string, retryAfter time.Duration) {
	if m == nil {
		return
	}
	d := m.cfg.Default
	if retryAfter > 0 {
		d = retryAfter
	}
	if d > m.cfg.Max {
		d = m.cfg.Max
	}

	m.mu.Lock()
	m.fails[model]++
	reached := m.fails[model] >= m.cfg.FailThreshold
	// wasActivelyCooling = an unexpired window is open RIGHT NOW. Keyed on live
	// state, not mere map presence: an until entry can outlive its TTL until the
	// next cooling() read or recordSuccess deletes it, so a model can ride its
	// window out silently — it never sees a 200 because it's being skipped — then
	// re-cool on a fresh burst. Keying dedup on presence would swallow that
	// re-entry event; keying on active-window emits one entry event PER cooldown
	// window (Decision 3, per-window re-event) regardless of WHEN the lapsed entry
	// was swept (eager via cooling(), or still present here). Spam is naturally
	// bounded: at most one event per window, and a window is ≥ FailThreshold fresh
	// fails long.
	until, present := m.until[model]
	wasActivelyCooling := present && m.now().Before(until)
	if reached {
		m.until[model] = m.now().Add(d)
	}
	onChange := m.onChange
	m.mu.Unlock()

	// Fire onChange on the transition INTO an active cooldown window (entry).
	// Run async + recover so a misbehaving observer (panic/block) cannot stall or
	// crash the request path. Mirrors CircuitBreaker.doTransition (circuit.go).
	if reached && !wasActivelyCooling && onChange != nil {
		fireCooldownObserver(onChange, model, true, d)
	}
}

// recordSuccess clears fails+until for model (a 200 means the quota recovered).
// nil-safe no-op.
func (m *modelCooldown) recordSuccess(model string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	_, wasCooling := m.until[model]
	delete(m.fails, model)
	delete(m.until, model)
	onChange := m.onChange
	m.mu.Unlock()

	if wasCooling && onChange != nil {
		fireCooldownObserver(onChange, model, false, 0)
	}
}

// fireCooldownObserver runs the optional onChange callback in its own goroutine
// with panic recovery, so a misbehaving observer (panic or block) cannot stall
// or crash the request path that triggered the cooldown transition. Mirrors the
// CircuitBreaker.doTransition discipline (circuit.go). The transition has already
// been committed to the map under lock before this fires, so the observer sees
// consistent state.
func fireCooldownObserver(fn func(model string, cooling bool, d time.Duration), model string, cooling bool, d time.Duration) {
	go func() {
		defer func() { _ = recover() }()
		fn(model, cooling, d)
	}()
}

// quotaBodyMarkers are substrings (lowercased) that mark a 503 as quota/auth
// exhaustion rather than a transient gateway blip. Observed on the cliproxyapi
// fleet: "no auth available (model=...)" with type "auth_unavailable".
var quotaBodyMarkers = []string{
	"auth_unavailable",
	"no auth available",
	"quota",
	"rate_limit",
	"rate limit",
	"insufficient_quota",
}

// checkMarkers reports whether apiErr's Type, Code, or Body contains a quota/auth marker.
// Used by isQuotaError (for the 503 case) and by ClassifyErrorType (for the 403 case)
// so both share the same detection logic without routing 403 through the cooldown path.
func checkMarkers(a *APIError) bool {
	hay := strings.ToLower(a.Type + " " + a.Code + " " + a.Body)
	for _, marker := range quotaBodyMarkers {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

// isQuotaError reports whether err is a quota-class failure that should drive
// per-model cooldown. Conservative by design (risk register row 4):
//   - HTTP 429 is ALWAYS quota-class (any body).
//   - HTTP 503 is quota-class ONLY when its parsed Type or body marks
//     quota/auth-unavailable. A bare 503 (transient gateway blip) is NOT cooled.
//
// All other statuses (500/413/4xx) are not quota-class.
// NOTE: 403 is deliberately NOT included — a 403 (even with a quota marker) does
// NOT trigger cooldown. ClassifyErrorType classifies 403+marker as dependency_block
// for metrics purposes by calling checkMarkers directly.
func isQuotaError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusServiceUnavailable:
		return checkMarkers(apiErr)
	default:
		return false
	}
}
