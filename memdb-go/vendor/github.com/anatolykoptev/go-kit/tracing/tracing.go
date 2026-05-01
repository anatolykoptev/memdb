// Package tracing wires OpenTelemetry distributed tracing for go-* services.
//
// Scope: ONE-CALL bootstrap (Setup) + a thin Start helper. HTTP, gRPC, and SQL
// are covered by the canonical contrib packages — use them directly:
//
//	import "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
//	mux = otelhttp.NewHandler(mux, "service-name")
//
//	import "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
//	import "github.com/XSAM/otelsql"
//
// What we DO ship in subpackages:
//   - tracing/httpmw — thin path-label wrapper over otelhttp (chi + stdlib mux).
//   - tracing/mcpmw  — span-per-tool-call middleware for MCP servers
//     (no canonical equivalent exists upstream).
//
// Wire format: OTLP/HTTP (port 4318). Endpoint comes from
// OTEL_EXPORTER_OTLP_ENDPOINT or WithEndpoint(...). When neither is set,
// Setup installs propagators only and returns a no-op shutdown — Start still
// works, spans go nowhere. Mirrors MemDB-go convention (cmd/server/main.go:
// "Skipping the batcher avoids periodic 'connection refused' warnings when
// no collector is deployed").
package tracing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// envEndpoint matches the OTel SDK convention; "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
// is also accepted by the official SDK and takes precedence — we keep the simpler
// single-endpoint env for symmetry with the rest of the fleet.
const envEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

// ShutdownFunc flushes pending spans and tears down the provider.
// Defer it from main on SIGTERM/SIGINT. Safe when Setup returned a no-op.
type ShutdownFunc func(context.Context) error

// Option configures Setup.
type Option func(*config)

type config struct {
	endpoint     string
	sampleRatio  float64
	batchTimeout time.Duration
	attrs        []attribute.KeyValue
}

// WithEndpoint overrides OTEL_EXPORTER_OTLP_ENDPOINT. Accepts "host:port",
// "http://host:port", or "https://host:port".
func WithEndpoint(url string) Option { return func(c *config) { c.endpoint = url } }

// WithSampleRatio sets head-sampling probability. 1.0 = sample all (default).
// Drop to 0.05–0.1 in high-traffic prod when storage cost matters.
func WithSampleRatio(r float64) Option { return func(c *config) { c.sampleRatio = r } }

// WithBatchTimeout overrides span-batch flush interval (default 5s).
func WithBatchTimeout(d time.Duration) Option { return func(c *config) { c.batchTimeout = d } }

// WithAttributes attaches resource attributes to every span. service.name is
// set automatically from the serviceName arg of Setup.
func WithAttributes(kv ...attribute.KeyValue) Option {
	return func(c *config) { c.attrs = append(c.attrs, kv...) }
}

// Setup configures a global TracerProvider for the service.
//
// Always installs W3C traceparent + baggage propagators so distributed trace
// context flows in and out of this process even when local export is off.
// When the endpoint is unset, no exporter is created and the provider stays
// the default no-op — Start still returns a usable but inert span.
func Setup(ctx context.Context, serviceName string, opts ...Option) (ShutdownFunc, error) {
	if serviceName == "" {
		return noopShutdown, errors.New("tracing.Setup: serviceName must be non-empty")
	}

	cfg := config{sampleRatio: 1.0, batchTimeout: 5 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.endpoint == "" {
		cfg.endpoint = os.Getenv(envEndpoint)
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if cfg.endpoint == "" {
		return noopShutdown, nil
	}

	// WithEndpointURL accepts the canonical OTel env-var format — full URL
	// like "http://jaeger:4318" or "https://otel.example:4318". Scheme drives
	// TLS automatically (http=insecure, https=TLS). The legacy
	// WithEndpoint(host:port) takes bare host and double-prefixes if you
	// pass it a URL — caused parse errors in the fleet (memdb-go logs:
	// `traces export: parse "http://http://jaeger:4318/v1/traces"`).
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.endpoint),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("create OTLP HTTP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(append(
			[]attribute.KeyValue{semconv.ServiceName(serviceName)},
			cfg.attrs...,
		)...),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(cfg.batchTimeout)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(cfg.sampleRatio),
		)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns a Tracer scoped by name. Convention: pass the service name
// or "<service>/<subsystem>" for finer slicing in Tempo/Jaeger UIs.
//
// Always safe — falls through to the global provider, no-op when Setup did
// not initialise an exporter.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// Start opens a span in ctx using the global tracer. Pass attributes as
// optional args; the returned context carries the new span — use it for
// downstream calls so child spans nest correctly. Always defer span.End().
//
// Convenience over the verbose otel.Tracer(...).Start(...) pattern.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx, span := otel.Tracer("go-kit/tracing").Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	return ctx, span
}

// RecordError marks the current span as errored. Convenience over the two
// canonical calls (RecordError + SetStatus) — common boilerplate in handlers.
//
// Pass the error returned by the operation; nil is a no-op.
func RecordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func noopShutdown(context.Context) error { return nil }
