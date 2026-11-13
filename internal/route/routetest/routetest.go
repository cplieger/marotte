// Package routetest serves bindings on a plain http.ServeMux for a handler package's own tests, which
// reach each handler without the server's registrar and its endpoint table.
package routetest

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/route/mount"
)

// Mux mounts each binding as the server does, method filter included, with no table check.
type Mux struct {
	*http.ServeMux
}

// On returns a Mux over mux.
func On(mux *http.ServeMux) Mux { return Mux{ServeMux: mux} }

// Bind mounts b.
func (m Mux) Bind(b route.Binding) { mount.On(m.ServeMux, b) }
