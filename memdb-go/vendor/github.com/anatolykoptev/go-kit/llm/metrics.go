// Package llm — metrics.go: an OPTIONAL Prometheus helper for the WithEndpoints
// fallback chain. Caller-wired, observer-decoupled: the request path itself
// imports no prometheus (canon, client.go EndpointAttemptObserver doc — the
// observer "не должен блокировать… не должен panic'нуть"). A consumer that wants
// chain metrics constructs a ChainMetrics with ITS OWN registry and wires the
// returned observers via WithEndpointAttemptObserver / WithModelCooldownObserver.
//
// Why caller-passes-registry instead of the package-global promauto style used by
// embed/metrics.go and rerank/metrics.go: a process may construct multiple
// llm.Clients (different providers, hedged pools). Package-global CounterVecs
// would double-register on a second registry and panic. Returning instruments
// bound to a caller-supplied Registerer avoids that — each ChainMetrics owns its
// own series on its own registry. This is the deliberate second metric-emission
// style in go-kit (Decision 2 trade-off).
//
// Metric names follow the convention (~/docs/metrics-convention.md) and the
// rerank precedent: library-namespaced (llm_*), low-cardinality labels. For a
// chain derived from a CSV via ParseModelFallbackChain the model label is
// Prometheus-label-safe by construction (chain.go isSafeModelName drops unsafe
// tokens); a chain built directly with WithEndpoints carries whatever model
// strings the caller supplies, so that caller owns label hygiene. Cardinality is
// bounded either way — position is bounded by chain length (≤~8) and the model
// set is the fixed, small chain (not user input).
package llm

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ChainMetrics holds the Prometheus instruments for a WithEndpoints fallback
// chain, registered on a caller-supplied registry. Construct one per logical
// chain (or share one across clients that route the same model set). All methods
// are safe for concurrent use (the underlying prometheus vectors are).
type ChainMetrics struct {
	served   *prometheus.CounterVec // llm_chain_served_total{model,position}
	attempt  *prometheus.CounterVec // llm_chain_attempt_total{model,outcome}
	cooldown *prometheus.GaugeVec   // llm_model_cooldown_active{model}
}

// NewChainMetrics registers the chain instruments on reg and returns a handle.
// The caller owns reg (e.g. its service's prometheus.Registry); nothing is
// registered on the global default registry, so multiple llm.Clients /
// ChainMetrics never double-register.
func NewChainMetrics(reg prometheus.Registerer) *ChainMetrics {
	served := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_chain_served_total",
		Help: "Total successful chain completions by the model that returned 200 and its chain position (0=primary).",
	}, []string{"model", "position"})
	attempt := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_chain_attempt_total",
		Help: "Total chain endpoint attempts by model, outcome (ok|error), and error_type " +
			"(auth_expiry|dependency_block|context_overflow|transient|client|unknown|empty-on-success). " +
			"Cardinality: model(≤8) × outcome(2) × error_type(7) = 112 max series. " +
			"No high-cardinality user input touches labels.",
	}, []string{"model", "outcome", "error_type"})
	cooldown := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_model_cooldown_active",
		Help: "1 while a model is in quota cooldown (free-tier quota exhausted), 0 otherwise. A 1 is expected, not an alert.",
	}, []string{"model"})

	reg.MustRegister(served, attempt, cooldown)
	return &ChainMetrics{served: served, attempt: attempt, cooldown: cooldown}
}

// ServedTotal exposes the llm_chain_served_total vector (model, position).
func (m *ChainMetrics) ServedTotal() *prometheus.CounterVec { return m.served }

// AttemptTotal exposes the llm_chain_attempt_total vector (model, outcome, error_type).
// error_type is one of: auth_expiry, dependency_block, context_overflow, transient,
// client, unknown, or "" (empty string on success). Use ClassifyErrorType to derive
// the label value from an error.
func (m *ChainMetrics) AttemptTotal() *prometheus.CounterVec { return m.attempt }

// CooldownActive exposes the llm_model_cooldown_active gauge (model).
func (m *ChainMetrics) CooldownActive() *prometheus.GaugeVec { return m.cooldown }

// unknownPosition labels a served/attempted model that is NOT present in the
// chain the observer was built with (a wire-time chain/observer mismatch). We
// never drop the event — recording it under "unknown" surfaces the mismatch
// instead of hiding it.
const unknownPosition = "unknown"

// EndpointObserver returns an EndpointAttemptObserver to wire via
// WithEndpointAttemptObserver. It increments:
//   - llm_chain_attempt_total{model,outcome} on every attempt (outcome ok|error),
//   - llm_chain_served_total{model,position} on each success.
//
// position is the model's index in endpoints (0=primary). The model→position map
// is built ONCE here (read-only thereafter), so the per-attempt observer is a
// race-free O(1) lookup — the EndpointAttemptObserver signature carries only
// (Endpoint, err), so the chain order must be supplied at wire time. Pass the
// SAME []Endpoint you gave WithEndpoints. A model absent from that slice records
// under position="unknown" rather than being dropped.
//
// Call this ONCE at wire time (build the observer, hand it to
// WithEndpointAttemptObserver), not per request: each call allocates a fresh
// position map. The returned observer is what fires per attempt; the builder is
// setup-only.
func (m *ChainMetrics) EndpointObserver(endpoints []Endpoint) EndpointAttemptObserver {
	pos := make(map[string]string, len(endpoints))
	for i, ep := range endpoints {
		if _, dup := pos[ep.Model]; dup {
			continue // first wins — a model id maps to its earliest chain position
		}
		pos[ep.Model] = strconv.Itoa(i)
	}
	return func(ep Endpoint, err error) {
		if err != nil {
			m.attempt.WithLabelValues(ep.Model, "error", ClassifyErrorType(err)).Inc()
			return
		}
		m.attempt.WithLabelValues(ep.Model, "ok", "").Inc()
		position, ok := pos[ep.Model]
		if !ok {
			position = unknownPosition
		}
		m.served.WithLabelValues(ep.Model, position).Inc()
	}
}

// CooldownObserver returns the callback to wire via WithModelCooldownObserver. It
// drives the llm_model_cooldown_active{model} gauge: 1 on cooldown entry
// (cooling=true), 0 on recovery (cooling=false). The duration argument is unused
// by the gauge (the observer log line carries it if a consumer wants it).
//
// Note: a per-skip counter (llm_model_cooldown_skipped_total) is intentionally
// NOT emitted — the cooldown seam fires once per window (entry/recovery), not
// once per skipped attempt, so a skip counter would need a new request-path
// callback. The active gauge already answers the USE-saturation question ("is
// this model's quota exhausted right now?") from the existing seam. See the
// package doc and the plan's Decision 2 deviation note.
func (m *ChainMetrics) CooldownObserver() func(model string, cooling bool, d time.Duration) {
	return func(model string, cooling bool, _ time.Duration) {
		if cooling {
			m.cooldown.WithLabelValues(model).Set(1)
			return
		}
		m.cooldown.WithLabelValues(model).Set(0)
	}
}
