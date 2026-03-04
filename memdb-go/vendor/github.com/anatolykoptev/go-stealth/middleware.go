package stealth

import (
	"io"
)

// Request represents an outgoing HTTP request for the middleware chain.
type Request struct {
	Method      string
	URL         string
	Headers     map[string]string
	Body        io.Reader
	HeaderOrder []string // backend uses this for header ordering
}

// Response represents an HTTP response from the middleware chain.
type Response struct {
	Body       []byte
	Headers    map[string]string
	StatusCode int
}

// Handler processes a Request and returns a Response.
type Handler func(req *Request) (*Response, error)

// Middleware wraps a Handler to add behavior before/after request execution.
type Middleware func(next Handler) Handler

// Chain composes multiple middlewares into a single Middleware.
// Middlewares are applied in order: first middleware wraps the outermost layer.
//
//	Chain(logging, retry, rateLimit)(handler)
//	// execution order: logging → retry → rateLimit → handler
func Chain(middlewares ...Middleware) Middleware {
	return func(final Handler) Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
