package sparse

import (
	"context"
	"errors"
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
		recordGiveup(primary.model, "4xx")
		return res
	}
	if secondary == nil {
		return res
	}

	fallRes := secondary.embedWithResultUnchained(ctx, texts, opts...)
	if fallRes.Status == StatusOk {
		fallRes.Status = StatusFallback
		recordFallbackUsed(primary.model, secondary.model)
		return fallRes
	}
	return res
}

// isClientError returns true when err represents a 4xx HTTP status code.
// 400 specifically is the embed-server's signal for "bad model" or
// "empty input" — both repeat deterministically on a secondary backend.
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
