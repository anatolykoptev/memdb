// Package scheduler — worker domain metrics (Prometheus via OTel).
package scheduler

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	schedMetricsOnce        sync.Once
	schedMetricsInstruments *schedMetricsStruct
)

type schedMetricsStruct struct {
	Messages metric.Int64Counter     // labels: label, outcome
	Duration metric.Float64Histogram // labels: label
	DLQ      metric.Int64Counter     // labels: label
}

// schedMx returns the singleton scheduler instruments, lazy-initialised.
func schedMx() *schedMetricsStruct {
	schedMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/scheduler")
		msgs, _ := meter.Int64Counter("memdb.scheduler.messages_total",
			metric.WithDescription("Total scheduler messages processed by label and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.scheduler.duration_ms",
			metric.WithDescription("Scheduler message processing duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		dlq, _ := meter.Int64Counter("memdb.scheduler.dlq_total",
			metric.WithDescription("Total messages moved to Dead Letter Queue by label"),
		)
		schedMetricsInstruments = &schedMetricsStruct{
			Messages: msgs,
			Duration: dur,
			DLQ:      dlq,
		}
	})
	return schedMetricsInstruments
}
