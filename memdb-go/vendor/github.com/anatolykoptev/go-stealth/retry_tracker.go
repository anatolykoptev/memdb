package stealth

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

// RetryTracker tracks per-URL retry state across calls.
// Thread-safe. Entries expire after TTL via lazy eviction.
type RetryTracker struct {
	mu          sync.Mutex
	entries     map[string]*retryEntry
	ttl         time.Duration
	maxAttempts int
}

type retryEntry struct {
	attempts    int
	lastAttempt time.Time
	permanent   bool
}

// NewRetryTracker creates a tracker that allows up to maxAttempts per URL
// before blocking retries. Entries expire after ttl (lazy eviction).
func NewRetryTracker(maxAttempts int, ttl time.Duration) *RetryTracker {
	return &RetryTracker{
		entries:     make(map[string]*retryEntry),
		ttl:         ttl,
		maxAttempts: maxAttempts,
	}
}

// ShouldRetry returns true if the URL is eligible for another attempt.
// Unknown URLs are always retryable. Expired entries are evicted lazily.
func (t *RetryTracker) ShouldRetry(url string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[url]
	if !ok {
		return true
	}

	if !e.permanent && time.Since(e.lastAttempt) > t.ttl {
		delete(t.entries, url)
		return true
	}

	if e.permanent {
		return false
	}

	return e.attempts < t.maxAttempts
}

// RecordAttempt records a failed attempt for the URL. If the error is a
// permanent HTTP status (4xx client errors), the URL is marked as permanently
// failed and no further retries are allowed regardless of attempt count.
func (t *RetryTracker) RecordAttempt(url string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[url]
	if !ok {
		e = &retryEntry{}
		t.entries[url] = e
	}

	e.attempts++
	e.lastAttempt = time.Now()
	e.permanent = isPermanentError(err)
}

// RecordSuccess clears all retry state for the URL, making it fully
// retryable again.
func (t *RetryTracker) RecordSuccess(url string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.entries, url)
}

// isPermanentError returns true for HTTP status codes that indicate the
// request will never succeed regardless of retries.
func isPermanentError(err error) bool {
	var httpErr *HttpStatusError
	if !errors.As(err, &httpErr) {
		return false
	}

	switch httpErr.StatusCode {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusGone,
		http.StatusUnavailableForLegalReasons:
		return true
	}

	return false
}
