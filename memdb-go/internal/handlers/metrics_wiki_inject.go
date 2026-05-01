// Package handlers — wiki-injection metrics (W3 LLM Wiki layer).
//
// Three instruments cover the chat-time wiki injector:
//
//   - memdb.chat.wiki_inject_total{outcome}  — counter, fires once per chat
//     turn that passes the embedder/postgres handle check. outcome ∈
//     {gate_off, no_handles, no_query, embed_error, search_error,
//      no_results, injected}.
//   - memdb.chat.wiki_inject_pages             — histogram of pages returned
//     by SearchWikiByCosine on the success path. buckets: 1, 2, 3, 5, 10.
//   - memdb.chat.wiki_inject_body_chars        — histogram of total chars
//     in the rendered Wiki Synthesis block. buckets: 200, 500, 1000, 2000,
//     4000, 8000.
//
// All outcome labels pre-register at zero so the canary deploy can verify
// the gate (gate_off bumps when MEMDB_WIKI_SEARCH_INJECT=false, injected
// bumps when it flips to true) without waiting for actual traffic on
// every label combination.
package handlers

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	wikiInjectOutcomeGateOff     = "gate_off"
	wikiInjectOutcomeNoHandles   = "no_handles"
	wikiInjectOutcomeNoQuery     = "no_query"
	wikiInjectOutcomeEmbedError  = "embed_error"
	wikiInjectOutcomeSearchError = "search_error"
	wikiInjectOutcomeNoResults   = "no_results"
	wikiInjectOutcomeInjected    = "injected"
)

var wikiInjectOutcomes = []string{
	wikiInjectOutcomeGateOff,
	wikiInjectOutcomeNoHandles,
	wikiInjectOutcomeNoQuery,
	wikiInjectOutcomeEmbedError,
	wikiInjectOutcomeSearchError,
	wikiInjectOutcomeNoResults,
	wikiInjectOutcomeInjected,
}

var (
	wikiInjectMetricsOnce sync.Once
	wikiInjectInstruments *wikiInjectMetricsStruct
)

type wikiInjectMetricsStruct struct {
	Total     metric.Int64Counter     // labels: outcome
	Pages     metric.Float64Histogram // unlabeled, success-only
	BodyChars metric.Float64Histogram // unlabeled, success-only
}

func wikiInjectMx() *wikiInjectMetricsStruct {
	wikiInjectMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/chat")
		total, _ := meter.Int64Counter("memdb.chat.wiki_inject_total",
			metric.WithDescription("Wiki-synthesis inject outcomes per chat turn"),
		)
		pages, _ := meter.Float64Histogram("memdb.chat.wiki_inject_pages",
			metric.WithDescription("Pages returned by SearchWikiByCosine on the success path"),
			metric.WithExplicitBucketBoundaries(1, 2, 3, 5, 10),
		)
		body, _ := meter.Float64Histogram("memdb.chat.wiki_inject_body_chars",
			metric.WithDescription("Char count of the rendered Wiki Synthesis block"),
			metric.WithExplicitBucketBoundaries(200, 500, 1000, 2000, 4000, 8000),
		)
		wikiInjectInstruments = &wikiInjectMetricsStruct{
			Total: total, Pages: pages, BodyChars: body,
		}
		// context.Background(): metric pre-registration inside sync.Once, no request in scope.
		ctx := context.Background()
		for _, oc := range wikiInjectOutcomes {
			total.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
	})
	return wikiInjectInstruments
}

func recordWikiInjectOutcome(ctx context.Context, outcome string) {
	mx := wikiInjectMx()
	if mx.Total == nil {
		return
	}
	mx.Total.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func recordWikiInjectPages(ctx context.Context, n int) {
	mx := wikiInjectMx()
	if mx.Pages == nil {
		return
	}
	mx.Pages.Record(ctx, float64(n))
}

func recordWikiInjectBodyChars(ctx context.Context, chars int) {
	mx := wikiInjectMx()
	if mx.BodyChars == nil {
		return
	}
	mx.BodyChars.Record(ctx, float64(chars))
}
