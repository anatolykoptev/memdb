package search

// rerank_gate.go — adaptive LLM rerank strategy based on result spread.
//
// Instead of a binary should/shouldn't, this returns a RerankDecision
// with the reason and a TopK cap for cost control.
//
// M12.7 — REVIVE LLM Judge sprint:
//   - Thresholds are now env-overridable. Real-world relativity (post
//     temporal-decay multiplier) tops out around 0.6–0.85 — the legacy
//     0.93 high-confidence cut-off practically meant "never trigger".
//   - New defaults (0.85 / 0.10 / 0.30) make the gate adaptive to the
//     observed score band. Operators can tune per-environment via env.
//   - Each gate decision emits memdb.search.rerank_gate_decision_total
//     and the underlying relativity {top, spread} land in histograms,
//     so we can see why the gate said skip without tailing logs.

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

// Default thresholds. Override via env (envcfg.Float / envcfg.Int).
// Names are stable so dashboards / alerts can reference them.
const (
	defaultRerankTopCosineThreshold = 0.85 // skip if top result is already high-confidence
	defaultRerankClusteredSpread    = 0.10 // spread below this → clustered, rerank all
	defaultRerankWideSpread         = 0.30 // spread above this → clear separation, skip
	defaultRerankTopKCap            = 8    // cap for medium-spread reranking
	rerankMinResults                = 4    // skip if fewer than this many results
)

// rerankGateThresholds resolves the four tunables from env (or falls back
// to the defaults above). Resolved per-call so operators can flip a value
// without restarting the binary — the cost is four os.Getenv calls per
// search, which is negligible compared to the LLM round-trip the gate
// guards.
type rerankGateThresholds struct {
	TopCosine       float64 // MEMDB_RERANK_TOP_COSINE_THRESHOLD
	ClusteredSpread float64 // MEMDB_RERANK_CLUSTERED_SPREAD
	WideSpread      float64 // MEMDB_RERANK_WIDE_SPREAD
	TopKCap         int     // MEMDB_RERANK_TOPK_CAP
}

func loadRerankGateThresholds() rerankGateThresholds {
	return rerankGateThresholds{
		TopCosine:       envcfg.Float("MEMDB_RERANK_TOP_COSINE_THRESHOLD", defaultRerankTopCosineThreshold),
		ClusteredSpread: envcfg.Float("MEMDB_RERANK_CLUSTERED_SPREAD", defaultRerankClusteredSpread),
		WideSpread:      envcfg.Float("MEMDB_RERANK_WIDE_SPREAD", defaultRerankWideSpread),
		TopKCap:         envcfg.Int("MEMDB_RERANK_TOPK_CAP", defaultRerankTopKCap),
	}
}

// RerankDecision holds the adaptive rerank strategy.
type RerankDecision struct {
	ShouldRerank bool   // whether to invoke LLM rerank
	Reason       string // "too-few" | "high-confidence" | "clustered" | "medium-spread" | "wide-spread"
	TopK         int    // how many items to send to LLM reranker (0 = all)
}

// rerankStrategy analyzes result scores and returns a rerank decision.
// Uses the spread between top and bottom relativity scores to determine strategy.
//
// Side effect: emits gate-decision and relativity {top, spread} metrics so
// operators can observe gate behaviour without logs. Histogram readings
// are recorded only when len(items) >= rerankMinResults — too-few branches
// don't have a meaningful spread to report.
//
// ctx is threaded into all metric record calls so trace/slog hooks can
// correlate gate decisions with the originating request span.
func rerankStrategy(ctx context.Context, items []map[string]any) RerankDecision {
	t := loadRerankGateThresholds()

	if len(items) < rerankMinResults {
		recordGateDecision(ctx, "too-few")
		return RerankDecision{ShouldRerank: false, Reason: "too-few"}
	}

	topRel, botRel := extractRelativityRange(items)
	spread := topRel - botRel

	// Histograms are emitted for every gate call with >=4 items so the
	// distribution shows even when the gate skips — this is what makes
	// the metric useful for tuning the thresholds against real traffic.
	recordRelativityTop(ctx, topRel)
	recordRelativitySpread(ctx, spread)

	// High-confidence top result — cosine ordering is sufficient.
	if topRel > t.TopCosine {
		recordGateDecision(ctx, "high-confidence")
		return RerankDecision{ShouldRerank: false, Reason: "high-confidence"}
	}

	// Clustered: all scores are close together — LLM judgment needed for all.
	if spread < t.ClusteredSpread {
		recordGateDecision(ctx, "clustered")
		return RerankDecision{ShouldRerank: true, Reason: "clustered", TopK: 0}
	}

	// Wide spread: clear separation — cosine ordering is reliable.
	if spread > t.WideSpread {
		recordGateDecision(ctx, "wide-spread")
		return RerankDecision{ShouldRerank: false, Reason: "wide-spread"}
	}

	// Medium spread: ambiguous — rerank but cap items for cost control.
	recordGateDecision(ctx, "medium-spread")
	return RerankDecision{ShouldRerank: true, Reason: "medium-spread", TopK: t.TopKCap}
}

// shouldLLMRerank is the backward-compatible wrapper for callers that
// only need a bool.
func shouldLLMRerank(ctx context.Context, items []map[string]any) bool {
	return rerankStrategy(ctx, items).ShouldRerank
}

// extractRelativityRange returns the top and bottom relativity scores from items.
// Returns (0, 0) if metadata is missing.
func extractRelativityRange(items []map[string]any) (top, bottom float64) {
	if len(items) == 0 {
		return 0, 0
	}
	top = extractRelativity(items[0])
	bottom = extractRelativity(items[len(items)-1])
	return top, bottom
}

// extractRelativity extracts the relativity score from an item's metadata.
func extractRelativity(item map[string]any) float64 {
	meta, ok := item["metadata"].(map[string]any)
	if !ok {
		return 0
	}
	rel, ok := meta["relativity"].(float64)
	if !ok {
		return 0
	}
	return rel
}

// --- Observability ---

var (
	gateDecisionCounter  metric.Int64Counter
	relativityTopHist    metric.Float64Histogram
	relativitySpreadHist metric.Float64Histogram
)

// gateDecisionReasons is the closed set of `reason` labels emitted by
// recordGateDecision. Pre-registered at zero so dashboards see all
// series from container start (no "unknown label" gaps until the first
// time a branch runs in production).
var gateDecisionReasons = []string{
	"too-few",
	"high-confidence",
	"clustered",
	"medium-spread",
	"wide-spread",
}

func init() {
	m := otel.Meter("memdb-go/search")
	gateDecisionCounter, _ = m.Int64Counter("memdb.search.rerank_gate_decision_total",
		metric.WithDescription("rerank gate decision per search call (reason in too-few|high-confidence|clustered|medium-spread|wide-spread)"))
	relativityTopHist, _ = m.Float64Histogram("memdb.search.rerank_relativity_top",
		metric.WithDescription("Top relativity score observed at the rerank gate (post cosine+CE)"),
		metric.WithExplicitBucketBoundaries(0.0, 0.3, 0.5, 0.7, 0.85, 0.9, 1.0))
	relativitySpreadHist, _ = m.Float64Histogram("memdb.search.rerank_relativity_spread",
		metric.WithDescription("Top–bottom relativity spread observed at the rerank gate"),
		metric.WithExplicitBucketBoundaries(0.0, 0.05, 0.10, 0.15, 0.20, 0.30, 0.50))

	// Pre-register every reason at zero so the metric exposes the full
	// label set from container boot — operators querying
	// rerank_gate_decision_total{reason="wide-spread"} won't get an
	// empty result while waiting for the first wide-spread query.
	if gateDecisionCounter != nil {
		ctx := context.Background()
		for _, r := range gateDecisionReasons {
			gateDecisionCounter.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", r)))
		}
	}
}

func recordGateDecision(ctx context.Context, reason string) {
	if gateDecisionCounter == nil {
		return
	}
	gateDecisionCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func recordRelativityTop(ctx context.Context, v float64) {
	if relativityTopHist == nil {
		return
	}
	relativityTopHist.Record(ctx, v)
}

func recordRelativitySpread(ctx context.Context, v float64) {
	if relativitySpreadHist == nil {
		return
	}
	relativitySpreadHist.Record(ctx, v)
}
