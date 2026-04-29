package embed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// retryConfig holds exponential backoff parameters.
type retryConfig struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

var defaultRetry = retryConfig{
	maxAttempts: 3,
	baseDelay:   200 * time.Millisecond,
	maxDelay:    5 * time.Second,
}

// isRetryable returns true for transient errors worth retrying:
// network errors, 429 Too Many Requests, 503 Service Unavailable.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Network / context errors — retry only if context is not cancelled.
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
// reason ∈ {transient, http_429, http_5xx, context}.
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

// withRetry executes fn up to cfg.maxAttempts times with exponential backoff.
// It retries on transient errors returned by fn. fn should return (result, httpStatus, error).
// Pass httpStatus=0 when not applicable (e.g. non-HTTP errors).
func withRetry[T any](ctx context.Context, cfg retryConfig, fn func() (T, int, error)) (T, error) {
	var zero T
	delay := cfg.baseDelay

	for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
		result, status, err := fn()
		if err == nil {
			return result, nil
		}

		last := attempt == cfg.maxAttempts
		retryable := isRetryable(err) || isRetryableStatus(status)

		if last || !retryable {
			return zero, err
		}

		// Record this retry attempt with the classified reason.
		recordRetryReason(retryReason(err, status))

		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(delay):
		}

		delay *= 2
		if delay > cfg.maxDelay {
			delay = cfg.maxDelay
		}
	}
	return zero, errors.New("unreachable")
}
