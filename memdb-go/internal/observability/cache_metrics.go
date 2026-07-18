// Package observability — cache_metrics.go.
//
// VSET invalidation counters for cube-delete and related cache-management
// operations. These expose three outcomes (success|miss|error) so regressions
// like the one diagnosed in PR #234 (stale VSET returning topScore=0.99 on an
// empty cube) are immediately visible in dashboards and alertable in Prometheus.
//
// Naming follows the m12_metrics.go dotted-OTel convention; Prometheus exposes
// these as `memdb_cache_vset_invalidate_*`.
package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// vsetInvalidateOutcomes are the canonical outcome labels for the invalidate
// counter. Pre-registered at zero so the series appears from first scrape.
//
//   - success — VDrop called and Redis confirmed the key was deleted
//   - miss    — VDrop called but the VSET key did not exist (already evicted
//     by TTL or never warmed); not an error
//   - error   — VDrop returned a non-nil error (Redis unreachable, etc.)
var vsetInvalidateOutcomes = []string{"success", "miss", "error"}

type cacheInstruments struct {
	VsetInvalidateTotal metric.Int64Counter
}

var (
	cacheOnce sync.Once
	cacheInst *cacheInstruments
)

// Cache returns the singleton cache instruments, lazily initialised. All
// counters are pre-registered at zero across their label dimensions.
func Cache() *cacheInstruments {
	cacheOnce.Do(func() {
		meter := otel.Meter("memdb-go/cache")
		cacheInst = &cacheInstruments{}

		cacheInst.VsetInvalidateTotal, _ = meter.Int64Counter(
			"memdb.cache.vset_invalidate_total",
			metric.WithDescription(
				"Counts VSET key invalidation attempts triggered by cube-delete operations. "+
					"outcome in {success,miss,error}; op labels the call site (e.g. delete_cube). "+
					"error outcome should alert — it means stale VSET embeddings may persist in Redis.",
			),
		)

		// Pre-register all outcome × op combinations at zero so Grafana panels
		// and alert rules have a stable series from container start.
		ctx := context.Background()
		for _, oc := range vsetInvalidateOutcomes {
			cacheInst.VsetInvalidateTotal.Add(ctx, 0,
				metric.WithAttributes(
					attribute.String("outcome", oc),
					attribute.String("op", "delete_cube"),
				),
			)
		}
	})
	return cacheInst
}

// RecordVsetInvalidate bumps the VSET invalidation counter for the given
// op label and outcome. op should be "delete_cube" for the cube-delete path;
// add additional op values as new callers are introduced.
func RecordVsetInvalidate(ctx context.Context, op, outcome string) {
	Cache().VsetInvalidateTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("outcome", outcome),
			attribute.String("op", op),
		),
	)
}
