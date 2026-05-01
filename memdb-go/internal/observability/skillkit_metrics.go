// Package observability — skillkit_metrics.go.
//
// Singleton skillkit.Observer and skillkit.Tracer wired to MemDB's existing
// OTel MeterProvider and TracerProvider. Five metric instruments under scope
// "memdb-go/skillkit" surface at /metrics via the same Prometheus exporter
// used by D10 routing, scheduler loops, and cache metrics. Two span types
// ("skill.body", "skillkit.catalog.load") surface in Jaeger under the same
// "memdb-go/skillkit" tracer scope.
//
// Bounded label cardinality:
//   - "name"    — small known enum (~10 values: d10-extractor, atomic-extractor,
//                 memory-consolidator, feedback-memory-curator, etc.)
//   - "source"  — {"embedded","env","cache_hit","last_known_good"}  — skillkit enum
//   - "reason"  — {"unreadable","too_large","empty_body"}           — skillkit enum
//   - "outcome" — {"hit","miss"}                                    — skillkit enum
//   - "tier"    — {"scheduler-builtin", …}                         — operator-named
//
// Singleton design: a single *Observer and a single *Tracer are shared by D10,
// atomic-extractor, and scheduler. Per-skill differentiation flows through the
// "name" / "skill.name" label/attribute, not separate instances.
// Use SkillkitObserver() / SkillkitTracer() to retrieve them.
package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/anatolykoptev/skillkit"
)

const skillkitMeterScope = "memdb-go/skillkit"

type skillkitInstruments struct {
	bodyCalls   metric.Int64Counter
	envFallback metric.Int64Counter
	bodyBytes   metric.Int64Histogram
	catalogLoad metric.Int64Counter
}

// catalogSizesMu guards catalogSizes, written by CatalogSize hook on the
// operator goroutine and read by the OTel observable callback on the
// Prometheus scrape goroutine.
var (
	catalogSizesMu sync.RWMutex
	catalogSizes   = map[string]int64{} // tier name → skill count
)

var (
	skillkitOnce sync.Once
	skillkitInst *skillkitInstruments
	skillkitObsv *skillkit.Observer
	skillkitTrcr *skillkit.Tracer
)

// initSkillkitInstruments builds the OTel instruments and the Observer
// singleton. Called exactly once via sync.Once.
func initSkillkitInstruments() {
	meter := otel.Meter(skillkitMeterScope)
	inst := &skillkitInstruments{}

	inst.bodyCalls, _ = meter.Int64Counter(
		"memdb.skillkit.body_calls",
		metric.WithDescription(
			"Total Body() calls per skill, broken down by resolution source "+
				"(embedded | env | cache_hit | last_known_good)."),
	)
	inst.envFallback, _ = meter.Int64Counter(
		"memdb.skillkit.env_fallback",
		metric.WithDescription(
			"Env-override path rejected at a gate "+
				"(unreadable | too_large | empty_body). "+
				"A non-zero rate on 'unreadable' usually means the operator "+
				"typo'd MEMDB_*_SKILL_PATH."),
	)
	inst.bodyBytes, _ = meter.Int64Histogram(
		"memdb.skillkit.body_bytes",
		metric.WithUnit("By"),
		metric.WithDescription(
			"Distribution of body sizes (bytes) returned by Body() per skill. "+
				"Useful for spotting accidental truncation or env overrides that "+
				"are unexpectedly large."),
	)
	inst.catalogLoad, _ = meter.Int64Counter(
		"memdb.skillkit.catalog_load",
		metric.WithDescription(
			"Catalog Load/LoadCtx outcomes per skill name (hit | miss). "+
				"A 'miss' means a caller asked for a skill the catalog doesn't know — "+
				"usually a programmer error (see scheduler.schedulerPrompt panic guard)."),
	)

	// skillkit_catalog_skills uses an Int64ObservableGauge with a
	// register-time callback. The OTel Prometheus exporter serializes gauge
	// values only via observable (async) instruments; a synchronous
	// Int64Gauge.Record() call is never reflected in /metrics output.
	// The in-memory catalogSizes map is updated by the CatalogSize hook
	// (operator goroutine) and read by the callback on every Prometheus scrape.
	_, _ = meter.Int64ObservableGauge(
		"memdb.skillkit.catalog_skills",
		metric.WithDescription(
			"Skills loaded per catalog tier (snapshot at observer attach). "+
				"A value < expected count means //go:embed missed some SKILL.md files."),
		metric.WithInt64Callback(func(_ context.Context, obs metric.Int64Observer) error {
			catalogSizesMu.RLock()
			defer catalogSizesMu.RUnlock()
			for tier, n := range catalogSizes {
				obs.Observe(n, metric.WithAttributes(attribute.String("tier", tier)))
			}
			return nil
		}),
	)

	skillkitInst = inst

	skillkitObsv = &skillkit.Observer{
		BodyCall: func(name, source string) {
			skillkitInst.bodyCalls.Add(context.Background(), 1, metric.WithAttributes(
				attribute.String("name", name),
				attribute.String("source", source),
			))
		},
		EnvFallback: func(name, reason string) {
			skillkitInst.envFallback.Add(context.Background(), 1, metric.WithAttributes(
				attribute.String("name", name),
				attribute.String("reason", reason),
			))
		},
		BodyBytes: func(name string, bytes int) {
			skillkitInst.bodyBytes.Record(context.Background(), int64(bytes), metric.WithAttributes(
				attribute.String("name", name),
			))
		},
		CatalogLoad: func(name, outcome string) {
			skillkitInst.catalogLoad.Add(context.Background(), 1, metric.WithAttributes(
				attribute.String("name", name),
				attribute.String("outcome", outcome),
			))
		},
		CatalogSize: func(tier string, count int) {
			catalogSizesMu.Lock()
			catalogSizes[tier] = int64(count)
			catalogSizesMu.Unlock()
		},
	}

	skillkitTrcr = &skillkit.Tracer{
		StartBody: func(ctx context.Context, name string) (context.Context, func(string)) {
			ctx, span := otel.Tracer(skillkitMeterScope).Start(ctx, "skill.body",
				trace.WithAttributes(attribute.String("skill.name", name)))
			return ctx, func(source string) {
				span.SetAttributes(attribute.String("skill.source", source))
				span.End()
			}
		},
		StartCatalogLoad: func(ctx context.Context, name string) (context.Context, func(string)) {
			ctx, span := otel.Tracer(skillkitMeterScope).Start(ctx, "skillkit.catalog.load",
				trace.WithAttributes(attribute.String("skill.name", name)))
			return ctx, func(outcome string) {
				span.SetAttributes(attribute.String("catalog.outcome", outcome))
				span.End()
			}
		},
	}
}

// SkillkitObserver returns the process-wide singleton *skillkit.Observer
// for loader instrumentation. Wire into:
//   - skillkit.NewEmbedded via skillkit.WithObserver(observability.SkillkitObserver())
//   - skillkit.NewCatalog via Catalog.WithObserver(observability.SkillkitObserver())
//
// See also SkillkitTracer() for the companion OTel span adapter.
//
// Thread-safe: the Observer itself is a struct of function pointers
// set once at init; the OTel SDK instruments are concurrency-safe.
func SkillkitObserver() *skillkit.Observer {
	skillkitOnce.Do(initSkillkitInstruments)
	return skillkitObsv
}

// SkillkitTracer returns the process-wide singleton *skillkit.Tracer
// for span-level tracing of skill body and catalog-load hot paths. Wire into:
//   - skillkit.NewEmbedded via skillkit.WithTracer(observability.SkillkitTracer())
//   - skillkit.NewCatalog via Catalog.WithTracer(observability.SkillkitTracer())
//
// Two span types are emitted under the "memdb-go/skillkit" tracer scope:
//   - "skill.body"           — per Embedded.BodyCtx call; attribute "skill.source"
//   - "skillkit.catalog.load" — per Catalog.LoadCtx call; attribute "catalog.outcome"
//
// Spans nest under the request trace tree (HTTP handler → LLM call → skill load).
//
// Thread-safe: initialised exactly once by the same sync.Once that guards
// SkillkitObserver (shared initSkillkitInstruments call).
func SkillkitTracer() *skillkit.Tracer {
	skillkitOnce.Do(initSkillkitInstruments)
	return skillkitTrcr
}
