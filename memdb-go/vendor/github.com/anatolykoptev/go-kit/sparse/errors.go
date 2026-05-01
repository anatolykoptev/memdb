package sparse

import (
	"errors"
	"fmt"
)

// ErrModelNotConfigured is returned when the embed-server rejects a sparse
// request because the requested model is not loaded, or because no model
// name was supplied and zero or 2+ SPLADE models are configured.
//
// Surfaces the Rust handler's resolve_splade_name 400 case as a typed
// sentinel so callers can branch on configuration drift without parsing
// the error string.
var ErrModelNotConfigured = errors.New("sparse: splade model not configured on server")

// errHTTPStatus is a typed error carrying the HTTP status code and response
// body from a non-2xx response. Using a typed error (rather than a plain
// fmt.Errorf string) allows withRetry to type-assert the code for
// retryable-status filtering, and lets callers inspect Body/Code without
// regex-parsing the message.
//
// The Error() string follows embed/'s convention: "status <code>: <body>".
type errHTTPStatus struct {
	Code int
	Body string
}

// Error implements the error interface.
func (e *errHTTPStatus) Error() string {
	return fmt.Sprintf("status %d: %s", e.Code, e.Body)
}
