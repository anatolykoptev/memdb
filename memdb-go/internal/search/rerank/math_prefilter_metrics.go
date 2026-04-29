// Package rerank — MathPrefilter OTel metrics (M14 A1).
package rerank

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	mathPrefilterEngagedCounter metric.Int64Counter
	mathPrefilterSkippedCounter metric.Int64Counter
)

// skipReasons is the closed set of label values for the `reason` attribute.
var mathPrefilterSkipReasons = []string{"no_query_vec", "no_items", "empty_embeddings"}

func init() {
	m := otel.Meter("memdb-go/search/rerank")
	eng, _ := m.Int64Counter("memdb.search.math_prefilter_engaged_total",
		metric.WithDescription("M14 A1: math_prefilter reranker ran successfully (pre-CE diversity stage)"))
	mathPrefilterEngagedCounter = eng

	skp, _ := m.Int64Counter("memdb.search.math_prefilter_skipped_total",
		metric.WithDescription("M14 A1: math_prefilter skipped (reason in no_query_vec|no_items|empty_embeddings)"))
	mathPrefilterSkippedCounter = skp

	// Pre-register all reason labels at zero so dashboards see series from start.
	ctx := context.Background()
	for _, r := range mathPrefilterSkipReasons {
		skp.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", r)))
	}
}

// recordMathPrefilterEngaged bumps the engaged counter. No-op when nil
// (test harnesses without otel init).
func recordMathPrefilterEngaged(ctx context.Context) {
	if mathPrefilterEngagedCounter == nil {
		return
	}
	mathPrefilterEngagedCounter.Add(ctx, 1)
}

// recordMathPrefilterSkipped bumps the skipped counter with a reason label.
// No-op when nil.
func recordMathPrefilterSkipped(ctx context.Context, reason string) {
	if mathPrefilterSkippedCounter == nil {
		return
	}
	mathPrefilterSkippedCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("reason", reason),
	))
}
