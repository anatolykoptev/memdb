// Package handlers — wiki retrieval-slot metrics (W3.5 LLM Wiki layer).
//
// Slot path is independent from the prefix path (metrics_wiki_inject.go);
// dashboards may run them in A/B and need separate counters to attribute
// F1 lift correctly.
//
// Two instruments:
//
//   - memdb.chat.wiki_slot_total{outcome}  — counter, fires once per chat
//     turn that passes the gate. outcome ∈ {gate_off, no_handles, no_query,
//     embed_error, search_error, no_results, below_min_score, merged}.
//   - memdb.chat.wiki_slot_pages_merged    — histogram of pages that passed
//     the min-score gate and landed in the memory list. buckets: 1, 2, 3,
//     5, 10.
//
// All outcome labels pre-register at zero so canary deploys see "gate_off"
// vs "merged" immediately on the dashboard.
package handlers

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	wikiSlotOutcomeGateOff       = "gate_off"
	wikiSlotOutcomeNoHandles     = "no_handles"
	wikiSlotOutcomeNoQuery       = "no_query"
	wikiSlotOutcomeEmbedError    = "embed_error"
	wikiSlotOutcomeSearchError   = "search_error"
	wikiSlotOutcomeNoResults     = "no_results"
	wikiSlotOutcomeBelowMinScore = "below_min_score"
	wikiSlotOutcomeMerged        = "merged"
	// wikiSlotOutcomeOptInSkipped fires when the chat caller did NOT set
	// IncludeWiki=true on the request. This is the new default after the
	// Karpathy r2 forensic (2026-05-01) flipped the slot from always-on to
	// opt-in. Counter visibility lets dashboards confirm the flip rolled out
	// and quantify how many turns would have triggered the slot historically.
	wikiSlotOutcomeOptInSkipped = "opt_in_skipped"
)

var wikiSlotOutcomes = []string{
	wikiSlotOutcomeGateOff,
	wikiSlotOutcomeNoHandles,
	wikiSlotOutcomeNoQuery,
	wikiSlotOutcomeEmbedError,
	wikiSlotOutcomeSearchError,
	wikiSlotOutcomeNoResults,
	wikiSlotOutcomeBelowMinScore,
	wikiSlotOutcomeMerged,
	wikiSlotOutcomeOptInSkipped,
}

var (
	wikiSlotMetricsOnce sync.Once
	wikiSlotInstruments *wikiSlotMetricsStruct
)

type wikiSlotMetricsStruct struct {
	Total        metric.Int64Counter     // labels: outcome
	PagesMerged  metric.Float64Histogram // unlabeled, success-only
}

func wikiSlotMx() *wikiSlotMetricsStruct {
	wikiSlotMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/chat")
		total, _ := meter.Int64Counter("memdb.chat.wiki_slot_total",
			metric.WithDescription("Wiki retrieval-slot outcomes per chat turn"),
		)
		merged, _ := meter.Float64Histogram("memdb.chat.wiki_slot_pages_merged",
			metric.WithDescription("Wiki pages merged into memory list (above min-score)"),
			metric.WithExplicitBucketBoundaries(1, 2, 3, 5, 10),
		)
		wikiSlotInstruments = &wikiSlotMetricsStruct{
			Total: total, PagesMerged: merged,
		}
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, oc := range wikiSlotOutcomes {
			total.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
	})
	return wikiSlotInstruments
}

func recordWikiSlotOutcome(ctx context.Context, outcome string) {
	mx := wikiSlotMx()
	if mx.Total == nil {
		return
	}
	mx.Total.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func recordWikiSlotPagesMerged(ctx context.Context, n int) {
	mx := wikiSlotMx()
	if mx.PagesMerged == nil {
		return
	}
	mx.PagesMerged.Record(ctx, float64(n))
}
