package ratelimit

import (
	"sync"
	"time"
)

// Config defines rate limit parameters.
type Config struct {
	RequestsPerWindow int
	WindowDuration    time.Duration
}

// DefaultConfig is 50 requests per 15 minutes.
var DefaultConfig = Config{
	RequestsPerWindow: 50,
	WindowDuration:    15 * time.Minute,
}

// LimiterOption configures a Limiter.
type LimiterOption func(*Limiter)

// WithStore sets a custom store for the Limiter.
// Default is an in-memory store.
func WithStore(s Store) LimiterOption {
	return func(l *Limiter) {
		l.store = s
		l.storeProvided = true
	}
}

// WithClock injects the time source used for window expiry and blocked-until
// checks. Default is time.Now. Tests pass a fake clock to advance time
// deterministically instead of sleeping. The same clock is propagated to the
// default in-memory store so the window arithmetic stays consistent.
func WithClock(clock func() time.Time) LimiterOption {
	return func(l *Limiter) {
		if clock != nil {
			l.clock = clock
		}
	}
}

// Limiter tracks per-key sliding window rate limits. The per-window cap is the
// static Config value by default but can be raised or lowered per key at
// runtime via UpdateLimit (e.g. driven by an x-rate-limit-limit response
// header) so the limiter self-tunes to the upstream's real per-key budget.
type Limiter struct {
	config Config
	store  Store
	clock  func() time.Time

	storeProvided bool

	mu        sync.RWMutex
	perKeyCap map[string]int // adaptive per-key cap override; absent ⇒ use config cap
}

// NewLimiter creates a rate limiter with the given config.
func NewLimiter(cfg Config, opts ...LimiterOption) *Limiter {
	l := &Limiter{
		config:    cfg,
		clock:     time.Now,
		perKeyCap: make(map[string]int),
	}
	for _, o := range opts {
		o(l)
	}
	// Build the default store AFTER options so it inherits the resolved clock.
	// A caller-provided store (WithStore) is used as-is.
	if !l.storeProvided {
		l.store = newMemoryStore(l.clock)
	}
	return l
}

// UpdateLimit sets the effective per-window cap for a single key, overriding the
// static Config cap for that key only. Non-positive limits (e.g. an absent or
// malformed header) are ignored so a key is never collapsed to deny-everything.
// This is the adaptive hook: a 200/429 response carrying x-rate-limit-limit
// calls UpdateLimit(endpoint, limit) and the limiter tracks the real budget.
//
// The override is sticky: once set it persists (last-known-good budget) and is
// never reset to the static cap by a subsequent non-positive value. The
// perKeyCap map has no eviction, so callers must use a BOUNDED key space (e.g. a
// fixed set of endpoint names), not unbounded per-request keys.
func (l *Limiter) UpdateLimit(key string, limit int) {
	if limit <= 0 {
		return
	}
	l.mu.Lock()
	l.perKeyCap[key] = limit
	l.mu.Unlock()
}

// capFor returns the effective cap for a key: the adaptive per-key override if
// present, otherwise the static Config cap.
func (l *Limiter) capFor(key string) int {
	l.mu.RLock()
	c, ok := l.perKeyCap[key]
	l.mu.RUnlock()
	if ok {
		return c
	}
	return l.config.RequestsPerWindow
}

// Allow returns true if a request can be made for the given key, incrementing
// the counter when it does. The cap is read eventually-consistently: a
// concurrent UpdateLimit may land between the increment and the cap comparison,
// so at the exact instant a cap changes the admit decision can be off by one.
// This is harmless for pacing (caps move only by deliberate header-driven
// updates) and keeps Allow lock-light on the hot path.
func (l *Limiter) Allow(key string) bool {
	now := l.clock()

	blocked := l.store.GetBlocked(key)
	if now.Before(blocked) {
		return false
	}

	count, _ := l.store.Increment(key, l.config.WindowDuration)
	return count <= l.capFor(key)
}

// MarkRateLimited sets the blocked-until time for a key (e.g. from a 429
// response). `until` is compared against the Limiter's clock (see WithClock), so
// under a non-default clock it must be expressed on that same time base.
func (l *Limiter) MarkRateLimited(key string, until time.Time) {
	l.store.SetBlocked(key, until)
}

// IsRateLimited returns true if the key is currently blocked.
func (l *Limiter) IsRateLimited(key string) bool {
	now := l.clock()

	blocked := l.store.GetBlocked(key)
	if now.Before(blocked) {
		return true
	}

	count, windowStart := l.store.Count(key, l.config.WindowDuration)
	if count >= l.capFor(key) && now.Sub(windowStart) <= l.config.WindowDuration {
		return true
	}
	return false
}

// AvailableAt returns the time when the given key will become available.
// Returns zero time if available right now.
func (l *Limiter) AvailableAt(key string) time.Time {
	now := l.clock()
	var earliest time.Time

	blocked := l.store.GetBlocked(key)
	if now.Before(blocked) {
		earliest = blocked
	}

	count, windowStart := l.store.Count(key, l.config.WindowDuration)
	if count >= l.capFor(key) {
		windowEnd := windowStart.Add(l.config.WindowDuration)
		if now.Before(windowEnd) {
			if earliest.IsZero() || windowEnd.Before(earliest) {
				earliest = windowEnd
			}
		}
	}

	return earliest
}
