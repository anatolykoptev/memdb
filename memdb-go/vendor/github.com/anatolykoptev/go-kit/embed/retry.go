package embed

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

// RetryPolicy controls how many times and how quickly the embed backend is
// retried on retryable errors (5xx HTTP status by default).
//
// Default policy: MaxAttempts=3, BaseBackoff=200ms, MaxBackoff=5s,
// Multiplier=2.0, Jitter=0.1, RetryableStatus={429, 502, 503, 504}.
// v1 callers using New(cfg, logger) inherit this default via the internal
// HTTPEmbedder/withRetry path — the public RetryPolicy is active for v2 callers.
// Opt-out: WithRetry(embed.NoRetry).
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts (1 = no retry, 0 treated as 1).
	MaxAttempts int
	// BaseBackoff is the initial sleep duration between attempts.
	BaseBackoff time.Duration
	// MaxBackoff caps exponential growth.
	MaxBackoff time.Duration
	// Multiplier is the factor applied to backoff each attempt (e.g. 2.0 = double).
	Multiplier float64
	// Jitter adds randomness: actual sleep = backoff * (1 + Jitter * rand[0,1)).
	// Range 0..1.
	Jitter float64
	// RetryableStatus lists HTTP status codes that trigger a retry.
	// Non-listed status codes (e.g. 4xx) return immediately without retry.
	RetryableStatus []int
}

// NoRetry is an explicit opt-out from the default retry policy.
// Pass via WithRetry(embed.NoRetry) to disable all retries.
var NoRetry = RetryPolicy{MaxAttempts: 1}

// defaultRetryPolicy returns the standard production retry configuration.
func defaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:     3,
		BaseBackoff:     200 * time.Millisecond,
		MaxBackoff:      5 * time.Second,
		Multiplier:      2.0,
		Jitter:          0.1,
		RetryableStatus: []int{429, 502, 503, 504},
	}
}

// isRetryableStatusPolicy reports whether the given HTTP status code should be
// retried according to the policy. Always returns false for status==0.
func (p RetryPolicy) isRetryableStatusPolicy(code int) bool {
	for _, s := range p.RetryableStatus {
		if s == code {
			return true
		}
	}
	return false
}

// computeBackoff returns the sleep duration for the given attempt number
// (0-indexed: attempt=0 is the first retry, after the initial failure).
func (p RetryPolicy) computeBackoff(attempt int) time.Duration {
	if p.BaseBackoff <= 0 {
		return 0
	}
	backoff := float64(p.BaseBackoff) * math.Pow(p.Multiplier, float64(attempt))
	if p.MaxBackoff > 0 && backoff > float64(p.MaxBackoff) {
		backoff = float64(p.MaxBackoff)
	}
	if p.Jitter > 0 {
		backoff *= 1 + p.Jitter*rand.Float64() //nolint:gosec
	}
	return time.Duration(backoff)
}

// maxAttempts returns the effective max attempts (coercing 0 to 1).
func (p RetryPolicy) maxAttempts() int {
	if p.MaxAttempts <= 0 {
		return 1
	}
	return p.MaxAttempts
}

// do executes fn with retry per policy. On a retryable error it sleeps and
// retries up to MaxAttempts total attempts. Non-retryable errors (4xx) return
// immediately. ctx cancellation aborts the sleep and returns ctx.Err.
//
// obs.OnRetry is fired for each retry (not for the initial attempt).
// Increments embed_retry_attempt_total metric on each attempt after the first.
func do[T any](ctx context.Context, p RetryPolicy, model string, obs Observer, fn func() (T, error)) (T, error) {
	maxA := p.maxAttempts()
	var (
		result T
		err    error
	)
	for attempt := 0; attempt < maxA; attempt++ {
		result, err = fn()
		if err == nil {
			return result, nil
		}

		// Determine if we should retry.
		var statusCode int
		var isHTTPErr bool
		var httpStatusErr *errHTTPStatus
		if errors.As(err, &httpStatusErr) {
			statusCode = httpStatusErr.Code
			isHTTPErr = true
		}

		if isHTTPErr && !p.isRetryableStatusPolicy(statusCode) {
			// 4xx or other non-retryable HTTP status — return immediately.
			recordGiveup(model, "4xx")
			return result, err
		}

		if attempt == maxA-1 {
			// Last attempt exhausted.
			if isHTTPErr && p.isRetryableStatusPolicy(statusCode) {
				recordGiveup(model, "exhausted")
			}
			break
		}

		// Emit retry metric and hook.
		nextAttempt := attempt + 1
		recordRetryAttempt(model, nextAttempt)
		safeCall(func() { obs.OnRetry(ctx, nextAttempt, err) })

		// Backoff sleep with ctx cancellation support.
		// time.NewTimer + Stop() drain avoids the timer leak that time.After
		// causes when ctx fires before the sleep expires.
		sleep := p.computeBackoff(attempt)
		if sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return result, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return result, err
}

// ── Legacy internal retry helpers ─────────────────────────────────────────────
// These are used by the v1 backend Embedders (HTTPEmbedder, OllamaClient,
// VoyageClient) via their own withRetry calls. They are KEPT unchanged so that
// v1 callers preserve existing retry behaviour without going through RetryPolicy.

// retryConfig holds exponential backoff parameters (v1 internal only).
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

// withRetry executes fn up to cfg.maxAttempts times with exponential backoff.
// Used by v1 backend Embedders (HTTPEmbedder, OllamaClient, VoyageClient).
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
