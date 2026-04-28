// Package handlers — M11 F12 linked_memory_ids resolver metrics.
//
// Two counters cover the resolver:
//
//   - memdb.linked_resolver.facts_processed_total{outcome}  — counter, fires
//     once per fact the resolver looks at. outcome ∈ {success, empty,
//     llm_error, persist_error, disabled, no_candidates}.
//   - memdb.linked_resolver.relations_found_total            — counter,
//     incremented by the number of links the LLM emitted (after validation).
//
// Pre-registered at zero so Grafana panels stay alive on cold start (mirrors
// metrics_atomic.go and llm/metrics.go).
package handlers

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	linkedOutcomeSuccess       = "success"
	linkedOutcomeEmpty         = "empty"
	linkedOutcomeLLMError      = "llm_error"
	linkedOutcomePersistError  = "persist_error"
	linkedOutcomeDisabled      = "disabled"
	linkedOutcomeNoCandidates  = "no_candidates"
)

var linkedOutcomes = []string{
	linkedOutcomeSuccess,
	linkedOutcomeEmpty,
	linkedOutcomeLLMError,
	linkedOutcomePersistError,
	linkedOutcomeDisabled,
	linkedOutcomeNoCandidates,
}

var (
	linkedMetricsOnce sync.Once
	linkedInstruments *linkedMetricsStruct
)

type linkedMetricsStruct struct {
	FactsProcessed metric.Int64Counter // labels: outcome
	RelationsFound metric.Int64Counter // unlabeled
}

func linkedMx() *linkedMetricsStruct {
	linkedMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/linked_resolver")
		fp, _ := meter.Int64Counter("memdb.linked_resolver.facts_processed_total",
			metric.WithDescription("F12 linked_memory_ids resolver: facts processed by outcome"),
		)
		rf, _ := meter.Int64Counter("memdb.linked_resolver.relations_found_total",
			metric.WithDescription("F12 linked_memory_ids resolver: total validated relations emitted"),
		)
		linkedInstruments = &linkedMetricsStruct{
			FactsProcessed: fp,
			RelationsFound: rf,
		}
		ctx := context.Background()
		for _, oc := range linkedOutcomes {
			fp.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		rf.Add(ctx, 0)
	})
	return linkedInstruments
}

func recordLinkedFactProcessed(ctx context.Context, outcome string) {
	mx := linkedMx()
	if mx.FactsProcessed == nil {
		return
	}
	mx.FactsProcessed.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func recordLinkedRelationsFound(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	mx := linkedMx()
	if mx.RelationsFound == nil {
		return
	}
	mx.RelationsFound.Add(ctx, int64(n))
}
