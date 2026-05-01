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

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Handler wraps next with otelhttp instrumentation, naming each span
// "<method> <pattern>" using the stdlib net/http ServeMux pattern extractor.
//
// `service` becomes the otelhttp operation prefix; pass the service name.
// Falls back to "<method> unmatched" when r.Pattern is empty (no matching
// route — pre-routing handlers, raw 404s, etc.).
func Handler(service string, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, service,
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
func stdlibFormatter(_ string, r *http.Request) string {
	pattern := r.Pattern
	if pattern == "" {
		pattern = "unmatched"
	}
	return fmt.Sprintf("%s %s", r.Method, pattern)
}
