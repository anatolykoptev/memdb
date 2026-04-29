// Metrics — Prometheus instruments for embedder backends.
//
// Namespace was renamed from memdb_embedder_* (OpenTelemetry) to embed_*
// (pure prometheus client_golang) when the package moved out of memdb-go.
// This matches the rerank_* convention in github.com/anatolykoptev/go-kit/rerank.
//
// E0 series:
//
//   - embed_requests_total{backend, outcome}   — counter
//   - embed_duration_seconds{backend}          — histogram
//   - embed_batch_size{backend}                — histogram
//   - embed_retry_total{reason}                — counter (v1 internal retry)
//
// E1 series:
//
//   - embed_retry_attempt_total{backend, attempt} — counter
//   - embed_circuit_state{backend, state}          — gauge (0=closed,1=open,2=half-open)
//   - embed_circuit_transition_total{backend, from, to} — counter
//   - embed_giveup_total{backend, reason}          — counter
//   - embed_fallback_used_total{primary, secondary} — counter
//
// E3 series:
//
//   - embed_cache_hit_total{model}      — counter (full-batch hit events, NOT per-text)
//   - embed_cache_miss_total{model}     — counter (fall-through-to-backend events)
//   - embed_cache_set_docs_total{model} — counter (texts written to cache, per-text)

package embed

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ── E0 metrics ──────────────────────────────────────────────────────────────

	embedRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_requests_total",
			Help: "Total embed requests by backend and outcome (success|error).",
		},
		[]string{"backend", "outcome"},
	)
	embedDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "embed_duration_seconds",
			Help:    "Embed request duration by backend.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0},
		},
		[]string{"backend"},
	)
	embedBatchSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "embed_batch_size",
			Help:    "Number of texts per embed request.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512},
		},
		[]string{"backend"},
	)
	embedRetryTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_retry_total",
			Help: "Embed retry attempts by reason (transient|http_429|http_5xx|context). v1 internal retry only.",
		},
		[]string{"reason"},
	)

	// ── E1 metrics ──────────────────────────────────────────────────────────────

	// embedRetryAttemptTotal counts each retry attempt (not the initial attempt).
	// Labels: backend, attempt (string "1", "2", ...).
	embedRetryAttemptTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_retry_attempt_total",
			Help: "Total retry attempts after the initial attempt, by backend and attempt number.",
		},
		[]string{"backend", "attempt"},
	)

	// embedCircuitStateGauge is a gauge tracking the current circuit breaker state
	// (0=closed, 1=open, 2=half-open) per backend. Updated on each state change.
	embedCircuitStateGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "embed_circuit_state",
			Help: "Current circuit breaker state: 0=closed, 1=open, 2=half-open.",
		},
		[]string{"backend", "state"},
	)

	// embedCircuitTransitionTotal counts circuit breaker state transitions.
	embedCircuitTransitionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_circuit_transition_total",
			Help: "Total circuit breaker state transitions by backend, from and to state.",
		},
		[]string{"backend", "from", "to"},
	)

	// embedGiveupTotal counts requests that gave up without a successful response.
	// reason: exhausted (retries exhausted), circuit_open, 4xx (non-retryable).
	embedGiveupTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_giveup_total",
			Help: "Total requests that gave up on retry: reason=exhausted|circuit_open|4xx.",
		},
		[]string{"backend", "reason"},
	)

	// embedFallbackUsedTotal counts successful fallback invocations.
	embedFallbackUsedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_fallback_used_total",
			Help: "Total successful fallback invocations from primary to secondary backend.",
		},
		[]string{"primary", "secondary"},
	)

	// ── E3 metrics ──────────────────────────────────────────────────────────────

	// embedCacheHitTotal counts full-batch cache hit EVENTS (one per request
	// where all texts were served from cache). NOT per-text: a 10-text request
	// that fully hits is +1, not +10.
	embedCacheHitTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_cache_hit_total",
			Help: "Total full-batch cache hit events (one per request, not per text).",
		},
		[]string{"model"},
	)

	// embedCacheMissTotal counts cache miss EVENTS (one per request that fell
	// through to the backend due to any partial or full cache miss).
	embedCacheMissTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_cache_miss_total",
			Help: "Total cache miss events (one per request that fell through to backend).",
		},
		[]string{"model"},
	)

	// embedCacheSetDocsTotal counts individual text embeddings written to cache
	// after a successful backend call (doc-level granularity: +N for N texts).
	embedCacheSetDocsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "embed_cache_set_docs_total",
			Help: "Total text embeddings written to cache after backend call (per-text).",
		},
		[]string{"model"},
	)
)

// init pre-registers retry-reason labels at zero so all four series are
// visible from container start. Mirrors the OpenTelemetry behaviour in the
// memdb-go predecessor.
func init() {
	for _, reason := range []string{"transient", "http_429", "http_5xx", "context"} {
		embedRetryTotal.WithLabelValues(reason).Add(0)
	}
}

// Outcome label values for the embed_requests_total counter.
const (
	outcomeSuccess = "success"
	outcomeError   = "error"
)

// ── E0 helpers ───────────────────────────────────────────────────────────────

// recordRequest records a single embed call's duration, batch size, and outcome.
func recordRequest(backend, outcome string, batchSize int, d time.Duration) {
	embedRequestsTotal.WithLabelValues(backend, outcome).Inc()
	embedDurationSeconds.WithLabelValues(backend).Observe(d.Seconds())
	embedBatchSize.WithLabelValues(backend).Observe(float64(batchSize))
}

// recordRetryReason bumps the v1-internal retry counter for the classified reason.
func recordRetryReason(reason string) {
	embedRetryTotal.WithLabelValues(reason).Inc()
}

// ── E1 helpers ───────────────────────────────────────────────────────────────

// recordRetryAttempt increments the retry counter for the given backend and
// attempt number (1-indexed: 1 = first retry after initial failure).
func recordRetryAttempt(backend string, attempt int) {
	embedRetryAttemptTotal.WithLabelValues(backend, itoa(attempt)).Inc()
}

// recordCircuitState updates the circuit state gauge for the given backend.
// Only the active state label is set to 1; the others are set to 0.
func recordCircuitState(backend string, state CircuitState) {
	states := []CircuitState{CircuitClosed, CircuitOpen, CircuitHalfOpen}
	for _, s := range states {
		v := 0.0
		if s == state {
			v = 1.0
		}
		embedCircuitStateGauge.WithLabelValues(backend, s.String()).Set(v)
	}
}

// recordCircuitTransition increments the transition counter.
func recordCircuitTransition(backend string, from, to CircuitState) {
	embedCircuitTransitionTotal.WithLabelValues(backend, from.String(), to.String()).Inc()
}

// recordGiveup increments the giveup counter for the given reason.
func recordGiveup(backend, reason string) {
	embedGiveupTotal.WithLabelValues(backend, reason).Inc()
}

// recordFallbackUsed increments the fallback counter.
func recordFallbackUsed(primary, secondary string) {
	embedFallbackUsedTotal.WithLabelValues(primary, secondary).Inc()
}

// ── E3 helpers ───────────────────────────────────────────────────────────────

// recordCacheHit increments the cache-hit EVENT counter by 1 (one per request).
func recordCacheHit(model string) {
	embedCacheHitTotal.WithLabelValues(model).Inc()
}

// recordCacheMiss increments the cache-miss EVENT counter by 1 (one per request).
func recordCacheMiss(model string) {
	embedCacheMissTotal.WithLabelValues(model).Inc()
}

// recordCacheSet adds n to the cache-set-docs counter (per-text granularity).
func recordCacheSet(model string, n int) {
	embedCacheSetDocsTotal.WithLabelValues(model).Add(float64(n))
}

// itoa converts a non-negative integer to its decimal string representation.
// Avoids importing strconv into this file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
