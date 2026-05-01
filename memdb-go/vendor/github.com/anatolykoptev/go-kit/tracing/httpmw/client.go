// HTTP client tracing — outgoing-request side.
//
// otelhttp.NewTransport is the canonical way to instrument an http.Client
// with OTel: it wraps the RoundTripper to create a client span per request,
// inject W3C traceparent into headers, and emit standard semconv metrics.
//
// Without it, distributed traces BREAK at every cross-service hop — the
// callee receives no traceparent, can't link its server span to the caller.
//
// This file ships two convenience constructors so wiring is one line in
// each service that makes outgoing calls:
//
//	httpClient := tracing/httpmw.Client()
//	resp, err := httpClient.Get("http://go-search:8890/research?q=x")
//	// ↑ creates a client span "HTTP GET" + injects traceparent header

package httpmw

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Client returns an *http.Client whose transport is wrapped with otelhttp.
// Suitable as a fleet-wide default for outgoing calls.
//
// Wraps http.DefaultTransport — keeps connection pooling, default timeouts,
// and any tweaks applied via http.DefaultTransport.* downstream.
//
// Override the transport when you need a custom one:
//
//	httpClient := &http.Client{
//	    Transport: tracing/httpmw.WrapTransport(myProxiedTransport),
//	}
func Client() *http.Client {
	return &http.Client{Transport: WrapTransport(http.DefaultTransport)}
}

// WrapTransport wraps any http.RoundTripper with otelhttp instrumentation.
// Use when you have a custom Transport (proxy, retry, rate-limit) and want
// to keep it but add OTel client spans + traceparent injection on top.
//
// Idempotent — calling on an already-wrapped Transport is fine but
// produces nested spans, so don't double-wrap.
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}
