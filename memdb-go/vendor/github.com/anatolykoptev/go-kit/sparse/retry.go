package sparse

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"
)

// RetryConfig holds exponential backoff parameters for sparse retries.
//
// Mirrors the v1 internal retry helper from
// github.com/anatolykoptev/go-kit/embed/retry.go — copied here rather than
// extracted into a shared internal/transportretry package to keep the two
// packages independently versionable. Fields are exported so callers can
// construct a custom policy via WithRetry; pass NoRetry to opt out.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (1 = no retry).
	MaxAttempts int
	// BaseDelay is the initial sleep between attempts.
	BaseDelay time.Duration
	// MaxDelay caps exponential growth of the sleep.
	MaxDelay time.Duration
	// Jitter adds randomness: actual sleep = delay * (1 + Jitter * rand[0,1)).
	// Range 0..1; 0 disables jitter (deterministic backoff).
	Jitter float64
}

// defaultRetry is the standard production retry: 3 attempts, 200ms base,
// 5s cap, 10% jitter. Matches embed/'s default and the SPLADE p95 latency
// budget (single-text long-doc p95 ≈ 460ms post-INT8 quantisation, so a
// 200ms initial sleep does not perceptibly inflate happy-path latency).
// The 10% jitter spreads simultaneous retry storms when many memdb workers
// hit the same transient embed-server outage.
var defaultRetry = RetryConfig{
	MaxAttempts: 3,
	BaseDelay:   200 * time.Millisecond,
	MaxDelay:    5 * time.Second,
	Jitter:      0.1,
}

// isRetryable returns true for transient network timeout errors.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// isRetryableStatus returns true for HTTP status codes that warrant a retry.
func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusBadGateway ||
		code == http.StatusGatewayTimeout
}

// retryReason classifies an error + HTTP status into a metric label.
func retryReason(err error, status int) string {
	if err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)) {
		return "context"
	}
	if status == http.StatusTooManyRequests {
		return "http_429"
	}
	if status >= http.StatusInternalServerError {
		return "http_5xx"
	}
	return "transient"
}

// applyJitter multiplies d by (1 + jitter*rand.Float64()). When jitter <= 0
// it returns d unchanged. math/rand/v2 is seeded automatically — no
// package-level Seed call is required (unlike math/rand v1).
func applyJitter(d time.Duration, jitter float64) time.Duration {
	if jitter <= 0 {
		return d
	}
	return time.Duration(float64(d) * (1 + jitter*rand.Float64())) //nolint:gosec
}

// withRetry executes fn up to cfg.MaxAttempts times with exponential
// backoff + jitter. fn returns (result, status, err): status is the HTTP
// status code of the most recent attempt and is consulted for the
// retryable filter when err is non-nil but not a network timeout. ctx
// cancellation aborts the sleep and returns ctx.Err wrapped.
//
// On each retry-eligible failure (after recordRetryReason but before the
// sleep) the backend's retry-attempt counter is incremented and obs.OnRetry
// fires. backend is the Prometheus label ("http" for HTTPSparseEmbedder);
// obs may be nil — a noopObserver is substituted to keep call sites simple.
func withRetry[T any](ctx context.Context, cfg RetryConfig, backend string, obs Observer, fn func() (T, int, error)) (T, error) {
	if obs == nil {
		obs = noopObserver{}
	}
	var zero T
	delay := cfg.BaseDelay

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		result, status, err := fn()
		if err == nil {
			return result, nil
		}

		last := attempt == cfg.MaxAttempts
		retryable := isRetryable(err) || isRetryableStatus(status)

		if last || !retryable {
			return zero, err
		}

		// Record this retry attempt with the classified reason.
		recordRetryReason(retryReason(err, status))
		recordRetryAttempt(backend, attempt+1)
		safeCall(func() { obs.OnRetry(ctx, attempt+1, err) })

		sleep := applyJitter(delay, cfg.Jitter)
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(sleep):
		}

		delay *= 2
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}
	return zero, errors.New("unreachable")
}
