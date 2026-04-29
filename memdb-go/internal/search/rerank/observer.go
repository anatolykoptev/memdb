// Package rerank — go-kit/rerank Observer adapter (M13 W1).
//
// observer.go wires the v2 lifecycle hooks from go-kit/rerank.Observer
// onto OTel counters that scrape into the same Prometheus surface as
// the rest of memdb-go's search instrumentation.
//
// Counters declared here (memdb.search.rerank_*) are a deliberate
// duplicate of the ones go-kit/rerank already exports natively
// (rerank_retry_attempt_total, rerank_circuit_state, ...). The
// duplication is intentional:
//
//  1. memdb-go uses OTel meters → Prometheus exporter, while
//     go-kit/rerank uses promauto.NewCounterVec directly. Without an
//     OTel mirror, dashboards built on the memdb_search_* namespace
//     would be missing the new G1/G2 signals.
//  2. Observer is the supported integration point per go-kit/rerank
//     contract — patching the upstream metrics path would fork the
//     library.
//
// All hooks are non-blocking and panic-recovered (go-kit invokes them
// via safeCall). Counter creation is sync.Once-guarded so the same
// hooks across multiple Client instances share one OTel instrument.

package rerank

import (
	"context"
	"sync"
	"time"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	rerankObsOnce        sync.Once
	rerankObsInstruments *rerankObserverMetrics
)

type rerankObserverMetrics struct {
	BeforeCall metric.Int64Counter
	AfterCall  metric.Int64Counter
	Duration   metric.Float64Histogram
	Retry      metric.Int64Counter
	Circuit    metric.Int64Counter
	Truncate   metric.Int64Counter
	CacheHit   metric.Int64Counter
}

func rerankObsMx() *rerankObserverMetrics {
	rerankObsOnce.Do(func() {
		m := otel.Meter("memdb-go/search/rerank")
		before, _ := m.Int64Counter("memdb.search.rerank_before_call_total",
			metric.WithDescription("M13 W1: rerank HTTP calls dispatched (pre-flight) by model"))
		after, _ := m.Int64Counter("memdb.search.rerank_after_call_total",
			metric.WithDescription("M13 W1: rerank HTTP calls completed by model and status (ok|degraded|fallback|skipped)"))
		dur, _ := m.Float64Histogram("memdb.search.rerank_duration_ms",
			metric.WithDescription("M13 W1: rerank HTTP call duration (ms) by model and status"),
			metric.WithExplicitBucketBoundaries(10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000))
		retry, _ := m.Int64Counter("memdb.search.rerank_retry_total",
			metric.WithDescription("M13 W1: rerank retry attempts (excluding initial attempt) by model"))
		circuit, _ := m.Int64Counter("memdb.search.rerank_circuit_transition_total",
			metric.WithDescription("M13 W1: rerank circuit-breaker state transitions by model, from, to"))
		trunc, _ := m.Int64Counter("memdb.search.rerank_truncate_total",
			metric.WithDescription("M13 W1: rerank doc truncation events by model"))
		cache, _ := m.Int64Counter("memdb.search.rerank_cache_hit_total",
			metric.WithDescription("M13 W1: rerank full-batch cache-hit events by model"))
		rerankObsInstruments = &rerankObserverMetrics{
			BeforeCall: before, AfterCall: after, Duration: dur,
			Retry: retry, Circuit: circuit, Truncate: trunc, CacheHit: cache,
		}
	})
	return rerankObsInstruments
}

// otelObserver fans go-kit lifecycle hooks into OTel counters/histograms.
// The model attribute is captured at construction time so per-model series
// stay consistent even when the server returns a different model name in
// the response (which would be unusual but the v1 client allows it).
type otelObserver struct {
	model string
}

// newOTelObserver returns the singleton observer wired to memdb-go's
// OTel meter. model is recorded as the `model` attribute on every event.
func newOTelObserver(model string) gokitrerank.Observer {
	return &otelObserver{model: model}
}

// modelAttr is a hot-path helper — pre-formats the attribute set for the
// model label. Called once per hook firing.
func (o *otelObserver) modelAttr() metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("model", o.model))
}

// OnBeforeCall fires once per HTTP request right before dispatch.
func (o *otelObserver) OnBeforeCall(ctx context.Context, _ string, _ int) {
	mx := rerankObsMx()
	if mx == nil || mx.BeforeCall == nil {
		return
	}
	mx.BeforeCall.Add(ctx, 1, o.modelAttr())
}

// OnAfterCall fires once per HTTP request after the round-trip, regardless
// of outcome (ok | degraded | fallback | skipped).
func (o *otelObserver) OnAfterCall(ctx context.Context, status gokitrerank.Status, dur time.Duration, _ int) {
	mx := rerankObsMx()
	if mx == nil {
		return
	}
	statusAttr := metric.WithAttributes(
		attribute.String("model", o.model),
		attribute.String("status", status.String()),
	)
	if mx.AfterCall != nil {
		mx.AfterCall.Add(ctx, 1, statusAttr)
	}
	if mx.Duration != nil {
		mx.Duration.Record(ctx, float64(dur.Milliseconds()), statusAttr)
	}
}

// OnRetry fires for each retry attempt (NOT for the initial attempt).
func (o *otelObserver) OnRetry(ctx context.Context, _ int, _ error) {
	mx := rerankObsMx()
	if mx == nil || mx.Retry == nil {
		return
	}
	mx.Retry.Add(ctx, 1, o.modelAttr())
}

// OnCircuitTransition fires on circuit-breaker state changes (G1).
func (o *otelObserver) OnCircuitTransition(ctx context.Context, from, to gokitrerank.CircuitState) {
	mx := rerankObsMx()
	if mx == nil || mx.Circuit == nil {
		return
	}
	mx.Circuit.Add(ctx, 1, metric.WithAttributes(
		attribute.String("model", o.model),
		attribute.String("from", from.String()),
		attribute.String("to", to.String()),
	))
}

// OnCacheHit fires when a full-batch cache hit short-circuits an HTTP call (G4).
func (o *otelObserver) OnCacheHit(ctx context.Context, _ int) {
	mx := rerankObsMx()
	if mx == nil || mx.CacheHit == nil {
		return
	}
	mx.CacheHit.Add(ctx, 1, o.modelAttr())
}

// OnTruncate fires per-doc when token-aware truncation kicks in (G2).
func (o *otelObserver) OnTruncate(ctx context.Context, _ string, _, _ int) {
	mx := rerankObsMx()
	if mx == nil || mx.Truncate == nil {
		return
	}
	mx.Truncate.Add(ctx, 1, o.modelAttr())
}
