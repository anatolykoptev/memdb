package search

// d10_metrics.go — D10 routing observability counters + histograms.
//
// Why a separate file: metrics.go has grown into a single large registration
// block; D10 routing telemetry is its own concern (post-PR-#255 instrumentation
// to diagnose why soft-routing regressed cefix7 → cefix8 from Judge 0.300 to
// 0.220). Keeping the routing metrics here means the diagnostic surface is
// obvious and the next reader does not have to grep through 300 lines of
// other-feature wiring to find it.
//
// What we instrument:
//   - d10_route{mode, top1_cat}     — counter, "which path did we take + with
//                                     which top-1 category"
//   - d10_top1_confidence{top1_cat} — histogram, "did the classifier produce
//                                     enough mass on top-1 to ever cross the
//                                     hard threshold?"
//   - d10_distribution_entropy      — histogram, "is the classifier confident
//                                     or is its output noise?"
//   - d10_top1_top2_gap             — histogram, "how decisive is the top-1
//                                     vs runner-up?"
//   - d10_outcome_by_cat{top1_cat,
//                        outcome}   — counter, "where (per category) does
//                                     the LLM give up (UNKNOWN)?"
//
// Cardinality: 21 (route) + 5 (top1_conf) + 1 (entropy) + 1 (gap) + 20 (outcome)
// = 48 new series. Bounded — top1_cat is the 5-element classifierCategoryOrder
// plus a sentinel "none" for the disabled / no-signal path.

import (
	"context"
	"math"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// D10RoutingMode is the discrete decision the prompt builder made for one
// D10 enhancement call. Bounded set, used as a metric label.
type D10RoutingMode string

const (
	// D10RouteHard — top-1 ≥ HARD_ROUTING_THRESHOLD AND a category-specific
	// hard prompt exists. Full prompt replacement.
	D10RouteHard D10RoutingMode = "hard"
	// D10RouteSoft — base prompt + soft distribution block.
	D10RouteSoft D10RoutingMode = "soft"
	// D10RouteBase — no classifier signal (nil emb, classifier disabled,
	// embed error, empty distribution, OR open_domain at hard threshold).
	// Returned prompt is byte-identical to base.
	D10RouteBase D10RoutingMode = "base"
)

// d10Top1NoneSentinel is the top1_cat label value used when no classifier
// signal is available (D10RouteBase from disabled / nil emb / no signal).
// Distinct from QueryCategoryOpenDomain — open_domain at hard threshold also
// produces D10RouteBase but its top1_cat label is QueryCategoryOpenDomain.
const d10Top1NoneSentinel = "none"

var (
	d10MetricsOnce sync.Once
	d10Metrics     *d10MetricsInstruments
)

type d10MetricsInstruments struct {
	// Route — counter per (mode, top1_cat). Increments once per D10 call.
	Route metric.Int64Counter
	// Top1Confidence — histogram of softmax-normalised top-1 probability,
	// labelled by top1_cat so we can see calibration per category.
	Top1Confidence metric.Float64Histogram
	// DistEntropy — Shannon entropy of the full classifier distribution
	// (in nats; max = ln(5) ≈ 1.609 for a uniform 5-way distribution).
	// Low entropy = sharp, high entropy = diffuse.
	DistEntropy metric.Float64Histogram
	// Top12Gap — top-1 minus top-2 softmax probability. Decisiveness.
	Top12Gap metric.Float64Histogram
	// OutcomeByCat — counter per (top1_cat, outcome). Lets us see
	// "cat-3 temporal gives 60% UNKNOWN vs cat-1 single_hop 80% UNKNOWN" —
	// pinpoint where the LLM gives up.
	OutcomeByCat metric.Int64Counter
}

func d10Mx() *d10MetricsInstruments {
	d10MetricsOnce.Do(func() {
		m := otel.Meter("memdb-go/search")
		route, _ := m.Int64Counter("memdb.search.d10_route",
			metric.WithDescription("D10 prompt routing decision (mode=hard|soft|base, top1_cat=single_hop|multi_hop|temporal|open_domain|adversarial|none)"))
		top1, _ := m.Float64Histogram("memdb.search.d10_top1_confidence",
			metric.WithDescription("D10 classifier top-1 softmax probability, per top1_cat label"),
			metric.WithExplicitBucketBoundaries(0.2, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.97, 0.99))
		entropy, _ := m.Float64Histogram("memdb.search.d10_distribution_entropy",
			metric.WithDescription("D10 classifier distribution Shannon entropy in nats. Max ≈ ln(5) = 1.609 for uniform 5-way."),
			metric.WithExplicitBucketBoundaries(0.1, 0.3, 0.5, 0.8, 1.0, 1.2, 1.4, 1.5, 1.6))
		gap, _ := m.Float64Histogram("memdb.search.d10_top1_top2_gap",
			metric.WithDescription("D10 classifier top-1 minus top-2 softmax probability. Higher = more decisive."),
			metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.7, 0.9))
		outByCat, _ := m.Int64Counter("memdb.search.d10_outcome_by_cat",
			metric.WithDescription("D10 enhance outcomes (answered|unknown|skipped|error) split by top1_cat for failure attribution"))
		d10Metrics = &d10MetricsInstruments{
			Route:          route,
			Top1Confidence: top1,
			DistEntropy:    entropy,
			Top12Gap:       gap,
			OutcomeByCat:   outByCat,
		}
		// Pre-register all label combinations at zero so Prometheus scrapers
		// see every series from cold start.
		ctx := context.Background()
		categoryLabels := []string{
			string(QueryCategorySingleHop),
			string(QueryCategoryMultiHop),
			string(QueryCategoryTemporal),
			string(QueryCategoryOpenDomain),
			string(QueryCategoryAdversarial),
			d10Top1NoneSentinel,
		}
		for _, mode := range []D10RoutingMode{D10RouteHard, D10RouteSoft, D10RouteBase} {
			for _, cat := range categoryLabels {
				route.Add(ctx, 0, metric.WithAttributes(
					attribute.String("mode", string(mode)),
					attribute.String("top1_cat", cat),
				))
			}
		}
		for _, cat := range categoryLabels {
			top1.Record(ctx, 0, metric.WithAttributes(attribute.String("top1_cat", cat)))
			for _, oc := range []string{"answered", "unknown", "skipped", "error"} {
				outByCat.Add(ctx, 0, metric.WithAttributes(
					attribute.String("top1_cat", cat),
					attribute.String("outcome", oc),
				))
			}
		}
		entropy.Record(ctx, 0)
		gap.Record(ctx, 0)
	})
	return d10Metrics
}

// d10RoutingTrace captures every numeric signal that fed the routing
// decision. Filled by buildAnswerEnhanceSystemPrompt and threaded through
// EnhanceRetrievalAnswer to applyAnswerEnhancement so we can record metrics +
// sample-log without re-running the classifier.
//
// All histograms are in [0, 1] except Entropy (in nats, [0, ln(5) ≈ 1.609]).
type d10RoutingTrace struct {
	Mode        D10RoutingMode
	Top1Cat     QueryCategory // QueryCategoryOpenDomain when mode=base from open_domain skip; "" when mode=base from nil emb / disabled / no signal
	Top1Conf    float64
	Top2Conf    float64
	Entropy     float64
	HasSignal   bool // false when classifier returned no-signal sentinel — Top1Cat is meaningless
}

// top1Label converts the trace's Top1Cat to the metric label string.
// HasSignal=false → sentinel "none"; otherwise the category string.
func (t d10RoutingTrace) top1Label() string {
	if !t.HasSignal {
		return d10Top1NoneSentinel
	}
	return string(t.Top1Cat)
}

// recordD10Routing emits all routing metrics for one D10 enhancement call.
// Called once per applyAnswerEnhancement invocation, regardless of outcome.
func recordD10Routing(ctx context.Context, t d10RoutingTrace) {
	mx := d10Mx()
	label := t.top1Label()
	mx.Route.Add(ctx, 1, metric.WithAttributes(
		attribute.String("mode", string(t.Mode)),
		attribute.String("top1_cat", label),
	))
	if t.HasSignal {
		mx.Top1Confidence.Record(ctx, t.Top1Conf, metric.WithAttributes(attribute.String("top1_cat", label)))
		mx.DistEntropy.Record(ctx, t.Entropy)
		gap := t.Top1Conf - t.Top2Conf
		if gap < 0 {
			gap = 0
		}
		mx.Top12Gap.Record(ctx, gap)
	}
}

// recordD10OutcomeByCat increments the outcome counter labelled by the
// classifier's top-1 category. Called from applyAnswerEnhancement after the
// LLM call returns, separate from recordD10Routing because the outcome is
// only known post-LLM.
func recordD10OutcomeByCat(ctx context.Context, t d10RoutingTrace, outcome string) {
	mx := d10Mx()
	mx.OutcomeByCat.Add(ctx, 1, metric.WithAttributes(
		attribute.String("top1_cat", t.top1Label()),
		attribute.String("outcome", outcome),
	))
}

// distributionEntropy computes Shannon entropy in nats for a probability
// distribution. Zero-mass entries are skipped (0 * log(0) := 0). Returns 0
// when dist is empty / only one entry.
func distributionEntropy(dist []CategoryConfidence) float64 {
	if len(dist) <= 1 {
		return 0
	}
	var h float64
	for _, c := range dist {
		if c.Confidence <= 0 {
			continue
		}
		h -= c.Confidence * math.Log(c.Confidence)
	}
	return h
}
