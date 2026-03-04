package ratelimit

import (
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
	}
}

// Limiter tracks per-key sliding window rate limits.
type Limiter struct {
	config Config
	store  Store
}

// NewLimiter creates a rate limiter with the given config.
func NewLimiter(cfg Config, opts ...LimiterOption) *Limiter {
	l := &Limiter{
		config: cfg,
		store:  NewMemoryStore(),
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Allow returns true if a request can be made for the given key.
// Atomically increments the counter when returning true.
func (l *Limiter) Allow(key string) bool {
	now := time.Now()

	blocked := l.store.GetBlocked(key)
	if now.Before(blocked) {
		return false
	}

	count, _ := l.store.Increment(key, l.config.WindowDuration)
	return count <= l.config.RequestsPerWindow
}

// MarkRateLimited sets the blocked-until time for a key (e.g. from a 429 response).
func (l *Limiter) MarkRateLimited(key string, until time.Time) {
	l.store.SetBlocked(key, until)
}

// IsRateLimited returns true if the key is currently blocked.
func (l *Limiter) IsRateLimited(key string) bool {
	now := time.Now()

	blocked := l.store.GetBlocked(key)
	if now.Before(blocked) {
		return true
	}

	count, windowStart := l.store.Count(key, l.config.WindowDuration)
	if count >= l.config.RequestsPerWindow && now.Sub(windowStart) <= l.config.WindowDuration {
		return true
	}
	return false
}

// AvailableAt returns the time when the given key will become available.
// Returns zero time if available right now.
func (l *Limiter) AvailableAt(key string) time.Time {
	now := time.Now()
	var earliest time.Time

	blocked := l.store.GetBlocked(key)
	if now.Before(blocked) {
		earliest = blocked
	}

	count, windowStart := l.store.Count(key, l.config.WindowDuration)
	if count >= l.config.RequestsPerWindow {
		windowEnd := windowStart.Add(l.config.WindowDuration)
		if now.Before(windowEnd) {
			if earliest.IsZero() || windowEnd.Before(earliest) {
				earliest = windowEnd
			}
		}
	}

	return earliest
}
