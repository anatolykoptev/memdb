// Package handlers — domain metrics for handler-level instrumentation.
// Instruments are created on first access and read by the Prometheus exporter.
package handlers

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	bufferMetricsOnce sync.Once
	bufferMetrics     *bufferMetricsInstruments
)

type bufferMetricsInstruments struct {
	// FlushErrors counts buffer flush failures by reason.
	// reason ∈ {lua, parse, db, other}. rate(...[5m]) > 10 alerts.
	FlushErrors metric.Int64Counter
}

// bufferMx returns the singleton buffer instruments, lazy-initialised.
func bufferMx() *bufferMetricsInstruments {
	bufferMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/buffer")
		flush, _ := meter.Int64Counter("memdb.buffer.flush_errors",
			metric.WithDescription("Count of buffer flush failures by reason (lua/parse/db/other). Burst alerts via increase([5m])>10."),
		)
		bufferMetrics = &bufferMetricsInstruments{FlushErrors: flush}
	})
	return bufferMetrics
}

var (
	metricsOnce     sync.Once
	feedbackMetrics *feedbackMetricsInstruments
)

type feedbackMetricsInstruments struct {
	Requests   metric.Int64Counter
	Duration   metric.Float64Histogram
	Operations metric.Int64Counter
}

// feedbackMx returns the singleton feedback instruments, lazy-initialised.
func feedbackMx() *feedbackMetricsInstruments {
	metricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/feedback")
		reqs, _ := meter.Int64Counter("memdb.feedback.requests",
			metric.WithDescription("Total feedback requests processed"),
		)
		dur, _ := meter.Float64Histogram("memdb.feedback.duration_ms",
			metric.WithDescription("Feedback pipeline duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		ops, _ := meter.Int64Counter("memdb.feedback.operations",
			metric.WithDescription("Feedback pipeline operations by type (ADD/UPDATE/NONE/KEYWORD_REPLACE/PURE_ADD)"),
		)
		feedbackMetrics = &feedbackMetricsInstruments{
			Requests:   reqs,
			Duration:   dur,
			Operations: ops,
		}
	})
	return feedbackMetrics
}
