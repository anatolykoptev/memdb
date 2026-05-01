// Package handlers — atomic-fact extraction metrics (M11 F8).
//
// Three instruments cover the atomic-fact pipeline:
//
//   - memdb.add.atomic_facts_extracted_total{outcome}  — counter, fires once
//     per extraction call. outcome ∈ {success, empty, llm_error}.
//   - memdb.add.facts_per_chunk                         — histogram of facts
//     produced per call (success path only). buckets: 1, 3, 5, 10, 15, 20.
//   - memdb.add.fact_word_count                         — histogram of
//     individual fact word counts. buckets: 5, 15, 40, 80, 120.
//
// All instruments pre-register at zero so Grafana panels stay alive on a
// cold start (same pattern as metrics_add.go and llm/metrics.go).
package handlers

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	atomicOutcomeSuccess       = "success"
	atomicOutcomeEmpty         = "empty"
	atomicOutcomeLLMError      = "llm_error"
	atomicOutcomeNearDupSkip   = "near_dup_skip"
	atomicOutcomeClassifySkip  = "classify_skip"
	atomicOutcomeHashDedupSkip = "hash_dedup_skip"
)

var atomicOutcomes = []string{
	atomicOutcomeSuccess,
	atomicOutcomeEmpty,
	atomicOutcomeLLMError,
	atomicOutcomeNearDupSkip,
	atomicOutcomeClassifySkip,
	atomicOutcomeHashDedupSkip,
}

var (
	atomicMetricsOnce sync.Once
	atomicInstruments *atomicMetricsStruct
)

type atomicMetricsStruct struct {
	Extracted     metric.Int64Counter     // labels: outcome
	FactsPerChunk metric.Float64Histogram // unlabeled
	FactWordCount metric.Float64Histogram // unlabeled
}

func atomicMx() *atomicMetricsStruct {
	atomicMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/add")
		ext, _ := meter.Int64Counter("memdb.add.atomic_facts_extracted_total",
			metric.WithDescription("Atomic-fact extraction outcomes (success|empty|llm_error|near_dup_skip|classify_skip|hash_dedup_skip)"),
		)
		fpc, _ := meter.Float64Histogram("memdb.add.facts_per_chunk",
			metric.WithDescription("Facts produced per atomic-extraction call"),
			metric.WithExplicitBucketBoundaries(1, 3, 5, 10, 15, 20),
		)
		fwc, _ := meter.Float64Histogram("memdb.add.fact_word_count",
			metric.WithDescription("Word count per extracted atomic fact"),
			metric.WithExplicitBucketBoundaries(5, 15, 40, 80, 120),
		)
		atomicInstruments = &atomicMetricsStruct{
			Extracted: ext, FactsPerChunk: fpc, FactWordCount: fwc,
		}
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, oc := range atomicOutcomes {
			ext.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
	})
	return atomicInstruments
}

// recordAtomicExtractOutcome bumps the outcome counter for one extraction
// call. count is the number of facts (informational; only success uses it).
func recordAtomicExtractOutcome(ctx context.Context, outcome string) {
	mx := atomicMx()
	if mx.Extracted == nil {
		return
	}
	mx.Extracted.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func recordAtomicFactsPerChunk(ctx context.Context, count int) {
	mx := atomicMx()
	if mx.FactsPerChunk == nil {
		return
	}
	mx.FactsPerChunk.Record(ctx, float64(count))
}

func recordAtomicFactWordCount(ctx context.Context, words int) {
	mx := atomicMx()
	if mx.FactWordCount == nil {
		return
	}
	mx.FactWordCount.Record(ctx, float64(words))
}
