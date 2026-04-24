// Package search — per-D-feature telemetry (M1).
// OTel Int64Counters + a confidence histogram. Pre-registered at zero so
// Prometheus scrapes see them from first container start.
package search

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	searchMetricsOnce sync.Once
	searchMetrics     *searchMetricsInstruments
)

type searchMetricsInstruments struct {
	D4Rewrite   metric.Int64Counter
	D7CoT       metric.Int64Counter
	D5Staged    metric.Int64Counter
	D5Justified metric.Int64Counter
	D10Enhance  metric.Int64Counter
	D10Conf     metric.Float64Histogram
	Multihop    metric.Int64Counter
}

func searchMx() *searchMetricsInstruments {
	searchMetricsOnce.Do(func() {
		m := otel.Meter("memdb-go/search")
		d4, _ := m.Int64Counter("memdb.search.d4_rewrite",
			metric.WithDescription("D4 query rewrite invocations by outcome (rewritten/skipped/error/low_confidence)"))
		d7, _ := m.Int64Counter("memdb.search.d7_cot",
			metric.WithDescription("D7 CoT decomposition by outcome (decomposed/atomic/skipped/error)"))
		d5, _ := m.Int64Counter("memdb.search.d5_staged",
			metric.WithDescription("D5 staged retrieval stage outcomes (stage in 2_refine/3_justify, outcome in success/fallback/error)"))
		d5j, _ := m.Int64Counter("memdb.search.d5_justified",
			metric.WithDescription("D5 stage-3 per-item relevance (relevant/irrelevant)"))
		d10, _ := m.Int64Counter("memdb.search.d10_enhance",
			metric.WithDescription("D10 answer enhancement outcomes (answered/unknown/error/skipped)"))
		d10c, _ := m.Float64Histogram("memdb.search.d10_confidence",
			metric.WithDescription("D10 LLM self-reported confidence values (0.0 to 1.0)"),
			metric.WithExplicitBucketBoundaries(0.1, 0.3, 0.5, 0.7, 0.9, 1.0))
		mh, _ := m.Int64Counter("memdb.search.multihop",
			metric.WithDescription("D2 multi-hop graph expansion outcomes (expanded/empty_seeds/error/disabled)"))
		searchMetrics = &searchMetricsInstruments{
			D4Rewrite:   d4,
			D7CoT:       d7,
			D5Staged:    d5,
			D5Justified: d5j,
			D10Enhance:  d10,
			D10Conf:     d10c,
			Multihop:    mh,
		}
		// Pre-register at zero (like db/metrics.go pattern) so scrapers see
		// the series before the first real event fires — avoids a
		// "metric not found" gap in dashboards / alert rules.
		ctx := context.Background()
		for _, c := range []metric.Int64Counter{d4, d7, d10, mh} {
			c.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", "")))
		}
		d5.Add(ctx, 0, metric.WithAttributes(
			attribute.String("stage", ""),
			attribute.String("outcome", ""),
		))
		d5j.Add(ctx, 0, metric.WithAttributes(attribute.String("relevance", "")))
	})
	return searchMetrics
}
