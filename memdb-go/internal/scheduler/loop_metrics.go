package scheduler

// loop_metrics.go — metrics for the periodicLoop primitive.
//
// Instruments:
//   - memdb.scheduler.loop_iterations_total{name,outcome} counter
//   - memdb.scheduler.loop_duration_ms{name} histogram
//
// Pre-registered at zero for name=pagerank and name=periodic_reorg so
// Prometheus scrapers see the series immediately (before the first tick).

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	loopMetricsOnce        sync.Once
	loopMetricsInstruments *loopMetricsStruct
)

type loopMetricsStruct struct {
	// Iterations counts per-loop-per-outcome ticks.
	// Labels: name (loop name), outcome (success|error|skipped_other_leader).
	Iterations metric.Int64Counter

	// DurationMs records wall-clock milliseconds per iteration.
	// Label: name.
	DurationMs metric.Float64Histogram
}

// labelLoopOutcome returns an OTel measurement option for (name, outcome) labels.
func labelLoopOutcome(name, outcome string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("name", name),
		attribute.String("outcome", outcome),
	)
}

// labelLoopName returns an OTel measurement option for the name label only.
func labelLoopName(name string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("name", name))
}

// loopMx returns the singleton loop instruments, lazy-initialised.
func loopMx() *loopMetricsStruct {
	loopMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/scheduler")

		iters, _ := meter.Int64Counter(
			"memdb.scheduler.loop_iterations_total",
			metric.WithDescription(
				"Total periodicLoop iterations by loop name and outcome "+
					"(success|error|skipped_other_leader)",
			),
		)
		dur, _ := meter.Float64Histogram(
			"memdb.scheduler.loop_duration_ms",
			metric.WithDescription("Wall-clock duration of a single periodicLoop iteration in milliseconds"),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(100, 1000, 10_000, 60_000, 300_000, 1_800_000),
		)

		loopMetricsInstruments = &loopMetricsStruct{
			Iterations: iters,
			DurationMs: dur,
		}

		// Pre-register at zero for known loop names so scrapers see the series
		// before the first tick (matches the db/metrics.go and metrics.go pattern).
		ctx := context.Background()
		for _, name := range []string{"pagerank", "periodic_reorg"} {
			for _, outcome := range []string{"success", "error", "skipped_other_leader"} {
				iters.Add(ctx, 0, labelLoopOutcome(name, outcome))
			}
			dur.Record(ctx, 0, labelLoopName(name))
		}
	})
	return loopMetricsInstruments
}
