// Metrics — Prometheus instruments for sparse-embedding backends.
//
// Namespace gokit_sparse_* (chosen to disambiguate from embed_* and
// rerank_* — the embed/rerank packages predate the gokit_ prefix
// convention and are kept stable for backward compatibility; new
// packages adopt the prefixed form).
//
// E0 series:
//
//   - gokit_sparse_requests_total{backend, outcome}        — counter
//   - gokit_sparse_request_duration_seconds{backend}       — histogram
//   - gokit_sparse_batch_size{backend}                     — histogram
//   - gokit_sparse_terms_per_vector{backend}               — histogram
//   - gokit_sparse_retry_total{reason}                     — counter (v1 internal retry)
//
// E1 series:
//
//   - gokit_sparse_retry_attempt_total{backend, attempt}      — counter
//   - gokit_sparse_circuit_state{backend, state}              — gauge
//   - gokit_sparse_circuit_transition_total{backend, from, to} — counter
//   - gokit_sparse_giveup_total{backend, reason}              — counter
//   - gokit_sparse_fallback_used_total{primary, secondary}    — counter
//
// E3 series:
//
//   - gokit_sparse_cache_hit_total{model}      — counter (full-batch hit events)
//   - gokit_sparse_cache_miss_total{model}     — counter
//   - gokit_sparse_cache_set_docs_total{model} — counter (per-text)

package sparse

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ── E0 metrics ──────────────────────────────────────────────────────────

	sparseRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_requests_total",
			Help: "Total sparse embed requests by backend and outcome (success|error).",
		},
		[]string{"backend", "outcome"},
	)
	sparseRequestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gokit_sparse_request_duration_seconds",
			Help:    "Sparse embed request duration by backend.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0},
		},
		[]string{"backend"},
	)
	sparseBatchSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gokit_sparse_batch_size",
			Help:    "Number of texts per sparse embed request.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512},
		},
		[]string{"backend"},
	)
	sparseTermsPerVector = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gokit_sparse_terms_per_vector",
			Help:    "Non-zero terms per output sparse vector.",
			Buckets: []float64{1, 4, 16, 32, 64, 128, 256, 512, 1024},
		},
		[]string{"backend"},
	)
	sparseRetryTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_retry_total",
			Help: "Sparse retry attempts by reason (transient|http_429|http_5xx|context). v1 internal retry only.",
		},
		[]string{"reason"},
	)

	// ── E1 metrics ──────────────────────────────────────────────────────────

	sparseRetryAttemptTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_retry_attempt_total",
			Help: "Total sparse retry attempts after the initial attempt, by backend and attempt number.",
		},
		[]string{"backend", "attempt"},
	)
	sparseCircuitStateGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gokit_sparse_circuit_state",
			Help: "Current sparse circuit breaker state: 0=closed, 1=open, 2=half-open.",
		},
		[]string{"backend", "state"},
	)
	sparseCircuitTransitionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_circuit_transition_total",
			Help: "Total sparse circuit breaker state transitions by backend, from and to state.",
		},
		[]string{"backend", "from", "to"},
	)
	sparseGiveupTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_giveup_total",
			Help: "Total sparse requests that gave up: reason=exhausted|circuit_open|4xx.",
		},
		[]string{"backend", "reason"},
	)
	sparseFallbackUsedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_fallback_used_total",
			Help: "Total successful fallback invocations from primary to secondary sparse backend.",
		},
		[]string{"primary", "secondary"},
	)

	// ── E3 metrics ──────────────────────────────────────────────────────────

	sparseCacheHitTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_cache_hit_total",
			Help: "Total sparse full-batch cache hit events (one per request, not per text).",
		},
		[]string{"model"},
	)
	sparseCacheMissTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_cache_miss_total",
			Help: "Total sparse cache miss events (one per request that fell through to backend).",
		},
		[]string{"model"},
	)
	sparseCacheSetDocsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gokit_sparse_cache_set_docs_total",
			Help: "Total sparse vectors written to cache after backend call (per-text).",
		},
		[]string{"model"},
	)
)

func init() {
	for _, reason := range []string{"transient", "http_429", "http_5xx", "context"} {
		sparseRetryTotal.WithLabelValues(reason).Add(0)
	}
}

// Outcome label values.
const (
	outcomeSuccess = "success"
	outcomeError   = "error"
)

// recordRequest records a single sparse-embed call's batch size, duration,
// outcome, and per-vector term count distribution. termCounts may be nil
// when the call failed before producing vectors.
func recordRequest(backend, outcome string, batchSize int, termCounts []int, d time.Duration) {
	sparseRequestsTotal.WithLabelValues(backend, outcome).Inc()
	sparseRequestDurationSeconds.WithLabelValues(backend).Observe(d.Seconds())
	sparseBatchSize.WithLabelValues(backend).Observe(float64(batchSize))
	for _, n := range termCounts {
		sparseTermsPerVector.WithLabelValues(backend).Observe(float64(n))
	}
}

// recordRetryReason bumps the v1-internal retry counter for the classified reason.
func recordRetryReason(reason string) {
	sparseRetryTotal.WithLabelValues(reason).Inc()
}

// recordRetryAttempt increments the retry counter for the given backend and
// attempt number (1-indexed: 1 = first retry after initial failure).
func recordRetryAttempt(backend string, attempt int) {
	sparseRetryAttemptTotal.WithLabelValues(backend, itoa(attempt)).Inc()
}

// recordCircuitState updates the circuit state gauge for the given backend.
func recordCircuitState(backend string, state CircuitState) {
	states := []CircuitState{CircuitClosed, CircuitOpen, CircuitHalfOpen}
	for _, s := range states {
		v := 0.0
		if s == state {
			v = 1.0
		}
		sparseCircuitStateGauge.WithLabelValues(backend, s.String()).Set(v)
	}
}

// recordCircuitTransition increments the transition counter.
func recordCircuitTransition(backend string, from, to CircuitState) {
	sparseCircuitTransitionTotal.WithLabelValues(backend, from.String(), to.String()).Inc()
}

// recordGiveup increments the giveup counter for the given reason.
func recordGiveup(backend, reason string) {
	sparseGiveupTotal.WithLabelValues(backend, reason).Inc()
}

// recordFallbackUsed increments the fallback counter.
func recordFallbackUsed(primary, secondary string) {
	sparseFallbackUsedTotal.WithLabelValues(primary, secondary).Inc()
}

// recordCacheHit increments the cache-hit EVENT counter by 1 (one per request).
func recordCacheHit(model string) {
	sparseCacheHitTotal.WithLabelValues(model).Inc()
}

// recordCacheMiss increments the cache-miss EVENT counter by 1 (one per request).
func recordCacheMiss(model string) {
	sparseCacheMissTotal.WithLabelValues(model).Inc()
}

// recordCacheSet adds n to the cache-set-docs counter (per-text granularity).
func recordCacheSet(model string, n int) {
	sparseCacheSetDocsTotal.WithLabelValues(model).Add(float64(n))
}

// itoa converts a non-negative integer to its decimal string. Avoids
// importing strconv into this file.
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
