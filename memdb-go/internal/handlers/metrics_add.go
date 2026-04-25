// Package handlers — add-pipeline domain metrics.
package handlers

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	addMetricsOnce sync.Once
	addInstruments *addMetricsStruct
)

type addMetricsStruct struct {
	Requests       metric.Int64Counter     // labels: mode, outcome
	Duration       metric.Float64Histogram // labels: mode
	Memories       metric.Int64Counter     // labels: mode (records len(items))
	EmbedBatchSize metric.Float64Histogram // labels: mode (texts per batched Embed call in fast-add)
	// M8 Stream 10 — structural edges emitted at ingest. Type label is one of
	// SAME_SESSION | TIMELINE_NEXT | SIMILAR_COSINE_HIGH (matches relation column).
	StructuralEdges    metric.Int64Counter // labels: type
	SameSessionCapped  metric.Int64Counter // unlabeled — fires when N=20 cap trims SAME_SESSION fan-out
}

func addMx() *addMetricsStruct {
	addMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/add")
		reqs, _ := meter.Int64Counter("memdb.add.requests_total",
			metric.WithDescription("Total add-pipeline requests by mode and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.add.duration_ms",
			metric.WithDescription("Add-pipeline duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		mems, _ := meter.Int64Counter("memdb.add.memories_total",
			metric.WithDescription("Total memories written by add pipeline"),
		)
		batch, _ := meter.Float64Histogram("memdb.add.embed_batch_size",
			metric.WithDescription("Number of texts per batched Embed call in fast-add pipeline"),
		)
		structEdges, _ := meter.Int64Counter("memdb.add.structural_edges_total",
			metric.WithDescription("Structural memory_edges emitted at ingest, labelled by relation type"),
		)
		capCounter, _ := meter.Int64Counter("memdb.add.same_session_capped_total",
			metric.WithDescription("Times the SAME_SESSION fan-out cap (N=20) trimmed candidate edges"),
		)
		addInstruments = &addMetricsStruct{
			Requests: reqs, Duration: dur, Memories: mems, EmbedBatchSize: batch,
			StructuralEdges: structEdges, SameSessionCapped: capCounter,
		}
	})
	return addInstruments
}
