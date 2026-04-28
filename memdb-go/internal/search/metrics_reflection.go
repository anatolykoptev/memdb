// Package search — metrics_reflection.go: F2 reflection-loop telemetry.
//
// Pre-registered OTel instruments emitted by the reflection stage.
// Series surface in Prometheus from container start (zero-value pre-record)
// so dashboards / alert rules don't have a "missing series" gap.
package search

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// reflectionMetricsInstruments holds the F2 instruments. One iteration loop
// can produce multiple counter increments (one per iteration outcome) and a
// single duration histogram observation per Reflect() call.
type reflectionMetricsInstruments struct {
	// Iterations counts reflection iterations by outcome. Labels:
	//   outcome = sufficient | needs_raw | missing_info | error | disabled
	Iterations metric.Int64Counter
	// DurationMS records the wall-clock duration of one ReflectionAgent.Reflect call.
	DurationMS metric.Int64Histogram
	// ExtraEmbeddings counts how many additional embeddings the reflection
	// loop triggered (one per missing aspect that re-entered the embed path).
	ExtraEmbeddings metric.Int64Counter
}

var (
	reflectionMxOnce sync.Once
	reflectionMxInst *reflectionMetricsInstruments
)

// reflectionOutcome enumerates the labels emitted by reflectionMx.Iterations.
const (
	reflectionOutcomeSufficient  = "sufficient"
	reflectionOutcomeNeedsRaw    = "needs_raw"
	reflectionOutcomeMissingInfo = "missing_info"
	reflectionOutcomeError       = "error"
	reflectionOutcomeDisabled    = "disabled"
)

// reflectionOutcomes is the canonical set of outcome labels — used for
// pre-registration so all five series are visible from container start.
var reflectionOutcomes = []string{
	reflectionOutcomeSufficient,
	reflectionOutcomeNeedsRaw,
	reflectionOutcomeMissingInfo,
	reflectionOutcomeError,
	reflectionOutcomeDisabled,
}

// reflectionMx returns the lazily-initialised reflection metrics. Safe for
// concurrent use; first call wires + pre-registers all series.
func reflectionMx() *reflectionMetricsInstruments {
	reflectionMxOnce.Do(func() {
		m := otel.Meter("memdb-go/search")
		iter, _ := m.Int64Counter("memdb.reflection.iterations_total",
			metric.WithDescription("F2 reflection-loop iterations by outcome (sufficient|needs_raw|missing_info|error|disabled)"))
		dur, _ := m.Int64Histogram("memdb.reflection.duration_ms",
			metric.WithDescription("F2 reflection-agent LLM call duration in milliseconds"),
			metric.WithExplicitBucketBoundaries(50, 100, 250, 500, 1000, 2000, 5000))
		extra, _ := m.Int64Counter("memdb.reflection.extra_embeddings_total",
			metric.WithDescription("F2 additional embeddings triggered by missing_info aspects (post-reflection re-embed)"))
		reflectionMxInst = &reflectionMetricsInstruments{
			Iterations:      iter,
			DurationMS:      dur,
			ExtraEmbeddings: extra,
		}

		// Pre-register zero values so Prometheus sees every series from container start.
		ctx := context.Background()
		for _, oc := range reflectionOutcomes {
			iter.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		dur.Record(ctx, 0)
		extra.Add(ctx, 0)
	})
	return reflectionMxInst
}
