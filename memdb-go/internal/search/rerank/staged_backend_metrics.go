// Package rerank — staged-backend ablation telemetry (M12.6).
//
// Adds a SECOND counter on top of the legacy `memdb.search.d5_staged`
// series so operators can A/B the LLM-vs-CE backends without losing
// the existing dashboard panels. The legacy series stays load-bearing
// for the per-stage outcome view; the new series exposes which
// *backend* (ce | llm | hybrid) actually served the call and whether
// the resolution happened cleanly or fell back from the requested one.
//
// Outcome semantics:
//
//	success  — requested backend ran end-to-end without falling back
//	fallback — requested CE/Hybrid degraded to LLM (or vice versa)
//	           because the underlying client was unavailable; the call
//	           still returned a result, just not from the requested path
//	error    — both stages failed; staged returned input unchanged
//
// Pre-registered at zero across the cross-product so dashboards see
// every series from container start.
package rerank

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// stagedBackendCounter records every Staged.Rerank dispatch tagged by
// the resolved backend and outcome. Initialised lazily by initStagedBackendMetrics.
var stagedBackendCounter metric.Int64Counter

// stagedBackendNames is the closed set of label values for the `backend`
// attribute. Adding a new backend MUST extend this list to keep the
// dashboard panels populated from start.
var stagedBackendNames = []string{"ce", "llm", "hybrid"}

// stagedBackendOutcomes is the closed set of label values for the
// `outcome` attribute.
var stagedBackendOutcomes = []string{"success", "fallback", "error"}

func init() {
	m := otel.Meter("memdb-go/search/rerank")
	c, _ := m.Int64Counter("memdb.search.staged.backend_total",
		metric.WithDescription("M12.6 staged-retrieval backend dispatch (backend in ce|llm|hybrid, outcome in success|fallback|error)"))
	stagedBackendCounter = c
	ctx := context.Background()
	for _, name := range stagedBackendNames {
		for _, oc := range stagedBackendOutcomes {
			c.Add(ctx, 0, metric.WithAttributes(
				attribute.String("backend", name),
				attribute.String("outcome", oc),
			))
		}
	}
}

// recordStagedBackend bumps the staged-backend counter. No-op when the
// counter is unset (test harnesses without otel init).
func recordStagedBackend(ctx context.Context, backend, outcome string) {
	if stagedBackendCounter == nil {
		return
	}
	stagedBackendCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("backend", backend),
		attribute.String("outcome", outcome),
	))
}
