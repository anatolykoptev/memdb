package stealth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// RetryConfig controls retry behavior.
type RetryConfig struct {
	MaxRetries  int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
	JitterPct   float64 // random variation (0 = no jitter)
}

// DefaultRetryConfig is suitable for most HTTP calls.
var DefaultRetryConfig = RetryConfig{
	MaxRetries:  3,
	InitialWait: 500 * time.Millisecond,
	MaxWait:     10 * time.Second,
	Multiplier:  2.0,
}

// RetryDo retries fn up to MaxRetries times with exponential backoff.
// Retries only on retryable errors; returns immediately on non-retryable or context cancellation.
func RetryDo[T any](ctx context.Context, rc RetryConfig, fn func() (T, error)) (T, error) {
	return retryDo(ctx, rc, nil, fn)
}

// retryDo is the shared retry loop. If resetFn is non-nil, it is called before
// each retry attempt (not before the first attempt).
func retryDo[T any](ctx context.Context, rc RetryConfig, resetFn func(), fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt <= rc.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		if attempt > 0 && resetFn != nil {
			resetFn()
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !IsRetryable(err) {
			return zero, err
		}

		if attempt < rc.MaxRetries {
			if hook := retryHookFromContext(ctx); hook != nil {
				hook(ctx, attempt+1, rc.MaxRetries, lastErr)
			}

			wait := time.Duration(float64(rc.InitialWait) * math.Pow(rc.Multiplier, float64(attempt)))
			if wait > rc.MaxWait {
				wait = rc.MaxWait
			}
			if rc.JitterPct > 0 {
				jitter := float64(wait) * rc.JitterPct * (2*rand.Float64() - 1)
				wait += time.Duration(jitter)
				if wait < 0 {
					wait = 0
				}
			}
			slog.Debug("retrying", slog.Int("attempt", attempt+1), slog.Duration("wait", wait), slog.Any("error", err))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
	}
	return zero, lastErr
}

// RetryHTTP executes an HTTP request function with retry logic.
func RetryHTTP(ctx context.Context, rc RetryConfig, fn func() (*http.Response, error)) (*http.Response, error) {
	return RetryDo(ctx, rc, func() (*http.Response, error) {
		resp, err := fn()
		if err != nil {
			return nil, err
		}
		if IsRetryableStatus(resp.StatusCode) {
			resp.Body.Close()
			return nil, &HttpStatusError{StatusCode: resp.StatusCode}
		}
		return resp, nil
	})
}

// HttpStatusError wraps a retryable HTTP status code.
// Exported so consumers can use stealth.RetryDo/RetryHTTP directly
// without reimplementing their own error type for errors.As matching.
type HttpStatusError struct {
	StatusCode int
}

func (e *HttpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// IsRetryable returns true for transient errors worth retrying.
func IsRetryable(err error) bool {
	var httpErr *HttpStatusError
	if errors.As(err, &httpErr) {
		return IsRetryableStatus(httpErr.StatusCode)
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	return false
}

// ParseRetryAfter extracts the wait duration from a Retry-After header.
// Handles both delta-seconds ("120") and HTTP-date ("Thu, 01 Dec 2025 16:00:00 GMT") formats.
// Returns 0 if the header is missing or unparseable.
func ParseRetryAfter(resp *http.Response) time.Duration {
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0
	}

	// Try delta-seconds first (most common for 429s)
	if seconds, err := strconv.Atoi(val); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	// Try HTTP-date (RFC 7231 §7.1.1.1)
	for _, layout := range []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC850,
		time.ANSIC,
	} {
		if t, err := time.Parse(layout, val); err == nil {
			delta := time.Until(t)
			if delta > 0 {
				return delta
			}
			return 0
		}
	}

	return 0
}

// IsRetryableStatus returns true for HTTP status codes worth retrying.
func IsRetryableStatus(code int) bool {
	switch code {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}
