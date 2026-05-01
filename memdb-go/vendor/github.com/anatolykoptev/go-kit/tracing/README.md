# tracing

OpenTelemetry distributed tracing helpers for go-* services. Wraps the
canonical OTel SDK with a one-call `Setup`, a `Start` shortcut, and two
middleware packages for the gaps the upstream contrib repo doesn't cover.

## Why this package

| Concern | Use this | Use canonical |
|---------|---------|---------------|
| TracerProvider bootstrap, propagators, OTLP/HTTP exporter | `tracing.Setup(ctx, "service")` | — (no established OSS helper, only 0-star repos) |
| HTTP server middleware | `tracing/httpmw.Handler` (route-pattern span names) | `otelhttp.NewHandler` directly when path-label customisation isn't needed |
| MCP server middleware | `tracing/mcpmw.Middleware("service")` | — (**no canonical otelmcp exists upstream — verified 2026-04-30**) |
| gRPC client/server | — | `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` |
| `database/sql` queries | — | `github.com/XSAM/otelsql` |

## Quickstart

```go
package main

import (
    "context"
    "log/slog"

    "github.com/anatolykoptev/go-kit/tracing"
    "github.com/anatolykoptev/go-kit/tracing/httpmw"
    "go.opentelemetry.io/otel/attribute"
)

func main() {
    ctx := context.Background()

    shutdown, err := tracing.Setup(ctx, "go-search",
        tracing.WithSampleRatio(0.1), // 10% in prod
        tracing.WithAttributes(attribute.String("version", version)),
    )
    if err != nil {
        slog.Error("tracing setup", "error", err)
    }
    defer shutdown(context.Background())

    mux := http.NewServeMux()
    mux.HandleFunc("GET /research", handleResearch)

    handler := httpmw.Handler("go-search", mux)
    http.ListenAndServe(":8890", handler)
}

func handleResearch(w http.ResponseWriter, r *http.Request) {
    ctx, span := tracing.Start(r.Context(), "search.research",
        attribute.String("query", r.URL.Query().Get("q")))
    defer span.End()

    results, err := doSearch(ctx, ...)
    if err != nil {
        tracing.RecordError(span, err)
        return
    }
    span.SetAttributes(attribute.Int("results.count", len(results)))
}
```

## MCP wiring

Use side-by-side with `metrics/mcpmw` to get RED metrics + per-call spans.

```go
import (
    metricsmw "github.com/anatolykoptev/go-kit/metrics/mcpmw"
    tracemw "github.com/anatolykoptev/go-kit/tracing/mcpmw"
)

mcpserver.Run(server, mcpserver.Config{
    MCPReceivingMiddleware: []mcp.Middleware{
        hooks.Middleware(),
        metricsmw.Middleware(reg, "tool"),  // gowp_tool_calls_total etc
        tracemw.Middleware("go-wp"),         // span per tools/call
    },
})
```

## Endpoint configuration

Endpoint comes from one of (in order):

1. `tracing.WithEndpoint("...")` option — overrides everything
2. `OTEL_EXPORTER_OTLP_ENDPOINT` env var
3. unset → no exporter created, `Start` returns no-op spans (propagators
   still install so trace context flows through the process — matches the
   MemDB-go pattern of "skip the batcher when no collector is deployed")

Format: full URL with scheme — `http://host:port` (insecure) or
`https://host:port` (TLS). Matches the canonical OTel env-var spec.
Bare `host:port` is **NOT** supported — pass the scheme.

## Sampling

Default `1.0` (record all). Drop in prod with `WithSampleRatio(0.05)` for
high-traffic services. The sampler is `ParentBased(TraceIDRatioBased(r))` —
incoming sampled traces always continue, only roots get sampled.

## Compatibility

- Designed against `github.com/modelcontextprotocol/go-sdk v1.5+`.
- HTTP wrapper assumes Go 1.22+ ServeMux (uses `r.Pattern`); for chi /
  gorilla / echo, use `HandlerWithFormatter`.
- Wire format is OTLP/HTTP only. Add OTLP/gRPC if the collector layer
  needs it (one extra exporter, ~10 LOC).
