// Package retry provides generic retry logic with exponential backoff.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Default retry constants.
const (
	DefaultMaxAttempts  = 3
	DefaultInitialDelay = 500 * time.Millisecond
	DefaultMaxDelay     = 5 * time.Second
)

// Timer abstracts time.After for testability.
type Timer interface {
	After(d time.Duration) <-chan time.Time
}

// Backoff selects the delay growth strategy between retries.
type Backoff int

const (
	BackoffExponential Backoff = iota // default: delay doubles each attempt
	BackoffFibonacci                  // delay follows fibonacci: 1,1,2,3,5,8... × InitialDelay
)

// Options controls retry behavior. Zero values use Default* constants.
type Options struct {
	MaxAttempts    int
	InitialDelay   time.Duration
	MaxDelay       time.Duration
	MaxElapsedTime time.Duration // total wall-clock budget; 0 = no limit
	Jitter         bool          // add ±25% random jitter to delay
	Timer          Timer         // custom timer for tests; nil = real time.After
	Backoff        Backoff       // backoff strategy (default: exponential)
	AbortOn        []error       // never retry these errors (checked via errors.Is)
	RetryableOnly  bool          // if true, only retry errors implementing Retryable
	OnRetry        func(attempt int, err error) // called after each failed attempt
	RetryIf        func(error) bool // custom predicate; overrides AbortOn + RetryableOnly
}

func (o *Options) applyDefaults() {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.InitialDelay <= 0 {
		o.InitialDelay = DefaultInitialDelay
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = DefaultMaxDelay
	}
}

// applyJitter adds ±25% random variation to a delay.
func applyJitter(d time.Duration) time.Duration {
	quarter := int64(d) / 4 //nolint:mnd // ±25% jitter
	return time.Duration(int64(d) - quarter + rand.Int64N(2*quarter+1))
}

// waitWithContext waits for the given delay, respecting context cancellation.
func waitWithContext(ctx context.Context, delay time.Duration, timer Timer) error {
	afterCh := time.After(delay)
	if timer != nil {
		afterCh = timer.After(delay)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-afterCh:
		return nil
	}
}

// retryDelay computes the actual delay for a retry attempt.
func retryDelay(baseDelay time.Duration, lastErr error, jitter bool) time.Duration {
	delay := baseDelay
	var ra *RetryAfterError
	if errors.As(lastErr, &ra) && ra.Delay > 0 {
		delay = ra.Delay
	}
	if jitter && delay > 0 {
		delay = applyJitter(delay)
	}
	return delay
}

// Do retries fn up to MaxAttempts times with exponential backoff.
// Respects context cancellation. Returns the last error if all attempts fail.
func Do[T any](ctx context.Context, opts Options, fn func() (T, error)) (T, error) {
	opts.applyDefaults()

	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}

	start := time.Now()
	delay := opts.InitialDelay
	prevDelay := time.Duration(0) // for fibonacci tracking
	var lastErr error
	attempts := 0

	for attempt := range opts.MaxAttempts {
		if opts.MaxElapsedTime > 0 && attempt > 0 && time.Since(start) >= opts.MaxElapsedTime {
			break
		}

		if attempt > 0 {
			actualDelay := retryDelay(delay, lastErr, opts.Jitter)
			if err := waitWithContext(ctx, actualDelay, opts.Timer); err != nil {
				var zero T
				return zero, wrapContextErr(ctx, attempts, lastErr)
			}
			switch opts.Backoff {
			case BackoffFibonacci:
				prevDelay, delay = delay, min(prevDelay+delay, opts.MaxDelay)
			default: // BackoffExponential
				delay = min(delay*2, opts.MaxDelay)
			}
		}

		result, err := fn()
		attempts++
		if err == nil {
			return result, nil
		}

		// Permanent error: stop immediately, return unwrapped error.
		var pe *permanentError
		if errors.As(err, &pe) {
			var zero T
			return zero, pe.err
		}

		lastErr = err

		if opts.OnRetry != nil {
			opts.OnRetry(attempt, lastErr)
		}

		if shouldAbort(&opts, lastErr) {
			break
		}
	}

	var zero T
	return zero, wrapContextErr(ctx, attempts, lastErr)
}

// isRetryableStatus reports whether the HTTP status code warrants a retry.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// HTTP retries an HTTP request function, treating 429 and 5xx as retryable.
// The caller is responsible for closing the response body on success.
func HTTP(ctx context.Context, opts Options, fn func() (*http.Response, error)) (*http.Response, error) {
	return Do(ctx, opts, func() (*http.Response, error) {
		resp, err := fn()
		if err != nil {
			return nil, err
		}
		if isRetryableStatus(resp.StatusCode) {
			retryDelay := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			httpErr := &HTTPError{StatusCode: resp.StatusCode}
			if retryDelay > 0 {
				return nil, RetryAfter(retryDelay, httpErr)
			}
			return nil, httpErr
		}
		return resp, nil
	})
}

// parseRetryAfter parses the Retry-After header value as seconds.
func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
