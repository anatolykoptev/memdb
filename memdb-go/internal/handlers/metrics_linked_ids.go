// Package handlers — M11 F12 linked_memory_ids resolver metrics.
//
// Counters covering the resolver:
//
//   - memdb.linked_resolver.facts_processed_total{outcome}  — counter, fires
//     once per fact the resolver looks at. outcome ∈ {success, empty,
//     llm_error, persist_error, disabled, no_candidates, overloaded,
//     no_embedding}.
//   - memdb.linked_resolver.relations_found_total            — counter,
//     incremented by the number of links the LLM emitted (after validation).
//   - memdb.linked_resolver.relations_filtered_total{reason} — counter,
//     splits the validation drop count by reason. reason ∈ {hallucination
//     (LLM returned an ID not in candidates), invalid_uuid, dup, empty}.
//     Lets operators see WHY filtering kicked in instead of inferring from
//     LLM-output count vs persisted-count diffs.
//   - memdb.linked_resolver.cap_hit_total                    — counter,
//     fires when the merged set is truncated at linkedResolverMaxLinks.
//   - memdb.linked_resolver.inflight_count                   — gauge,
//     current goroutines past the per-fact semaphore.
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
	linkedOutcomeSuccess      = "success"
	linkedOutcomeEmpty        = "empty"
	linkedOutcomeLLMError     = "llm_error"
	linkedOutcomePersistError = "persist_error"
	linkedOutcomeDisabled     = "disabled"
	linkedOutcomeNoCandidates = "no_candidates"
	// F12 followup: split the previous catch-all linkedOutcomeLLMError into
	// distinct outcome labels for the two non-LLM failure modes the trigger
	// path actually hits.
	linkedOutcomeOverloaded  = "overloaded"   // semaphore acquire timed out
	linkedOutcomeNoEmbedding = "no_embedding" // ef.embedding empty → no vector search possible
)

var linkedOutcomes = []string{
	linkedOutcomeSuccess,
	linkedOutcomeEmpty,
	linkedOutcomeLLMError,
	linkedOutcomePersistError,
	linkedOutcomeDisabled,
	linkedOutcomeNoCandidates,
	linkedOutcomeOverloaded,
	linkedOutcomeNoEmbedding,
}

// F12 followup: closed set for the relations_filtered_total{reason} counter.
const (
	linkedFilterHallucination = "hallucination"
	linkedFilterInvalidUUID   = "invalid_uuid"
	linkedFilterDup           = "dup"
	linkedFilterEmpty         = "empty"
)

var linkedFilterReasons = []string{
	linkedFilterHallucination,
	linkedFilterInvalidUUID,
	linkedFilterDup,
	linkedFilterEmpty,
}

var (
	linkedMetricsOnce sync.Once
	linkedInstruments *linkedMetricsStruct
)

type linkedMetricsStruct struct {
	FactsProcessed    metric.Int64Counter       // labels: outcome
	RelationsFound    metric.Int64Counter       // unlabeled
	RelationsFiltered metric.Int64Counter       // labels: reason
	CapHit            metric.Int64Counter       // unlabeled — fires when merged set hits linkedResolverMaxLinks
	Inflight          metric.Int64UpDownCounter // unlabeled — current goroutines past the semaphore
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
		rfilt, _ := meter.Int64Counter("memdb.linked_resolver.relations_filtered_total",
			metric.WithDescription("F12 linked_memory_ids resolver: relations dropped during validation, labelled by reason (hallucination|invalid_uuid|dup|empty)"),
		)
		ch, _ := meter.Int64Counter("memdb.linked_resolver.cap_hit_total",
			metric.WithDescription("F12 linked_memory_ids resolver: merged set truncated at linkedResolverMaxLinks (8)"),
		)
		ic, _ := meter.Int64UpDownCounter("memdb.linked_resolver.inflight_count",
			metric.WithDescription("F12 linked_memory_ids resolver: goroutines currently past the per-fact semaphore (saturation gauge for 45s budget tail)"),
		)
		linkedInstruments = &linkedMetricsStruct{
			FactsProcessed:    fp,
			RelationsFound:    rf,
			RelationsFiltered: rfilt,
			CapHit:            ch,
			Inflight:          ic,
		}
		ctx := context.Background()
		for _, oc := range linkedOutcomes {
			fp.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		for _, r := range linkedFilterReasons {
			rfilt.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", r)))
		}
		rf.Add(ctx, 0)
		ch.Add(ctx, 0)
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

// recordLinkedRelationsFiltered increments the relations_filtered_total
// counter with a reason label. n is bumped per-event (caller can pass >1
// when batching the same reason). Reasons outside linkedFilterReasons are
// dropped — closed-set policy mirrors recordStageDuration in metrics_add.go.
func recordLinkedRelationsFiltered(ctx context.Context, reason string, n int) {
	if n <= 0 {
		return
	}
	valid := false
	for _, r := range linkedFilterReasons {
		if r == reason {
			valid = true
			break
		}
	}
	if !valid {
		return
	}
	mx := linkedMx()
	if mx.RelationsFiltered == nil {
		return
	}
	mx.RelationsFiltered.Add(ctx, int64(n), metric.WithAttributes(attribute.String("reason", reason)))
}

// recordLinkedCapHit fires when mergeLinkedIDs / Resolve truncates the
// emitted set at linkedResolverMaxLinks. Useful as a "should we raise the
// cap" signal — sustained non-zero rate suggests the LLM is finding more
// real links than we let through.
func recordLinkedCapHit(ctx context.Context) {
	mx := linkedMx()
	if mx.CapHit == nil {
		return
	}
	mx.CapHit.Add(ctx, 1)
}

// recordLinkedInflightDelta adjusts the in-flight goroutine gauge. Pass +1
// after the semaphore acquire and -1 in the release defer — the gauge
// shows how close we are to saturating linkedResolverSemaphoreSize (4).
// At sustained 4 with non-trivial p95 latency we are dropping the 45s
// budget tail and need to raise the cap or shed work.
func recordLinkedInflightDelta(ctx context.Context, delta int64) {
	mx := linkedMx()
	if mx.Inflight == nil {
		return
	}
	mx.Inflight.Add(ctx, delta)
}
