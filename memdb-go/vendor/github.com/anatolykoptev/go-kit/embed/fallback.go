package embed

import (
	"context"
	"errors"
	"fmt"
)

// embedWithFallback tries the primary client and, on StatusDegraded with a
// non-4xx error, tries the secondary client. Returns StatusFallback on
// secondary success. Returns the primary's Degraded result if:
//   - the error was a 4xx (caller error — same bug will repeat on secondary)
//   - secondary is nil
//   - secondary also fails
//
// Fallback is capped at depth 1: primary → secondary. No further chaining.
// opts are forwarded to both primary and secondary calls.
func embedWithFallback(
	ctx context.Context,
	primary *Client,
	secondary *Client,
	texts []string,
	opts ...EmbedOpt,
) *Result {
	res := primary.embedWithResultUnchained(ctx, texts, opts...)
	if res.Status != StatusDegraded {
		return res
	}
	if isClientError(res.Err) {
		// 4xx — caller error; secondary would see the same problem.
		recordGiveup(primary.model, "4xx")
		return res
	}
	if secondary == nil {
		return res
	}

	// Attempt secondary.
	fallRes := secondary.embedWithResultUnchained(ctx, texts, opts...)
	if fallRes.Status == StatusOk {
		fallRes.Status = StatusFallback
		recordFallbackUsed(primary.model, secondary.model)
		return fallRes
	}
	// Both failed — return primary's Degraded result.
	return res
}

// isClientError returns true when err represents a 4xx HTTP status code
// (a caller-side error that would repeat if retried against secondary).
func isClientError(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *errHTTPStatus
	if errors.As(err, &statusErr) {
		return statusErr.Code >= 400 && statusErr.Code < 500
	}
	return false
}

// errHTTPStatus is a typed error carrying the HTTP status code and response body
// from a non-2xx response. Using a typed error (rather than a plain fmt.Errorf
// string) allows do() and isClientError() to inspect the status code via
// errors.As — works correctly through fmt.Errorf("%w", ...) wrapping chains.
//
// The Error() string is "status <code>: <body>" — callers that do
// strings.Contains(err.Error(), "status NNN") continue to work.
type errHTTPStatus struct {
	Code int
	Body string
}

func (e *errHTTPStatus) Error() string {
	return fmt.Sprintf("status %d: %s", e.Code, e.Body)
}
