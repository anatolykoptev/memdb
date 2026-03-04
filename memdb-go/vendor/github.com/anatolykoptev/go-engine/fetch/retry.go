package fetch

import (
	"errors"

	stealth "github.com/anatolykoptev/go-stealth"
)

// ErrPermanentlyFailed is returned when a URL is permanently failed in the tracker.
var ErrPermanentlyFailed = errors.New("permanently failed: URL exceeded retry limit or returned permanent error")

// RetryHookFunc is called before each retry attempt (re-exported from go-stealth).
type RetryHookFunc = stealth.RetryHookFunc

// WithRetryHookCtx returns a context carrying a retry hook (re-exported from go-stealth).
var WithRetryHookCtx = stealth.WithRetryHook

// RetryTracker tracks per-URL retry state (re-exported from go-stealth).
type RetryTracker = stealth.RetryTracker

// NewRetryTracker creates a per-URL retry tracker (re-exported from go-stealth).
var NewRetryTracker = stealth.NewRetryTracker

// HttpStatusError wraps a non-OK HTTP status code (re-exported from go-stealth).
type HttpStatusError = stealth.HttpStatusError

// WithRetryTracker sets a per-URL retry tracker on the fetcher.
// When set, FetchBody checks ShouldRetry before each request and records
// attempts/successes after each request.
func WithRetryTracker(t *stealth.RetryTracker) Option {
	return func(f *Fetcher) { f.retryTracker = t }
}
