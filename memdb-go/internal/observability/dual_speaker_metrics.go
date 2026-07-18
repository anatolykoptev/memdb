// Package observability — M9 dual-speaker retrieval instruments.
//
// Server-side dual-speaker fan-out lets a single /product/search or
// /product/chat call retrieve memories from N speakers' personal memory
// stores in parallel and merge them into one labelled result list. Until
// today the only consumer was the eval harness (evaluation/locomo/query.py),
// which built the fan-out client-side. These counters surface the same
// behaviour on the server so prod consumers (vaelor etc.) can opt in via
// the JSON `speakers` field and operators can see when it fires.
//
// Metric names follow the m12_metrics.go dotted-OTel convention; they
// surface as `memdb_search_dual_speaker_*` in Prometheus.
package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// dualSpeakerCountBuckets — speaker-count histogram. 1 = legacy single-speaker
// path (logically "dual_speaker_engaged=disabled"); 2 = the LoCoMo case;
// boundaries up to 8 cover plausible group-chat fan-outs without forcing the
// SDK to allocate a long bucket array.
var dualSpeakerCountBuckets = []float64{1, 2, 3, 4, 5, 8}

// dualSpeakerLatencyBuckets — fan-out elapsed-ms (parallel branch) buckets.
// p99 of the harness path is ~600ms; we extend up to 5s for cold-cache.
var dualSpeakerLatencyBuckets = []float64{10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// dualSpeakerEngagedOutcomes pre-registers both label values so the series
// exists from first scrape — operators can alert on the absence of the
// "enabled" path even before any caller opts in.
var dualSpeakerEngagedOutcomes = []string{"enabled", "disabled"}

// dualSpeakerSurfaces are the request paths that can engage the fan-out.
// Pre-registered as a label so dashboards can split search vs chat.
var dualSpeakerSurfaces = []string{"search", "chat"}

// dualSpeakerInstruments holds the M9 server-side dual-speaker counters.
type dualSpeakerInstruments struct {
	Engaged         metric.Int64Counter
	SpeakersFanned  metric.Float64Histogram
	MergedTotal     metric.Int64Counter
	FanOutLatencyMs metric.Float64Histogram
}

var (
	dualSpeakerOnce sync.Once
	dualSpeakerInst *dualSpeakerInstruments
)

// DualSpeaker returns the singleton M9 dual-speaker instruments.
func DualSpeaker() *dualSpeakerInstruments {
	dualSpeakerOnce.Do(func() {
		meter := otel.Meter("memdb-go/m9-dual-speaker")
		dualSpeakerInst = &dualSpeakerInstruments{}

		dualSpeakerInst.Engaged, _ = meter.Int64Counter(
			"memdb.search.dual_speaker_engaged_total",
			metric.WithDescription("Counts requests that hit the dual-speaker fan-out branch. outcome=enabled when len(speakers)>=2; outcome=disabled when speakers is empty/single — the legacy path. surface in {search,chat}."),
		)
		dualSpeakerInst.SpeakersFanned, _ = meter.Float64Histogram(
			"memdb.search.dual_speaker_count",
			metric.WithDescription("Number of speakers in a dual-speaker fan-out call (==1 means the disabled branch). surface in {search,chat}."),
			metric.WithExplicitBucketBoundaries(dualSpeakerCountBuckets...),
		)
		dualSpeakerInst.MergedTotal, _ = meter.Int64Counter(
			"memdb.search.dual_speaker_merged_total",
			metric.WithDescription("Sum of memories returned across all per-speaker buckets BEFORE the final TopK cap. Lets operators see post-merge dedup pressure (input - output = duplicates removed). surface in {search,chat}."),
		)
		dualSpeakerInst.FanOutLatencyMs, _ = meter.Float64Histogram(
			"memdb.search.dual_speaker_fanout_ms",
			metric.WithDescription("Wall-clock elapsed-ms for the parallel fan-out (max(per-speaker latency) + merge). Recorded only on the engaged branch. surface in {search,chat}."),
			metric.WithExplicitBucketBoundaries(dualSpeakerLatencyBuckets...),
		)

		// Pre-register canonical labels at zero.
		ctx := context.Background()
		for _, oc := range dualSpeakerEngagedOutcomes {
			for _, sf := range dualSpeakerSurfaces {
				dualSpeakerInst.Engaged.Add(ctx, 0,
					metric.WithAttributes(
						attribute.String("outcome", oc),
						attribute.String("surface", sf),
					),
				)
				dualSpeakerInst.MergedTotal.Add(ctx, 0,
					metric.WithAttributes(attribute.String("surface", sf)))
			}
		}
		for _, sf := range dualSpeakerSurfaces {
			dualSpeakerInst.SpeakersFanned.Record(ctx, 0,
				metric.WithAttributes(attribute.String("surface", sf)))
			dualSpeakerInst.FanOutLatencyMs.Record(ctx, 0,
				metric.WithAttributes(attribute.String("surface", sf)))
		}
	})
	return dualSpeakerInst
}

// RecordDualSpeakerEngaged bumps the engaged/disabled counter and records
// the speaker count. Call once per request from the search / chat handler
// (after validation, before fan-out). speakerCount == 0 when the request
// has no Speakers field — counted as "disabled" with count=1 for histogram
// to keep the legacy path visible.
func RecordDualSpeakerEngaged(ctx context.Context, surface string, speakerCount int) {
	outcome := "disabled"
	histCount := 1
	if speakerCount >= 2 {
		outcome = "enabled"
		histCount = speakerCount
	}
	mx := DualSpeaker()
	mx.Engaged.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("surface", surface),
		),
	)
	mx.SpeakersFanned.Record(ctx, float64(histCount),
		metric.WithAttributes(attribute.String("surface", surface)))
}

// RecordDualSpeakerMerged records the post-fan-out merged size BEFORE the
// final TopK cap. Caller passes the sum of len(per-speaker memories).
func RecordDualSpeakerMerged(ctx context.Context, surface string, mergedSize int) {
	if mergedSize <= 0 {
		return
	}
	DualSpeaker().MergedTotal.Add(ctx, int64(mergedSize),
		metric.WithAttributes(attribute.String("surface", surface)))
}

// RecordDualSpeakerLatency records the parallel fan-out wall-clock latency
// (ms). Caller measures from goroutine spawn until merge end.
func RecordDualSpeakerLatency(ctx context.Context, surface string, ms float64) {
	DualSpeaker().FanOutLatencyMs.Record(ctx, ms,
		metric.WithAttributes(attribute.String("surface", surface)))
}
