// Runtime profiles, behind the loopback gate. Mounted explicitly: the blank import registers
// on http.DefaultServeMux, which this process never serves. pprof.Index serves every named
// profile (goroutineleak included) from the path, so it must sit at the literal
// /debug/pprof/ prefix. CPU and Trace are not mounted: each holds the server for a
// caller-controlled sample window. Cmdline and Symbol are not needed.

package server

import (
	"net/http"
	// A normal import (see the file comment): the handlers are reached only through
	// loopbackOnly, and DefaultServeMux, where the init registers, is never served.
	"net/http/pprof"

	"github.com/cplieger/marotte/internal/httpreply"
)

// pprofPath is the subtree pattern. The trailing slash is load-bearing: ServeMux needs it to
// match a subtree, and pprof.Index trims exactly this prefix to derive a profile name.
const pprofPath = "/debug/pprof/"

// pprofSurface is what a refused caller is told declined the request (this endpoint, not
// the repair hook sharing the middleware).
const pprofSurface = "the runtime profile endpoint"

// pprofHandler returns the gated profile handler. A goroutine dump and heap profile are a map
// of the process, hence the repair hook's gate.
func pprofHandler() http.Handler {
	return loopbackOnly(pprofSurface, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// pprof.Index answers any method, so the gate is ours, inside the loopback wrapper and not
		// on the pattern (whose mismatch falls through to the SPA mount).
		if !httpreply.RequireMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		pprof.Index(w, r)
	}))
}
