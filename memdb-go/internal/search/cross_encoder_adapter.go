package search

// cross_encoder_adapter.go — cross-encoder live-call legacy entrypoint.
//
// M11/R3: implementation moved to internal/search/rerank/ce.go (typed
// rerank.CrossEncoder strategy). The wrapper exists because
// cross_encoder_precompute.go's fallback path historically called
// rerankMemoryItems unconditionally for a single-batch live rerank; the
// strategy now subsumes both flavours so the wrapper is a one-liner.
//
// The ceLiveHook keeps the legacy memdb.search.ce_live_call_total
// counter wired without leaking otel imports into the rerank package.

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ceLiveHook bumps the legacy CE-live counter on every live HTTP call.
// Wired into rerank.CrossEncoder.OnLiveCall by the precompute wrapper +
// the postProcessResults chain construction.
func ceLiveHook(ctx context.Context) {
	if mx := searchMx(); mx != nil && mx.CELiveCall != nil {
		mx.CELiveCall.Add(ctx, 1)
	}
}

// ceMathFallbackHook bumps the CE→MathReranker fallback counter. reason ∈
// {"degraded", "low_quality", "low_spread", "bypass_cosine", "circuit_open"}.
// circuit_open = per-cube CE breaker tripped on sustained low_spread rate
// (ce_circuit_breaker.go). Wired into rerank.CrossEncoder.OnMathFallback
// by the postProcessResults chain construction; precompute_wrapper does NOT
// route through this path (offline, no live cosine signal needed).
func ceMathFallbackHook(ctx context.Context, reason string) {
	if mx := searchMx(); mx != nil && mx.CEMathFallback != nil {
		mx.CEMathFallback.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	}
}
