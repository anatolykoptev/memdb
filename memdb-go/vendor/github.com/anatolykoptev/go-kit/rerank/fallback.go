package rerank

import (
	"context"
)

// rerankWithFallback tries the primary client and, on StatusDegraded with a
// non-4xx error, tries the secondary client. Returns StatusFallback on
// secondary success. Returns the primary's Degraded result if:
//   - the error was a 4xx (caller error — same bug will repeat on secondary)
//   - secondary is nil
//   - secondary also fails
//
// Fallback is capped at depth 1: primary → secondary. No further chaining.
// opts are forwarded to both primary and secondary calls (G1 deviation closed).
func rerankWithFallback(
	ctx context.Context,
	primary *Client,
	secondary Reranker,
	secondaryName string,
	query string,
	docs []Doc,
	opts ...RerankOpt,
) *Result {
	res := primary.rerankInternal(ctx, query, docs, opts...)
	if res.Status != StatusDegraded {
		return res
	}
	if isClientError(res.Err) {
		// 4xx — caller error; secondary would see the same problem.
		recordGiveup(primary.cfg.model, "4xx")
		return res
	}
	if secondary == nil || !secondary.Available() {
		return res
	}

	// Attempt secondary via the public Reranker interface so any backend
	// (Voyage, Jina, future LLM-based rerankers, etc.) plugs in cleanly.
	fallRes, _ := secondary.RerankWithResult(ctx, query, docs, opts...)
	if fallRes != nil && fallRes.Status == StatusOk {
		fallRes.Status = StatusFallback
		recordFallbackUsed(primary.cfg.model, secondaryName)
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
	e, ok := err.(errHTTPStatus) //nolint:errorlint
	if !ok {
		return false
	}
	return e.Code >= 400 && e.Code < 500
}
