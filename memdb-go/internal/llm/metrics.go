// Package llm — domain metrics (Prometheus via OTel).
package llm

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	llmMetricsOnce        sync.Once
	llmMetricsInstruments *llmMetricsStruct
)

type llmMetricsStruct struct {
	Requests metric.Int64Counter
	Duration metric.Float64Histogram
}

// llmMetrics returns the singleton LLM instruments, lazy-initialised.
func llmMetrics() *llmMetricsStruct {
	llmMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/llm")
		reqs, _ := meter.Int64Counter("memdb.llm.requests_total",
			metric.WithDescription("Total LLM chat requests by model and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.llm.duration_ms",
			metric.WithDescription("LLM chat request duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		llmMetricsInstruments = &llmMetricsStruct{
			Requests: reqs,
			Duration: dur,
		}
	})
	return llmMetricsInstruments
}
