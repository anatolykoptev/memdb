package rerank

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// RetryPolicy controls how many times and how quickly callCohere is retried
// on retryable errors (5xx HTTP status by default).
//
// Default policy: MaxAttempts=3, BaseBackoff=200ms, MaxBackoff=2s,
// Multiplier=2.0, Jitter=0.1, RetryableStatus={500,502,503,504}.
// v1 callers using New(cfg, logger) inherit this default.
// Opt-out: WithRetry(rerank.NoRetry).
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
// Pass via WithRetry(rerank.NoRetry) to disable all retries.
var NoRetry = RetryPolicy{MaxAttempts: 1}

// defaultRetryPolicy returns the standard production retry configuration.
func defaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:     3,
		BaseBackoff:     200 * time.Millisecond,
		MaxBackoff:      2 * time.Second,
		Multiplier:      2.0,
		Jitter:          0.1,
		RetryableStatus: []int{500, 502, 503, 504},
	}
}

// isRetryableStatus reports whether the given HTTP status code should be
// retried according to the policy. Always returns false for status==0 (no HTTP
// status, e.g. network error — those are retried if any status is retryable).
func (p RetryPolicy) isRetryableStatus(code int) bool {
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
// Increments rerank_retry_attempt_total metric on each attempt after the first.
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
		if e, ok := err.(errHTTPStatus); ok { //nolint:errorlint
			statusCode = e.Code
			isHTTPErr = true
		}

		if isHTTPErr && !p.isRetryableStatus(statusCode) {
			// 4xx or other non-retryable HTTP status — return immediately.
			recordGiveup(model, "4xx")
			return result, err
		}

		if attempt == maxA-1 {
			// Last attempt exhausted.
			if isHTTPErr && p.isRetryableStatus(statusCode) {
				recordGiveup(model, "exhausted")
			}
			break
		}

		// Emit retry metric and hook.
		nextAttempt := attempt + 1
		recordRetryAttempt(model, nextAttempt)
		safeCall(func() { obs.OnRetry(ctx, nextAttempt, err) })

		// Backoff sleep with ctx cancellation support.
		sleep := p.computeBackoff(attempt)
		if sleep > 0 {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(sleep):
			}
		}
	}
	return result, err
}
