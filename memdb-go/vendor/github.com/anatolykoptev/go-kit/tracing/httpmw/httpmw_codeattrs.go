package httpmw

import (
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// codeAttrCache stores resolved code.* attributes keyed by "METHOD pattern".
// Populated lazily on first request per route; safe for concurrent access.
var codeAttrCache sync.Map // key: string → []attribute.KeyValue

// resolveCodeAttrs resolves OTEL code.* semantic-convention attributes for a
// handler function via runtime reflection. Resolution happens once per unique
// (method, pattern) pair; subsequent lookups hit the cache.
//
// Returns nil (not an error) when resolution is not possible (anonymous
// closures, funcN.func1 generated names, or non-ServeMux handlers).
func resolveCodeAttrs(method, pattern string, h http.Handler) []attribute.KeyValue {
	key := method + " " + pattern
	if cached, ok := codeAttrCache.Load(key); ok {
		return cached.([]attribute.KeyValue)
	}

	attrs := computeCodeAttrs(h)
	// Store even if nil — avoids re-computing for anonymous closures.
	codeAttrCache.Store(key, attrs)
	return attrs
}

// computeCodeAttrs does the actual reflection work. Returns nil on failure.
func computeCodeAttrs(h http.Handler) []attribute.KeyValue {
	if h == nil {
		return nil
	}
	pc := reflect.ValueOf(h).Pointer()
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return nil
	}
	fullName := fn.Name() // e.g. "github.com/x/y/pkg.Func"

	// Skip anonymous closures: they contain ".func" in the symbol name.
	if strings.Contains(fullName, ".func") {
		return nil
	}

	namespace, funcName := splitFuncName(fullName)
	if namespace == "" || funcName == "" {
		return nil
	}

	file, line := fn.FileLine(pc)

	return []attribute.KeyValue{
		attribute.String("code.namespace", namespace),
		attribute.String("code.function", funcName),
		attribute.String("code.filepath", file),
		attribute.Int("code.lineno", line),
	}
}

// splitFuncName splits a fully-qualified Go function name into (namespace, function).
//
// Examples:
//
//	"github.com/x/y/pkg.Func"         → ("github.com/x/y/pkg", "Func")
//	"github.com/x/y/pkg.(*T).Method"  → ("github.com/x/y/pkg", "(*T).Method")
//	"github.com/x/y/pkg.T.Method"     → ("github.com/x/y/pkg", "T.Method")
//
// The split point is the last '/' followed by the first '.' after it.
func splitFuncName(name string) (namespace, funcName string) {
	// Find last slash — package boundary.
	lastSlash := strings.LastIndex(name, "/")
	rest := name
	prefix := ""
	if lastSlash >= 0 {
		prefix = name[:lastSlash+1] // "github.com/x/y/"
		rest = name[lastSlash+1:]   // "pkg.Func" or "pkg.(*T).Method"
	}

	// First dot in 'rest' separates package name from identifier.
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return "", ""
	}

	namespace = prefix + rest[:dot] // "github.com/x/y/pkg"
	funcName = rest[dot+1:]         // "Func" or "(*T).Method"
	return namespace, funcName
}

// muxCodeAttrsMiddleware wraps an http.ServeMux to inject code.* attributes
// into the active span on each request. For non-ServeMux handlers, this is
// a no-op wrapper.
func muxCodeAttrsMiddleware(next http.Handler) http.Handler {
	mux, ok := next.(*http.ServeMux)
	if !ok {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		attrs := resolveCodeAttrs(r.Method, pattern, h)
		if len(attrs) > 0 {
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(attrs...)
		}
		next.ServeHTTP(w, r)
	})
}
