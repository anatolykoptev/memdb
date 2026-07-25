// Package llm — metrics_prewarm.go.
//
// Pre-warms every per-package OTel meter singleton at server boot so the
// Prometheus /metrics endpoint exposes the full series space (with
// pre-registered zero values) before the FIRST request lands.
//
// Without this, lazy sync.Once accessors leave Grafana panels blank and
// alert rules in "no data" state until traffic naturally exercises the
// code path.
package llm

// PrewarmMetrics primes every llm-package metric singleton so the
// Prometheus pull endpoint returns all series from container start.
// Idempotent — each underlying sync.Once fires at most once anyway.
func PrewarmMetrics() {
	llmMetrics()
	llmStructuredMetrics()
}
