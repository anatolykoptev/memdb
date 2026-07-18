// Package scheduler — worker domain metrics (Prometheus via OTel).
package scheduler

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	schedMetricsOnce        sync.Once
	schedMetricsInstruments *schedMetricsStruct
)

type schedMetricsStruct struct {
	Messages        metric.Int64Counter     // labels: label, outcome
	Duration        metric.Float64Histogram // labels: label
	DLQ             metric.Int64Counter     // labels: label
	TreeReorg       metric.Int64Counter     // labels: tier, outcome
	PageRankRuns           metric.Int64Counter     // labels: outcome (success|empty|db_error|compute_error|skipped_other_leader)
	PageRankLastRun        metric.Float64Gauge     // seconds since epoch of last completed run
	PageRankDistinctScores metric.Int64Gauge       // distinct PageRank score values written per cycle (distribution health)
	// D3ClusterSize (Q5) — number of members in each cluster at promotion time.
	// tier ∈ {episodic, semantic}.
	D3ClusterSize metric.Int64Histogram
	// F14 Personalized PageRank metrics.
	PPRRuns       metric.Int64Counter   // outcome: success|empty_seed|cache_hit|fallback_global
	PPRCacheHits  metric.Int64Counter   // numerator for cache-hit ratio
	PPRCacheTotal metric.Int64Counter   // denominator for cache-hit ratio
	PPRIterCount  metric.Int64Histogram // convergence speed (iterations per PPR call)
	// PPRDurationMs records wall-clock latency of ComputePersonalizedPR end-to-end
	// (cache lookup → optional recompute → cache store). Label: outcome.
	PPRDurationMs metric.Float64Histogram
	// ReorgErrors counts non-fatal errors swallowed during reorganizer work.
	// Labels: operation, severity (nonfatal|retryable|fatal).
	ReorgErrors metric.Int64Counter
}

// labelPageRankOutcome returns an OTel attribute option for pagerank outcome labels.
func labelPageRankOutcome(outcome string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("outcome", outcome))
}

// labelPPROutcome returns an OTel attribute option for PPR outcome labels.
func labelPPROutcome(outcome string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("outcome", outcome))
}

// schedMx returns the singleton scheduler instruments, lazy-initialised.
func schedMx() *schedMetricsStruct {
	schedMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/scheduler")
		msgs, _ := meter.Int64Counter("memdb.scheduler.messages_total",
			metric.WithDescription("Total scheduler messages processed by label and outcome"),
		)
		dur, _ := meter.Float64Histogram("memdb.scheduler.duration_ms",
			metric.WithDescription("Scheduler message processing duration in milliseconds"),
			metric.WithUnit("ms"),
		)
		dlq, _ := meter.Int64Counter("memdb.scheduler.dlq_total",
			metric.WithDescription("Total messages moved to Dead Letter Queue by label"),
		)
		tr, _ := meter.Int64Counter("memdb.scheduler.tree_reorg",
			metric.WithDescription("D3 tree reorganizer outcomes (tier in episodic/semantic/all/relation, outcome in created/skipped_below_threshold/error/edge_write_error/hierarchy_write_error/audit_write_error/relation_attempted/relation_written_<RELATION>/relation_skipped/relation_error)"),
		)
		prRuns, _ := meter.Int64Counter("memdb.scheduler.pagerank_runs_total",
			metric.WithDescription("PageRank background task runs by outcome (success|empty|db_error|compute_error|skipped_other_leader)"),
		)
		prLast, _ := meter.Float64Gauge("memdb.scheduler.pagerank_last_run_seconds",
			metric.WithDescription("Duration in seconds of the last PageRank computation cycle"),
			metric.WithUnit("s"),
		)
		prDistinct, _ := meter.Int64Gauge("memdb.pagerank_distribution_distinct_total",
			metric.WithDescription("Number of distinct PageRank scores written in the last compute cycle (healthy = O(nodes); collapse = O(1))"),
		)
		d3cs, _ := meter.Int64Histogram("memdb.scheduler.d3_cluster_size",
			metric.WithDescription("D3 reorganizer: member count of each cluster at promotion (tier=episodic|semantic)"),
			metric.WithExplicitBucketBoundaries(2, 3, 5, 10, 20, 50),
		)
		pprRuns, _ := meter.Int64Counter("memdb.scheduler.ppr_compute_total",
			metric.WithDescription("Personalized PageRank compute outcomes (outcome in success|empty_seed|cache_hit|fallback_global)"),
		)
		pprCacheHits, _ := meter.Int64Counter("memdb.scheduler.ppr_cache_hits_total",
			metric.WithDescription("Personalized PageRank Redis cache hits (numerator for hit ratio)"),
		)
		pprCacheTotal, _ := meter.Int64Counter("memdb.scheduler.ppr_cache_requests_total",
			metric.WithDescription("Personalized PageRank Redis cache requests (denominator for hit ratio)"),
		)
		pprIterCount, _ := meter.Int64Histogram("memdb.scheduler.ppr_iter_count",
			metric.WithDescription("Personalized PageRank power-iteration convergence count per call"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 20, 30, 40, 50),
		)
		pprDuration, _ := meter.Float64Histogram("memdb.scheduler.personalized_pr_duration_ms",
			metric.WithDescription("Wall-clock latency of ComputePersonalizedPR end-to-end (ms), labelled by outcome"),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500),
		)
		reorgErrs, _ := meter.Int64Counter("memdb.scheduler.reorg_errors_total",
			metric.WithDescription("Non-fatal errors swallowed during reorganizer work (operation, severity)"),
		)
		schedMetricsInstruments = &schedMetricsStruct{
			Messages:               msgs,
			Duration:               dur,
			DLQ:                    dlq,
			TreeReorg:              tr,
			PageRankRuns:           prRuns,
			PageRankLastRun:        prLast,
			PageRankDistinctScores: prDistinct,
			D3ClusterSize:          d3cs,
			PPRRuns:                pprRuns,
			PPRCacheHits:           pprCacheHits,
			PPRCacheTotal:          pprCacheTotal,
			PPRIterCount:           pprIterCount,
			PPRDurationMs:          pprDuration,
			ReorgErrors:            reorgErrs,
		}
		// Pre-register TreeReorg at zero so Prometheus scrapers see the
		// series immediately (matches db/metrics.go pattern).
		tr.Add(context.Background(), 0, metric.WithAttributes(
			attribute.String("tier", ""),
			attribute.String("outcome", ""),
		))
		// Pre-register PageRank counters at zero so Prometheus scrapers see
		// every outcome series before the first tick (including the HA skip label).
		for _, outcome := range []string{"success", "empty", "db_error", "compute_error", "skipped_other_leader"} {
			prRuns.Add(context.Background(), 0, metric.WithAttributes(
				attribute.String("outcome", outcome),
			))
		}
		// Pre-register D3 cluster size for both tiers.
		for _, tier := range []string{"episodic", "semantic"} {
			d3cs.Record(context.Background(), 0, metric.WithAttributes(
				attribute.String("tier", tier),
			))
		}
		// Pre-register PPR outcome counters so Prometheus sees all series from start.
		for _, outcome := range []string{"success", "empty_seed", "cache_hit", "fallback_global"} {
			pprRuns.Add(context.Background(), 0, metric.WithAttributes(
				attribute.String("outcome", outcome),
			))
			pprDuration.Record(context.Background(), 0, metric.WithAttributes(
				attribute.String("outcome", outcome),
			))
		}
	})
	return schedMetricsInstruments
}
