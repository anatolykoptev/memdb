// Package embedder — domain metrics (Prometheus via OTel).
package embedder

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
	// RetryTotal (Q5) — retry attempts by classified reason. The reason
	// label is sourced from a closed set so dashboards never see free-form
	// strings: transient (network/timeout), http_429 (rate-limited),
	// http_5xx (upstream 5xx), context (caller cancellation / deadline).
	// Pre-registered at zero below so all four series are visible from
	// container start. Bumped from internal/embedder/retry.go at the
	// point each retry decision is taken.
	RetryTotal metric.Int64Counter
}

// chunkMetrics holds instruments for the client-side chunking feature added
// 2026-05-09 as defense-in-depth against ox-embed-server OOM on large batches.
type chunkMetricsStruct struct {
	// ChunksPerCall is recorded once per Embed call — the number of sub-batches
	// dispatched (1 means no chunking occurred). Useful for tuning chunkSize.
	// Buckets: 1, 2, 4, 8, 16+.
	ChunksPerCall metric.Int64Histogram
	// ChunkSize is recorded once per sub-batch with the sub-batch length.
	// Complements BatchSize (which records the original caller intent).
	// Buckets: 1, 8, 16, 32 (cap), +Inf.
	ChunkSize metric.Int64Histogram
}

var (
	chunkMetricsOnce        sync.Once
	chunkMetricsInstruments *chunkMetricsStruct
)

// embedderChunkMetrics returns the singleton chunk metrics, lazy-initialised.
func embedderChunkMetrics() *chunkMetricsStruct {
	chunkMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/embedder")
		chunksPerCall, _ := meter.Int64Histogram("memdb.embedder.chunks_per_call",
			metric.WithDescription("Number of HTTP sub-batches dispatched per Embed call (1 = no chunking). Tells operators how often client-side chunking triggers."),
			metric.WithExplicitBucketBoundaries(1, 2, 4, 8, 16),
		)
		chunkSize, _ := meter.Int64Histogram("memdb.embedder.chunk_size",
			metric.WithDescription("Size of each individual HTTP sub-batch sent to embed-server (≤ chunkSize cap). Complements BatchSize which records original caller intent."),
			metric.WithExplicitBucketBoundaries(1, 8, 16, 32),
		)
		chunkMetricsInstruments = &chunkMetricsStruct{
			ChunksPerCall: chunksPerCall,
			ChunkSize:     chunkSize,
		}
	})
	return chunkMetricsInstruments
}

// resetChunkMetrics resets the chunk-metrics singleton for testing purposes.
// Only call from test code; concurrent calls are unsafe.
func resetChunkMetrics() {
	chunkMetricsOnce = sync.Once{}
	chunkMetricsInstruments = nil
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
		retries, _ := meter.Int64Counter("memdb.embedder.retry_total",
			metric.WithDescription("Embedder retry attempts by reason (transient|http_429|http_5xx|context)"),
		)
		embedderMetricsInstruments = &embedderMetricsStruct{
			Requests:   reqs,
			Duration:   dur,
			BatchSize:  batch,
			RetryTotal: retries,
		}
		// Pre-register all retry reason labels at zero.
		ctx := context.Background()
		for _, reason := range []string{"transient", "http_429", "http_5xx", "context"} {
			retries.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", reason)))
		}
	})
	return embedderMetricsInstruments
}
