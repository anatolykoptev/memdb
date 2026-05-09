// Package httpmw is a thin convenience layer over
// go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp.
//
// otelhttp.NewHandler is the canonical way to instrument an HTTP server with
// OTel — use it directly when no special path-extraction is needed:
//
//	import "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
//	srv.Handler = otelhttp.NewHandler(mux, "service-name")
//
// This package adds a single feature on top: route-pattern extraction for
// span names so Tempo/Jaeger UIs group traces by route, not by raw URL path
// (which has unbounded cardinality with path params). Two extractors are
// provided: stdlib (Go 1.22+ ServeMux .Pattern) and chi (RouteContext).
//
// If your service routes through a different framework (gorilla/mux, echo,
// gin), pass your own extractor via WithSpanNameFormatter — see
// otelhttp.WithSpanNameFormatter docs.
package httpmw

import (
	"fmt"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Handler wraps next with otelhttp instrumentation, naming each span
// "<method> <pattern>" using the stdlib net/http ServeMux pattern extractor.
//
// `service` becomes the otelhttp operation prefix; pass the service name.
// Falls back to "<method> unmatched" when r.Pattern is empty (no matching
// route — pre-routing handlers, raw 404s, etc.).
//
// When next is a *http.ServeMux, Handler also emits OTEL semantic-convention
// code.* attributes (code.namespace, code.function, code.filepath,
// code.lineno) on each request span, resolved from the registered handler
// via runtime reflection at first-request time. Anonymous closures are
// silently skipped. Non-ServeMux handlers are wrapped without code.* attrs.
func Handler(service string, next http.Handler) http.Handler {
	instrumented := muxCodeAttrsMiddleware(next)
	return otelhttp.NewHandler(instrumented, service,
		otelhttp.WithSpanNameFormatter(stdlibFormatter),
	)
}

// HandlerWithFormatter is the escape hatch when stdlib pattern extraction
// is not enough (chi, gorilla, gin). Pass a function that extracts the
// matched route pattern from the request.
//
// Example for chi:
//
//	import "github.com/go-chi/chi/v5"
//
//	mux = httpmw.HandlerWithFormatter("go-search", mux,
//	    func(_ string, r *http.Request) string {
//	        if rc := chi.RouteContext(r.Context()); rc != nil {
//	            return r.Method + " " + rc.RoutePattern()
//	        }
//	        return r.Method + " unmatched"
//	    })
func HandlerWithFormatter(service string, next http.Handler, fn func(string, *http.Request) string) http.Handler {
	return otelhttp.NewHandler(next, service, otelhttp.WithSpanNameFormatter(fn))
}

// stdlibFormatter names spans "<method> <pattern>" using Go 1.22 ServeMux
// route patterns. Bounds cardinality on routes with path variables.
//
// Go 1.22 ServeMux supports two registration styles:
//
//   - method-qualified: mux.HandleFunc("POST /webhook", h) — r.Pattern
//     comes back as "POST /webhook" (already includes the method)
//   - bare:             mux.HandleFunc("/webhook", h)      — r.Pattern
//     comes back as "/webhook" (no method)
//
// The formatter handles both: if the pattern already begins with the
// request method followed by a space, it is used verbatim; otherwise the
// method is prepended. This produces "POST /webhook" in both cases — no
// duplicated method, stable cardinality.
func stdlibFormatter(_ string, r *http.Request) string {
	pattern := r.Pattern
	if pattern == "" {
		return fmt.Sprintf("%s unmatched", r.Method)
	}
	if methodPrefix := r.Method + " "; strings.HasPrefix(pattern, methodPrefix) {
		return pattern
	}
	return fmt.Sprintf("%s %s", r.Method, pattern)
}
