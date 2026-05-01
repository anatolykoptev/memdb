package skillkit

import "context"

// Tracer hooks span creation into the loader hot paths. All fields are
// optional; a nil hook is no-op. Caller adapts to its tracer of choice
// (OpenTelemetry, OpenTracing, slog Tracer, etc.) — skillkit core stays
// stdlib-only.
//
// Adapter pattern (OpenTelemetry):
//
//	tr := &skillkit.Tracer{
//	    StartBody: func(ctx context.Context, name string) (context.Context, func(string)) {
//	        ctx, span := otel.Tracer("skillkit").Start(ctx, "skill.body",
//	            trace.WithAttributes(attribute.String("skill.name", name)))
//	        return ctx, func(source string) {
//	            span.SetAttributes(attribute.String("skill.source", source))
//	            span.End()
//	        }
//	    },
//	    StartCatalogLoad: func(ctx context.Context, name string) (context.Context, func(string)) {
//	        ctx, span := otel.Tracer("skillkit").Start(ctx, "catalog.load",
//	            trace.WithAttributes(attribute.String("skill.name", name)))
//	        return ctx, func(outcome string) {
//	            span.SetAttributes(attribute.String("catalog.outcome", outcome))
//	            span.End()
//	        }
//	    },
//	}
//
// Backwards compatibility: future skillkit versions may add new hook
// fields. Adding a nil-able field is non-breaking; existing Tracers
// continue to work. Removing a field is a breaking change reserved for
// v2.0.0.
//
// Hook contract: Start* implementations MUST return a non-nil end-fn.
// Returning nil will panic the loader on span close — the runtime guards
// only the field itself, not the dynamic return value. Construct the
// end-fn as a closure even when the underlying tracer no-ops the span.
type Tracer struct {
	// StartBody opens a span around a single Embedded.BodyCtx call. The
	// end-fn is called when Body resolution completes, with the same
	// source label vocabulary as Observer.BodyCall:
	//   - "embedded"        — embedded default served
	//   - "env"             — env override path read this call
	//   - "cache_hit"       — env path mtime matched cache
	//   - "last_known_good" — env stat-failed, cached body returned
	//
	// The returned context is captured for future use but currently not
	// threaded into resolveBody (which is purely local). Implementations
	// should still return ctx so Catalog.LoadCtx and a future ctx-aware
	// resolver chain remain symmetric.
	StartBody func(ctx context.Context, name string) (context.Context, func(source string))

	// StartCatalogLoad opens a span around a single Catalog.LoadCtx call.
	// The returned context replaces ctx for downstream FindCtx calls so
	// ContextResolver tier spans nest under the catalog span. The end-fn
	// outcome label vocabulary matches Observer.CatalogLoad:
	//   - "hit"  — a tier resolved the name
	//   - "miss" — no tier had the name
	StartCatalogLoad func(ctx context.Context, name string) (context.Context, func(outcome string))
}
