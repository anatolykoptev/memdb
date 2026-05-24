package httpmw

import "net/http"

// WalkAndRegister registers routes from a router that supports a Walk-style
// callback. The caller wires this to the router's Walk method so this package
// does not need to import the router directly.
//
// # chi
//
//	import "github.com/go-chi/chi/v5"
//
//	err := httpmw.WalkAndRegister(func(reg func(method, pattern string, h http.Handler)) error {
//	    return chi.Walk(router, func(method, route string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
//	        reg(method, route, h)
//	        return nil
//	    })
//	})
//
// # gorilla/mux
//
//	import "github.com/gorilla/mux"
//
//	err := httpmw.WalkAndRegister(func(reg func(method, pattern string, h http.Handler)) error {
//	    return router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
//	        methods, err := route.GetMethods()
//	        if err != nil {
//	            return nil // skip routes without explicit methods
//	        }
//	        pattern, err := route.GetPathTemplate()
//	        if err != nil {
//	            return nil
//	        }
//	        for _, method := range methods {
//	            reg(method, pattern, route.GetHandler())
//	        }
//	        return nil
//	    })
//	})
//
// # gin
//
// gin exposes handler names as strings via engine.Routes()[i].HandlerFunc,
// so use RegisterGinRoute instead — no reflection needed:
//
//	for _, r := range engine.Routes() {
//	    httpmw.RegisterGinRoute(r.Method, r.Path, r.HandlerFunc)
//	}
//
// Call after all routes are mounted so the registry captures every route.
func WalkAndRegister(walk func(register func(method, pattern string, h http.Handler)) error) error {
	return walk(func(method, pattern string, h http.Handler) {
		registerHandler(method, pattern, h)
	})
}
