// Package scheduler — worker domain metrics (Prometheus via OTel).
package scheduler

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	schedMetricsOnce        sync.Once
	schedMetricsInstruments *schedMetricsStruct
)

type schedMetricsStruct struct {
	Messages  metric.Int64Counter     // labels: label, outcome
	Duration  metric.Float64Histogram // labels: label
	DLQ       metric.Int64Counter     // labels: label
	TreeReorg metric.Int64Counter     // labels: tier, outcome
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
		tr, _ := meter.Int64Counter("memdb.scheduler.tree_reorg",
			metric.WithDescription("D3 tree reorganizer outcomes (tier in episodic/semantic/all/relation, outcome in created/skipped_below_threshold/error/edge_write_error/hierarchy_write_error/audit_write_error/relation_attempted/relation_written_<RELATION>/relation_skipped/relation_error)"),
		)
		schedMetricsInstruments = &schedMetricsStruct{
			Messages:  msgs,
			Duration:  dur,
			DLQ:       dlq,
			TreeReorg: tr,
		}
		// Pre-register TreeReorg at zero so Prometheus scrapers see the
		// series immediately (matches db/metrics.go pattern).
		tr.Add(context.Background(), 0, metric.WithAttributes(
			attribute.String("tier", ""),
			attribute.String("outcome", ""),
		))
	})
	return schedMetricsInstruments
}
