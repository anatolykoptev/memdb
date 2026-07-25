// Package handlers — add-pipeline domain metrics.
package handlers

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
	StructuralEdges   metric.Int64Counter // labels: type
	SameSessionCapped metric.Int64Counter // unlabeled — fires when N=20 cap trims SAME_SESSION fan-out
	// M11 R1 — per-stage timing for the fine-add pipeline.
	// stage label is one of: classify | extract | embed | apply | fanout.
	StageDuration metric.Float64Histogram
	// 2026-04-29 cross-session-dedup fix — counter that fires every time
	// isDuplicate skips an incoming write. Previously this was a silent
	// drop (only Debug log), so the cross-session false-positive that
	// killed 5/6 LoCoMo conv-26 sessions went unnoticed for 20 days.
	// Labels: same_session ∈ {true,false} so dashboards can split
	// "legitimate same-session dedup" from "cross-session false-positive".
	DuplicateDrop metric.Int64Counter
	// FineFallback fires when nativeFineAddForCube degrades to fast-mode
	// because the LLM extraction stage couldn't produce results. reason ∈
	// {llm_timeout, circuit_open, canceled, llm_error, unknown}. The /add
	// request still succeeds — caller gets fast-mode rows (no atomic /
	// linked / event_dates metadata).
	FineFallback metric.Int64Counter
	// PF-6: hash dedup degraded — PG error on FilterExistingContentHashes.
	HashDedupDegraded metric.Int64Counter
	// PF-14: vector dedup error — PG error on VectorSearch in isDuplicate.
	VectorDedupError metric.Int64Counter
	// PF-23: WM cache VAdd failure — Redis write error.
	WMCacheWriteFailed metric.Int64Counter
	// BUG B: dedicated failure counter with a bounded reason label. Pre-fix
	// the only signal was memdb_add_requests_total{outcome="error"}, which
	// conflated cancellations, timeouts, and real errors. reason ∈
	// {canceled, timeout, error, llm_exhausted}.
	AddFailures metric.Int64Counter
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
		stageDur, _ := meter.Float64Histogram("memdb.add.stage_duration_ms",
			metric.WithDescription("Per-stage duration of the fine-add pipeline (classify/extract/embed/apply/fanout)"),
			metric.WithUnit("ms"),
		)
		dupDrop, _ := meter.Int64Counter("memdb.add.duplicate_drop_total",
			metric.WithDescription("Writes silently dropped by isDuplicate (cosine ≥ dedupThreshold). Label same_session ∈ {true,false} splits legitimate same-session dedup from cross-session false-positives."),
		)
		fineFb, _ := meter.Int64Counter("memdb.add.fine_fallback_total",
			metric.WithDescription("Fine→fast resilience fallback when LLM extraction unavailable (reason=llm_timeout|circuit_open|canceled|llm_error|unknown)"),
		)
		hashDedup, _ := meter.Int64Counter("memdb.add.hash_dedup_degraded_total",
			metric.WithDescription("Hash dedup degraded due to PG error on FilterExistingContentHashes (best-effort, request still succeeds)"),
		)
		vecDedup, _ := meter.Int64Counter("memdb.add.dedup_vector_error_total",
			metric.WithDescription("Vector dedup error on VectorSearch in isDuplicate (best-effort fail-open, request still succeeds)"),
		)
		wmCache, _ := meter.Int64Counter("memdb.add.wm_cache_write_failed_total",
			metric.WithDescription("WM cache VAdd failure (Redis write error, best-effort)"),
		)
		addFailures, _ := meter.Int64Counter("memdb.add.failures_total",
			metric.WithDescription("Add-pipeline failures by reason (canceled|timeout|error|llm_exhausted) — BUG B: separates cancel/timeout from real errors"),
		)
		addInstruments = &addMetricsStruct{
			Requests: reqs, Duration: dur, Memories: mems, EmbedBatchSize: batch,
			StructuralEdges: structEdges, SameSessionCapped: capCounter,
			StageDuration: stageDur, DuplicateDrop: dupDrop,
			FineFallback:      fineFb,
			HashDedupDegraded: hashDedup, VectorDedupError: vecDedup,
			WMCacheWriteFailed: wmCache,
			AddFailures:        addFailures,
		}
		// Pre-register zero observations so the time series exists before the
		// first real request lands. Keeps Grafana panels alive on a cold start.
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, s := range fineStages {
			stageDur.Record(ctx, 0,
				metric.WithAttributes(attribute.String("stage", s)))
		}
		for _, r := range []string{"llm_timeout", "circuit_open", "canceled", "llm_error", "unknown"} {
			fineFb.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", r)))
		}
		// BUG B: pre-register the failure reason labels at zero.
		for _, r := range []string{"canceled", "timeout", "error", "llm_exhausted"} {
			addFailures.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", r)))
		}
	})
	return addInstruments
}

// fineStages is the canonical list of stage labels for memdb.add.stage_duration_ms.
// Kept in sync with the orchestrator in add_fine.go (nativeFineAddForCube).
var fineStages = []string{"classify", "extract", "embed", "apply", "fanout"}

// fineStagesSet mirrors fineStages as a lookup for closed-set validation in
// recordStageDuration. Keeps the source of truth in fineStages while
// avoiding O(n) on every record.
var fineStagesSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(fineStages))
	for _, s := range fineStages {
		m[s] = struct{}{}
	}
	return m
}()

// recordStageDuration writes time.Since(start) to memdb.add.stage_duration_ms
// under the given stage label. Use as the final step of each pipeline stage
// so per-stage p50/p95 land in Prometheus alongside the total request timer.
//
// Q5 followup: the stage value is validated against fineStagesSet — labels
// outside the closed set are dropped (no record) so a typo or stray caller
// never opens a free-form label dimension in Prometheus. The pre-registered
// time-series matrix in addMx() always covers the canonical fineStages.
func recordStageDuration(ctx context.Context, stage string, start time.Time) {
	if _, ok := fineStagesSet[stage]; !ok {
		return
	}
	mx := addMx()
	if mx.StageDuration == nil {
		return
	}
	mx.StageDuration.Record(ctx, float64(time.Since(start).Microseconds())/1000.0,
		metric.WithAttributes(attribute.String("stage", stage)))
}
