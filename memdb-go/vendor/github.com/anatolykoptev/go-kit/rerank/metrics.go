package rerank

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	rerankRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rerank_requests_total",
			Help: "Total rerank requests by model and status (ok|error).",
		},
		[]string{"model", "status"},
	)
	rerankDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "rerank_duration_seconds",
			Help: "Rerank request duration by model.",
			Buckets: []float64{
				0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
			},
		},
		[]string{"model"},
	)
)

func recordStatus(model, status string) {
	rerankRequestsTotal.WithLabelValues(model, status).Inc()
}

func recordDuration(model string, d time.Duration) {
	rerankDurationSeconds.WithLabelValues(model).Observe(d.Seconds())
}
