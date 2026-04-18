// Package embedder — domain metrics (Prometheus via OTel).
package embedder

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	embedderMetricsOnce        sync.Once
	embedderMetricsInstruments *embedderMetricsStruct
)

type embedderMetricsStruct struct {
	Requests  metric.Int64Counter
	Duration  metric.Float64Histogram
	BatchSize metric.Float64Histogram
}

// embedderMetrics returns the singleton embedder instruments, lazy-initialised.
func embedderMetrics() *embedderMetricsStruct {
	embedderMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/embedder")
		reqs, _ := meter.Int64Counter("memdb.embedder.requests_total",
			metric.WithDescription("Total embedding requests by backend and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.embedder.duration_ms",
			metric.WithDescription("Embedding request duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		batch, _ := meter.Float64Histogram("memdb.embedder.batch_size",
			metric.WithDescription("Number of texts per embedding request"),
		)
		embedderMetricsInstruments = &embedderMetricsStruct{
			Requests:  reqs,
			Duration:  dur,
			BatchSize: batch,
		}
	})
	return embedderMetricsInstruments
}
