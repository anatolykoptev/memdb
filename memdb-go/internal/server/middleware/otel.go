package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName         = "memdb-go"
	errorStatusMinCode = http.StatusBadRequest // 400: track as error for OTel metrics
)

// routePath returns the matched Go 1.22 ServeMux pattern, or "unmatched".
// Using the pattern instead of r.URL.Path caps label cardinality on routes
// with variable segments (e.g. /product/get_memory/{memory_id}).
func routePath(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

// OTelMetrics holds the metric instruments for request tracking.
type OTelMetrics struct {
	RequestCount   metric.Int64Counter
	RequestLatency metric.Float64Histogram
	ErrorCount     metric.Int64Counter
	CacheHitCount  metric.Int64Counter
}

// NewOTelMetrics creates metric instruments from the global meter provider.
func NewOTelMetrics() *OTelMetrics {
	meter := otel.Meter(tracerName)

	reqCount, _ := meter.Int64Counter("memdb.http.requests",
		metric.WithDescription("Total HTTP requests"),
	)
	reqLatency, _ := meter.Float64Histogram("memdb.http.latency",
		metric.WithDescription("HTTP request latency in milliseconds"),
		metric.WithUnit("ms"),
	)
	errCount, _ := meter.Int64Counter("memdb.http.errors",
		metric.WithDescription("Total HTTP errors (4xx/5xx)"),
	)
	cacheHits, _ := meter.Int64Counter("memdb.cache.hits",
		metric.WithDescription("Cache hit count"),
	)

	return &OTelMetrics{
		RequestCount:   reqCount,
		RequestLatency: reqLatency,
		ErrorCount:     errCount,
		CacheHitCount:  cacheHits,
	}
}

// OTel returns OpenTelemetry tracing and metrics middleware.
// When enabled, wraps each request with otelhttp for automatic span creation,
// trace context propagation (W3C traceparent), and metric recording.
func OTel(logger *slog.Logger, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !enabled {
			return next // no-op when disabled
		}

		// Set up W3C trace context propagation
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))

		metrics := NewOTelMetrics()
		tracer := otel.Tracer(tracerName)

		// Wrap with otelhttp for automatic HTTP span instrumentation
		otelHandler := otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Add custom span attributes
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String("memdb.path", r.URL.Path),
				attribute.String("memdb.method", r.Method),
			)

			path := routePath(r)

			// Record request metric
			metrics.RequestCount.Add(r.Context(), 1,
				metric.WithAttributes(
					attribute.String("method", r.Method),
					attribute.String("path", path),
				),
			)

			// Track cache hits from response header
			rec := &otelResponseWriter{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			elapsed := float64(time.Since(start).Milliseconds())
			metrics.RequestLatency.Record(r.Context(), elapsed,
				metric.WithAttributes(
					attribute.String("method", r.Method),
					attribute.String("path", path),
				),
			)

			if rec.statusCode >= errorStatusMinCode {
				metrics.ErrorCount.Add(r.Context(), 1,
					metric.WithAttributes(
						attribute.String("method", r.Method),
						attribute.String("path", path),
						attribute.Int("status", rec.statusCode),
					),
				)
			}

			if w.Header().Get("X-Cache") == "HIT" {
				metrics.CacheHitCount.Add(r.Context(), 1)
			}
		}), "memdb-go")

		_ = tracer // tracer is available via otel.Tracer() for child spans
		_ = logger

		return otelHandler
	}
}

// otelResponseWriter captures status code for metric recording.
type otelResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *otelResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *otelResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
