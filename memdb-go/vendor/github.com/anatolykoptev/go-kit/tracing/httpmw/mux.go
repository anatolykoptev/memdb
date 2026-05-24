package httpmw

import (
	"net/http"
	"reflect"
	"strings"
)

// Mux is a drop-in wrapper for *http.ServeMux that auto-registers each
// pattern's handler symbol into the route registry, eliminating manual
// RegisterRoute calls.
//
// Use as a drop-in replacement:
//
//	mux := httpmw.NewServeMux()         // instead of http.NewServeMux()
//	mux.Handle("POST /webhook", handler)
//	mux.HandleFunc("GET /metrics", h)
//
// For http.Handler values (struct pointers implementing ServeHTTP), the
// wrapper resolves the method expression via reflect.Type.MethodByName so
// that code.function resolves to "(*T).ServeHTTP" rather than the
// compiler-generated "-fm" wrapper. Closure handlers and HandleFunc
// entries resolve via the usual FuncForPC path (may produce obfuscated names).
type Mux struct {
	*http.ServeMux
}

// NewServeMux returns a Mux wrapping a fresh http.ServeMux.
func NewServeMux() *Mux {
	return &Mux{ServeMux: http.NewServeMux()}
}

// Handle registers h for the given pattern, auto-registering code.* attrs
// in the route registry. For http.Handler values the ServeHTTP method
// expression is used so reflect resolves the real function name.
func (m *Mux) Handle(pattern string, h http.Handler) {
	method, path := splitMethodPattern(pattern)
	registerHandler(method, path, h)
	m.ServeMux.Handle(pattern, h)
}

// HandleFunc registers f for the given pattern, auto-registering code.* attrs
// in the route registry via RegisterRoute.
func (m *Mux) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	method, path := splitMethodPattern(pattern)
	RegisterRoute(method, path, f)
	m.ServeMux.HandleFunc(pattern, f)
}

// splitMethodPattern splits a Go 1.22+ ServeMux pattern like "POST /path"
// into ("POST", "/path"). A pattern without a leading method token like
// "/health" returns ("", "/health").
func splitMethodPattern(pattern string) (method, path string) {
	pattern = strings.TrimSpace(pattern)
	if idx := strings.IndexByte(pattern, ' '); idx > 0 {
		tok := pattern[:idx]
		// A method token contains only uppercase ASCII letters.
		if isUpperAlpha(tok) {
			return tok, strings.TrimSpace(pattern[idx+1:])
		}
	}
	return "", pattern
}

func isUpperAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// registerHandler registers code.* attrs for an http.Handler.
// If h's concrete type has a ServeHTTP method, the method expression PC is
// used (via reflect.Type.MethodByName) so that FuncForPC resolves to the
// real named function instead of the compiler-generated -fm wrapper.
// Falls back to RegisterRoute for non-method-expression paths.
func registerHandler(method, path string, h http.Handler) {
	t := reflect.TypeOf(h)
	if t == nil {
		return
	}
	m, ok := t.MethodByName("ServeHTTP")
	if !ok {
		// Try pointer type.
		if t.Kind() != reflect.Ptr {
			m, ok = reflect.PtrTo(t).MethodByName("ServeHTTP")
		}
	}
	if ok && m.Func.IsValid() {
		// m.Func is the method expression (same as (*T).ServeHTTP), not a
		// method value bound to a receiver — FuncForPC returns the real location.
		RegisterRoute(method, path, m.Func.Interface())
		return
	}
	// Fallback: try as a plain func (e.g. http.HandlerFunc).
	RegisterRoute(method, path, h)
}
