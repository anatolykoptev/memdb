// Metrics — Prometheus instruments for embedder backends.
//
// Namespace was renamed from memdb_embedder_* (OpenTelemetry) to embed_*
// (pure prometheus client_golang) when the package moved out of memdb-go.
// This matches the rerank_* convention in github.com/anatolykoptev/go-kit/rerank.
//
// Series:
//
//   - embed_requests_total{backend, outcome}   — counter
//   - embed_duration_seconds{backend}          — histogram
//   - embed_batch_size{backend}                — histogram
//   - embed_retry_total{reason}                — counter
//
// All four "reason" labels (transient, http_429, http_5xx, context) are
// pre-registered at zero so dashboards see the full series set from
// container start.

package embed

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
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
			Help: "Embed retry attempts by reason (transient|http_429|http_5xx|context).",
		},
		[]string{"reason"},
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

// recordRequest records a single embed call's duration, batch size, and outcome.
func recordRequest(backend, outcome string, batchSize int, d time.Duration) {
	embedRequestsTotal.WithLabelValues(backend, outcome).Inc()
	embedDurationSeconds.WithLabelValues(backend).Observe(d.Seconds())
	embedBatchSize.WithLabelValues(backend).Observe(float64(batchSize))
}

// recordRetryReason bumps the retry counter for the classified reason.
func recordRetryReason(reason string) {
	embedRetryTotal.WithLabelValues(reason).Inc()
}
