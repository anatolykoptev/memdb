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

// RetryError wraps a context error with the last attempt error and the
// attempt count, so errors.Is matches both the context sentinel and the
// attempt error's sentinels, and errors.As reaches the attempt error's
// concrete type via the As method.
//
// Modeled on googleapis/google-cloud-go internal/retry wrappedCallErr
// (PR #4797) and google/go-cloud internal/retry ContextError, extended
// with a structured attempt-count field.
//
// Not errors.Join: the attempt count must be a structured int field, and
// errors.Join has nowhere to put it.
//
// No Unwrap method is provided: exposing lastErr via Unwrap would make
// the cause arm of Is redundant — errors.Is falls back to Unwrap+== and
// finds the cause without ever calling Is, so dropping the cause arm from
// Is would go undetected (the bug this type was created to prevent).
// Instead, As delegates to errors.As on lastErr, giving errors.As the
// same reach without giving errors.Is a back-door around Is.
//
// Shared by the retry package and the leaf packages (embed, sparse,
// rerank) that previously carried their own copies — consolidating here
// prevents the divergence that caused the cause-dropping bug.
type RetryError struct {
	ctxErr   error // context.DeadlineExceeded or context.Canceled
	lastErr  error // last error returned by the retried function
	attempts int   // number of attempts that ran (≥1)
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("after %d attempts: %v; last error: %v", e.attempts, e.ctxErr, e.lastErr)
}

// Is reports true for both the context sentinel and the attempt error's
// sentinels, so errors.Is(err, context.Canceled) and
// errors.Is(err, <underlying>) both succeed on the same value.
//
// Without a Unwrap method, errors.Is cannot fall back to Unwrap+== to
// find the cause — Is is the only path. This makes the cause arm
// testable: dropping it causes errors.Is(err, <cause>) to fail.
func (e *RetryError) Is(target error) bool {
	return e.ctxErr == target || errors.Is(e.lastErr, target)
}

// As delegates to errors.As on the last attempt error so callers can
// reach its concrete type. Replaces Unwrap for errors.As reachability —
// see the type doc comment for why Unwrap is intentionally absent.
func (e *RetryError) As(target any) bool {
	return errors.As(e.lastErr, target)
}

// WrapContextErr wraps a context error with attempt count and the last
// function error. Returns lastErr unchanged when there is no context error
// or no last error.
func WrapContextErr(ctx context.Context, attempts int, lastErr error) error {
	if ctx.Err() == nil || lastErr == nil {
		return lastErr
	}
	return &RetryError{ctxErr: ctx.Err(), lastErr: lastErr, attempts: attempts}
}
