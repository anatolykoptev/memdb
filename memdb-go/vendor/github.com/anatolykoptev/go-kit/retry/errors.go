package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RetryAfterError wraps an error with a retry-after duration hint.
// Return this from fn to override the computed backoff for the next attempt.
type RetryAfterError struct {
	Delay time.Duration
	Err   error
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// RetryAfter wraps an error with a retry-after duration.
// When Do receives this error, it uses d instead of the exponential backoff.
func RetryAfter(d time.Duration, err error) error {
	return &RetryAfterError{Delay: d, Err: err}
}

// HTTPError is returned when an HTTP response has a retryable status code.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("retryable HTTP status %d", e.StatusCode)
}

// Retryable is an interface that errors can implement to signal
// whether they should be retried. Used with Options.RetryableOnly.
type Retryable interface {
	Retryable() bool
}

type retryableError struct {
	err error
}

func (e *retryableError) Error() string   { return e.err.Error() }
func (e *retryableError) Unwrap() error   { return e.err }
func (e *retryableError) Retryable() bool { return true }

// MarkRetryable wraps an error to signal it should be retried.
// Use with Options.RetryableOnly = true.
func MarkRetryable(err error) error {
	return &retryableError{err: err}
}

// IsRetryable reports whether err should be retried.
// Returns true if err implements Retryable and Retryable() returns true.
func IsRetryable(err error) bool {
	var r Retryable
	if errors.As(err, &r) {
		return r.Retryable()
	}
	return false
}

func shouldAbort(opts *Options, err error) bool {
	if opts.RetryIf != nil {
		return !opts.RetryIf(err)
	}
	for _, target := range opts.AbortOn {
		if errors.Is(err, target) {
			return true
		}
	}
	if opts.RetryableOnly && !IsRetryable(err) {
		return true
	}
	return false
}

// permanentError wraps an error to signal it should never be retried.
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps an error to signal it should never be retried.
// When Do receives a permanent error, it stops immediately and returns the unwrapped error.
// Permanent(nil) returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err is a permanent error.
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// wrapContextErr wraps a context error with attempt count and the last function error.
// Uses %w on ctx.Err() so errors.Is(err, context.DeadlineExceeded) works.
func wrapContextErr(ctx context.Context, attempts int, lastErr error) error {
	if ctx.Err() == nil || lastErr == nil {
		return lastErr
	}
	return fmt.Errorf("after %d attempts: %w: %v", attempts, ctx.Err(), lastErr)
}
