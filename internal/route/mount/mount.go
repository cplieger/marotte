// Package mount puts a binding's handler on an http.ServeMux the way the server serves it: a
// method-qualified pattern as is, a catch-all at its path with no filter, and a plain path behind a
// method filter. The registrar mounts through it after its table checks, and routetest mounts
// through it for a handler package's own tests, so both reach a handler by the same methods.
package mount

import (
	"net/http"
	"slices"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/route"
)

// On mounts b on mux. A plain-path binding with no method mounts nothing.
func On(mux *http.ServeMux, b route.Binding) {
	method, path := route.SplitPattern(b.Pattern)
	switch {
	case b.Any:
		mux.Handle(path, b.Handler)
	case method != "":
		mux.Handle(b.Pattern, b.Handler)
	case len(b.Methods) > 0:
		mux.Handle(path, methodFilter(b.Methods, b.Handler))
	}
}

// methodFilter answers every method ms does not bind 405 before h runs; a bound GET admits HEAD.
func methodFilter(ms []route.MethodTypes, h http.Handler) http.Handler {
	allow := make([]string, 0, len(ms)+1)
	for _, mt := range ms {
		allow = append(allow, mt.Method)
		if mt.Method == http.MethodGet {
			allow = append(allow, http.MethodHead)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(allow, r.Method) {
			httpreply.MethodNotAllowed(w, allow[0], allow[1:]...)
			return
		}
		h.ServeHTTP(w, r)
	})
}
