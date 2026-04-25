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
	D4Rewrite        metric.Int64Counter
	D7CoT            metric.Int64Counter
	D5Staged         metric.Int64Counter
	D5Justified      metric.Int64Counter
	D10Enhance       metric.Int64Counter
	D10Conf          metric.Float64Histogram
	Multihop         metric.Int64Counter
	HopsPerQuery     metric.Int64Histogram // M8: max hop reached per D2 expansion call
	D11CoTDecompose  metric.Int64Counter
	D11CoTSubqueries metric.Int64Histogram
	D11CoTDuration   metric.Int64Histogram
	D11CoTCacheHit   metric.Int64Counter
	// LevelTotal counts requests per memory-tier scope (l1/l2/l3/all).
	LevelTotal metric.Int64Counter
	// CEPrecomputeHit (M10 Stream 6) — outcome of CE precompute lookup
	// (hit | miss | stale).
	CEPrecomputeHit metric.Int64Counter
	// CELiveCall (M10 Stream 6) — fired every time the live cross-encoder
	// HTTP path executed (regardless of whether it was reached after a
	// lookup miss or because the precompute feature was disabled).
	CELiveCall metric.Int64Counter
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
		// HopsPerQuery (M8): max hop reached per call to expandViaGraph. Lets
		// ops tell at a glance whether D2 is actually walking past hop-1
		// (signal that the graph topology is dense enough for multi-hop to
		// matter). Buckets cover the full [0, MEMDB_D2_MAX_HOP] range up to 5.
		hpq, _ := m.Int64Histogram("memdb.search.d2_hops_per_query",
			metric.WithDescription("D2 multi-hop expansion: max hop reached per query (0 = no neighbors, n = walked n hops)"),
			metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 4, 5))
		d11, _ := m.Int64Counter("memdb.search.cot.decomposed_total",
			metric.WithDescription("D11 CoT decomposer invocations by outcome (success/skip/error)"))
		d11n, _ := m.Int64Histogram("memdb.search.cot.subqueries",
			metric.WithDescription("D11 number of sub-queries returned (including original at index 0)"),
			metric.WithExplicitBucketBoundaries(1, 2, 3, 4, 5))
		d11d, _ := m.Int64Histogram("memdb.search.cot.duration_ms",
			metric.WithDescription("D11 CoT decomposer LLM call duration in milliseconds"),
			metric.WithExplicitBucketBoundaries(100, 250, 500, 1000, 2000, 5000, 10000))
		d11c, _ := m.Int64Counter("memdb.search.cot.cache_hit_total",
			metric.WithDescription("D11 CoT decomposer cache hits"))
		lvl, _ := m.Int64Counter("memdb.search.level_total",
			metric.WithDescription("Search requests by memory tier (level=l1|l2|l3|all)"))
		ceph, _ := m.Int64Counter("memdb.search.ce_precompute_hit_total",
			metric.WithDescription("M10 Stream 6: CE precompute lookup outcome (outcome in hit|miss|stale)"))
		celive, _ := m.Int64Counter("memdb.search.ce_live_call_total",
			metric.WithDescription("M10 Stream 6: live cross-encoder HTTP rerank invocations (after precompute miss or when feature disabled)"))
		searchMetrics = &searchMetricsInstruments{
			D4Rewrite:        d4,
			D7CoT:            d7,
			D5Staged:         d5,
			D5Justified:      d5j,
			D10Enhance:       d10,
			D10Conf:          d10c,
			Multihop:         mh,
			HopsPerQuery:     hpq,
			D11CoTDecompose:  d11,
			D11CoTSubqueries: d11n,
			D11CoTDuration:   d11d,
			D11CoTCacheHit:   d11c,
			LevelTotal:       lvl,
			CEPrecomputeHit:  ceph,
			CELiveCall:       celive,
		}
		// Pre-register at zero (like db/metrics.go pattern) so scrapers see
		// the series before the first real event fires — avoids a
		// "metric not found" gap in dashboards / alert rules.
		ctx := context.Background()
		for _, c := range []metric.Int64Counter{d4, d7, d10, mh, d11} {
			c.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", "")))
		}
		d5.Add(ctx, 0, metric.WithAttributes(
			attribute.String("stage", ""),
			attribute.String("outcome", ""),
		))
		d5j.Add(ctx, 0, metric.WithAttributes(attribute.String("relevance", "")))
		d11c.Add(ctx, 0)
		for _, lv := range []string{"l1", "l2", "l3", "all"} {
			lvl.Add(ctx, 0, metric.WithAttributes(attribute.String("level", lv)))
		}
		// Pre-register CE precompute outcomes so dashboards see all three
		// series from container start, even before any search has fired.
		for _, oc := range []string{"hit", "miss", "stale"} {
			ceph.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		celive.Add(ctx, 0)
	})
	return searchMetrics
}
