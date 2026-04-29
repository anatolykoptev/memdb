package onnx

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// onnx_* metrics are registered separately from the parent embed_* set so
// the cgo-only path is a clearly distinguishable backend in dashboards.
//
// Series:
//
//   - onnx_embed_requests_total{backend, outcome}
//   - onnx_embed_duration_seconds{backend}
//   - onnx_embed_batch_size{backend}
//
// "backend" is fixed to "onnx" today but kept as a label for forward
// compatibility (CPU vs GPU build matrices, future ROCm / TensorRT
// variants).

var (
	onnxRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "onnx_embed_requests_total",
			Help: "Total ONNX embed requests by backend and outcome (success|error).",
		},
		[]string{"backend", "outcome"},
	)
	onnxDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "onnx_embed_duration_seconds",
			Help:    "ONNX embed request duration by backend.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0},
		},
		[]string{"backend"},
	)
	onnxBatchSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "onnx_embed_batch_size",
			Help:    "Number of texts per ONNX embed request.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512},
		},
		[]string{"backend"},
	)
)

// recordEmbed mirrors the parent embed.recordRequest shape.
func recordEmbed(backend, outcome string, batchSize int, d time.Duration) {
	onnxRequestsTotal.WithLabelValues(backend, outcome).Inc()
	onnxDurationSeconds.WithLabelValues(backend).Observe(d.Seconds())
	onnxBatchSize.WithLabelValues(backend).Observe(float64(batchSize))
}
