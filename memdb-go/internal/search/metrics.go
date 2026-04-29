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
	// HTTP path executed (regardless of why it was reached).
	CELiveCall metric.Int64Counter
	// D1BoostMagnitude (Q5) — combined D1 boost before the > 1.0 cap.
	// formula label is always "combined" (single code path); kept as label
	// so future formulas can be compared without schema change.
	D1BoostMagnitude metric.Float64Histogram
	// D5StageInputSize (Q5) — candidate count entering each staged-retrieval stage.
	D5StageInputSize metric.Int64Histogram
	// RecallBudget (F9) — counter fired once per search request with labels
	// category (cat2|other) and top_k (text budget).  Used for A/B comparison
	// of cat-2 threshold lowering on LoCoMo full-corpus runs.
	RecallBudget metric.Int64Counter
	// LinkedExpand (F12) — counter for stageLinkedExpand outcomes
	// (expanded|empty_seeds|no_neighbors|error|disabled).
	LinkedExpand metric.Int64Counter
	// LinkedExpandCount (F12) — histogram of linked-by neighbours injected
	// per query (excluding seeds). Mirrors HopsPerQuery's shape.
	LinkedExpandCount metric.Int64Histogram
	// RewriteCacheHit / RewriteCacheMiss — cache hit/miss counters for
	// D4 query-rewrite and D7 CoT-decompose LLM calls.
	// Label: stage = "d4" | "d7".
	RewriteCacheHit  metric.Int64Counter
	RewriteCacheMiss metric.Int64Counter
	// RRFEngaged counts calls that went through the go-kit/rerank.RRF path
	// (MEMDB_USE_GOKIT_RRF=1). Zero when env is unset (legacy path active).
	RRFEngaged metric.Int64Counter
	// RRFKGauge records the k constant used each time go-kit RRF fires.
	// Lets operators confirm MEMDB_RRF_K override is picked up.
	RRFKGauge metric.Float64Histogram
	// CountingBoost (Task #100) — fires once per search request that triggered
	// the counting-aware top-K boost (outcome=boosted) or was classified as a
	// counting query but already had TopK ≥ countingTopK (outcome=skipped).
	// Disabled queries (MEMDB_COUNTING_TOPK=0) also land in "skipped".
	CountingBoost metric.Int64Counter
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
			metric.WithDescription("M10 Stream 6 + M14 Y2: CE precompute lookup outcome (outcome in hit|partial|miss|stale)"))
		celive, _ := m.Int64Counter("memdb.search.ce_live_call_total",
			metric.WithDescription("M10 Stream 6: live cross-encoder HTTP rerank invocations"))
		d1boost, _ := m.Float64Histogram("memdb.search.d1_boost_magnitude",
			metric.WithDescription("D1 combined boost value before the >1.0 cap (formula=combined)"),
			metric.WithExplicitBucketBoundaries(0.5, 1.0, 1.5, 2.0, 3.0, 5.0))
		d5size, _ := m.Int64Histogram("memdb.search.d5_stage_input_size",
			metric.WithDescription("Candidate count at each D5 staged-retrieval stage entry (stage=0_cosine_prefilter|2_refine|3_justify)"),
			metric.WithExplicitBucketBoundaries(0, 5, 10, 20, 50, 100, 200))
		rb, _ := m.Int64Counter("memdb.search.recall_budget_total",
			metric.WithDescription("F9 recall budget: search requests by category heuristic (category=cat2|other) and text top_k"))
		lex, _ := m.Int64Counter("memdb.search.linked_expand_total",
			metric.WithDescription("F12 linked_memory_ids 1-hop expansion outcomes (expanded|empty_seeds|no_neighbors|error|disabled)"))
		lexc, _ := m.Int64Histogram("memdb.search.linked_expand_count",
			metric.WithDescription("F12 linked_memory_ids: linked-by neighbours injected per query (excluding seeds)"),
			metric.WithExplicitBucketBoundaries(0, 1, 3, 5, 10, 25, 50))
		rwHit, _ := m.Int64Counter("memdb.search.rewrite_cache_hit_total",
			metric.WithDescription("D4/D7 rewrite LRU cache hits by stage (stage=d4|d7)"))
		rwMiss, _ := m.Int64Counter("memdb.search.rewrite_cache_miss_total",
			metric.WithDescription("D4/D7 rewrite LRU cache misses by stage (stage=d4|d7)"))
		rrfEng, _ := m.Int64Counter("memdb.search.rrf_engaged_total",
			metric.WithDescription("go-kit/rerank.RRF fusion calls (MEMDB_USE_GOKIT_RRF=1 active); 0 when legacy path is used"))
		rrfK, _ := m.Float64Histogram("memdb.search.rrf_k",
			metric.WithDescription("RRF k constant used per go-kit RRF fusion call (set via MEMDB_RRF_K, default 60)"),
			metric.WithExplicitBucketBoundaries(10, 20, 30, 60, 100, 200))
		cntBoost, _ := m.Int64Counter("memdb.search.counting_boost_total",
			metric.WithDescription("Task #100: counting-aware top-K boost outcome per search (outcome=boosted|skipped)"))
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
			D1BoostMagnitude: d1boost,
			D5StageInputSize: d5size,
			RecallBudget:     rb,
			LinkedExpand:     lex,
			LinkedExpandCount: lexc,
			RewriteCacheHit:  rwHit,
			RewriteCacheMiss: rwMiss,
			RRFEngaged:       rrfEng,
			RRFKGauge:        rrfK,
			CountingBoost:    cntBoost,
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
		// Pre-register CE precompute outcomes so dashboards see all four
		// series from container start, even before any search has fired.
		// "partial" added in M14 Y2: some items hit cache, rest go live.
		for _, oc := range []string{"hit", "partial", "miss", "stale"} {
			ceph.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		celive.Add(ctx, 0)
		// Pre-register D1 boost magnitude so the formula=combined bucket-set is
		// visible from the first Prometheus scrape.
		d1boost.Record(ctx, 0, metric.WithAttributes(attribute.String("formula", "combined")))
		// Pre-register D5 stage input size for all three stage labels.
		// Note: 0_cosine_prefilter (was 1_cosine) reflects the cosine
		// pre-filter feeding D5, not a stage of D5 itself — Q5 followup.
		for _, stage := range []string{"0_cosine_prefilter", "2_refine", "3_justify"} {
			d5size.Record(ctx, 0, metric.WithAttributes(attribute.String("stage", stage)))
		}
		// Pre-register F9 recall budget counter for both category labels
		// across the bounded top_k bucket set so dashboards see every
		// series from container start.  Cardinality stays at
		// 2 × len(recallBudgetTopKBuckets) — bounded.
		for _, cat := range []string{"cat2", "other"} {
			for _, k := range recallBudgetTopKBuckets {
				rb.Add(ctx, 0, metric.WithAttributes(
					attribute.String("category", cat),
					attribute.Int64("top_k", int64(k)),
				))
			}
		}
		// Pre-register F12 linked_expand outcomes for all label values.
		for _, oc := range []string{"expanded", "empty_seeds", "no_neighbors", "error", "disabled"} {
			lex.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		lexc.Record(ctx, 0)
		// Pre-register rewrite cache counters for both stages so dashboards
		// see the series from container start, before any search fires.
		for _, stage := range []string{"d4", "d7"} {
			rwHit.Add(ctx, 0, metric.WithAttributes(attribute.String("stage", stage)))
			rwMiss.Add(ctx, 0, metric.WithAttributes(attribute.String("stage", stage)))
		}
		// Pre-register go-kit RRF counters at zero so dashboards see the
		// series even when MEMDB_USE_GOKIT_RRF is not yet enabled.
		rrfEng.Add(ctx, 0)
		rrfK.Record(ctx, 0)
		// Pre-register Task #100 counting boost outcomes so dashboards see both
		// series from container start, before the first counting query arrives.
		for _, oc := range []string{"boosted", "skipped"} {
			cntBoost.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
	})
	return searchMetrics
}

// recallBudgetTopKBuckets is the bounded set of top_k label values used by
// the RecallBudget counter.  Any incoming top_k is rounded UP to the closest
// bucket (with overflow clamped to the highest), keeping Prometheus label
// cardinality at exactly len(recallBudgetTopKBuckets) × 2 (cat2|other).
//
// Buckets cover the realistic top_k range used by current call-sites
// (text/skill/tool budgets) plus a small headroom.  Add a bucket here
// (sorted ASC) when a new tier appears — never push the raw int as a label.
var recallBudgetTopKBuckets = []int{1, 3, 5, 10, 20, 30, 50, 100}

// bucketTopK rounds an arbitrary top_k value UP to the closest entry in
// recallBudgetTopKBuckets so the metric label set stays bounded.  Out-of-range
// values (≤ 0) collapse to the smallest bucket; values above the largest
// bucket are clamped to it.
func bucketTopK(topK int) int {
	if topK <= 0 {
		return recallBudgetTopKBuckets[0]
	}
	for _, b := range recallBudgetTopKBuckets {
		if topK <= b {
			return b
		}
	}
	return recallBudgetTopKBuckets[len(recallBudgetTopKBuckets)-1]
}

// recallBudgetAttrs builds the OTel attribute set for the RecallBudget counter.
// `topK` is bucketed via bucketTopK so the label cardinality is bounded — see
// recallBudgetTopKBuckets for the canonical set.
func recallBudgetAttrs(category string, topK int) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("category", category),
		attribute.Int64("top_k", int64(bucketTopK(topK))),
	)
}
