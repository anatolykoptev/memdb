package httpmw

import (
	"reflect"
	"runtime"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
)

// codeAttrs holds precomputed OTEL semantic-convention code.* attributes
// for a single registered route.
type codeAttrs struct {
	attrs []attribute.KeyValue
}

var (
	routeRegistryMu sync.RWMutex
	routeRegistry   = map[string]codeAttrs{}
)

// RegisterRoute associates an HTTP route (method+pattern) with a named
// handler function so its OTEL semantic-convention code.* attributes can
// be emitted on each request span. Call once at startup per route you
// want Tier-1-resolvable in trace exports. Closures and method-bound
// funcs may resolve to obfuscated names — pass package-level named funcs
// where possible.
//
// Safe to call concurrently. Idempotent for the same (method, pattern):
// last call wins.
func RegisterRoute(method, pattern string, fn any) {
	if fn == nil {
		return
	}
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func {
		return
	}
	pc := v.Pointer()
	if pc == 0 {
		return
	}

	attrs := computeRegistryAttrs(pc)
	key := method + " " + pattern

	routeRegistryMu.Lock()
	routeRegistry[key] = codeAttrs{attrs: attrs}
	routeRegistryMu.Unlock()
}

// LookupRoute returns the precomputed code.* attributes for the given
// method+pattern, or nil if not registered. Used internally by Handler
// and exposed for testing.
func LookupRoute(method, pattern string) []attribute.KeyValue {
	key := method + " " + pattern
	routeRegistryMu.RLock()
	ca, ok := routeRegistry[key]
	routeRegistryMu.RUnlock()
	if !ok {
		return nil
	}
	return ca.attrs
}

// computeRegistryAttrs resolves OTEL code.* attributes from a function PC.
// Returns nil when resolution is not meaningful (anonymous closure, etc.);
// the caller stores nil as a sentinel to avoid re-lookup.
func computeRegistryAttrs(pc uintptr) []attribute.KeyValue {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return nil
	}
	fullName := fn.Name()

	namespace, funcName := splitRegistryFuncName(fullName)
	if namespace == "" || funcName == "" {
		return nil
	}

	file, line := fn.FileLine(fn.Entry())

	return []attribute.KeyValue{
		attribute.String("code.namespace", namespace),
		attribute.String("code.function", funcName),
		attribute.String("code.filepath", file),
		attribute.Int("code.lineno", line),
	}
}

// splitRegistryFuncName splits a fully-qualified Go function name into
// (namespace, function). Handles package-level funcs, methods, and
// method-value wrappers (suffix -fm).
//
// Examples:
//
//	"github.com/x/y/pkg.Func"          -> ("github.com/x/y/pkg", "Func")
//	"github.com/x/y/pkg.(*T).ServeHTTP" -> ("github.com/x/y/pkg", "(*T).ServeHTTP")
//	"github.com/x/y/pkg.(*T).ServeHTTP-fm" -> same, stripped -fm
func splitRegistryFuncName(name string) (namespace, funcName string) {
	// Strip method-value suffix.
	name = strings.TrimSuffix(name, "-fm")

	lastSlash := strings.LastIndex(name, "/")
	rest := name
	prefix := ""
	if lastSlash >= 0 {
		prefix = name[:lastSlash+1]
		rest = name[lastSlash+1:]
	}

	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return "", ""
	}

	namespace = prefix + rest[:dot]
	funcName = rest[dot+1:]
	return namespace, funcName
}

// RegisterGinRoute registers an HTTP route using a handler name string
// that gin provides via engine.Routes()[i].HandlerFunc. gin already resolves
// handler names at startup via runtime reflection; instead of re-doing the
// reflect.ValueOf dance we accept the string directly and inject it into
// the registry, bypassing FuncForPC.
//
// The handlerName is the value gin puts in RouteInfo.HandlerFunc, e.g.:
//
//	"github.com/myorg/app/handlers.(*API).GetUser-fm"
//
// The -fm suffix (method-value wrapper) is stripped so code.function resolves
// to the real method name. An empty handlerName is a no-op.
//
// Usage:
//
//	for _, r := range engine.Routes() {
//	    httpmw.RegisterGinRoute(r.Method, r.Path, r.HandlerFunc)
//	}
func RegisterGinRoute(method, pattern, handlerName string) {
	if handlerName == "" {
		return
	}
	namespace, funcName := splitRegistryFuncName(handlerName)
	if namespace == "" || funcName == "" {
		return
	}
	key := method + " " + pattern
	attrs := []attribute.KeyValue{
		attribute.String("code.namespace", namespace),
		attribute.String("code.function", funcName),
	}

	routeRegistryMu.Lock()
	routeRegistry[key] = codeAttrs{attrs: attrs}
	routeRegistryMu.Unlock()
}
