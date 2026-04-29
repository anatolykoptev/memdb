// Package handlers — metrics_prewarm.go.
//
// Pre-warms every per-package OTel meter singleton at server boot so the
// Prometheus /metrics endpoint exposes the full series space (with the
// pre-registered zero values) before the FIRST request lands.
//
// Without this, lazy sync.Once accessors leave Grafana panels blank and
// alert rules in "no data" state until traffic naturally exercises the
// code path. Eval harnesses (LoCoMo) and ops want the graphs alive on a
// cold container, even when the relevant feature has not yet fired.
package handlers

// PrewarmMetrics primes every handlers-package metric singleton so the
// Prometheus pull endpoint returns all series from container start.
// Idempotent — each underlying sync.Once fires at most once anyway.
//
// Add a new accessor here whenever a new metric singleton lands in this
// package; the cost is one nil-cheap function call at boot.
func PrewarmMetrics() {
	chatPromptMx()
	chatRefusalMx()
	chatAcceptanceMx()
	feedbackEventsMx()
	atomicMx()
	addMx()
	linkedMx()
}
