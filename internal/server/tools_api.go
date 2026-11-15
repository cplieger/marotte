package server

import (
	"net/http"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/toolbelt/v3/httpapi"
)

const toolsAPIPrefix = "/api/tools"

// errCodeToolsUnavailable marks an /api/tools answer while the tools engine is down, so the
// client shows the reason instead of retrying.
const errCodeToolsUnavailable = "tools_unavailable"

// toolsSource is the tools engine as the composition root holds it: Engine returns the live
// engine, or nil and why it is down, worded for the reader.
type toolsSource interface {
	Engine() (*toolbelt.Engine, error)
}

// toolsAPI serves toolbelt's routes from whichever engine its source holds at the request, so an
// engine that comes up after boot is served without a restart.
type toolsAPI struct {
	src toolsSource
}

// register binds each of toolbelt's routes under toolsAPIPrefix with the types toolbelt declares.
func (a *toolsAPI) register(m routeMux) {
	routes := httpapi.Routes()
	for i := range routes {
		rt := &routes[i]
		m.Bind(route.Binding{
			Pattern: rt.Method + " " + toolsAPIPrefix + rt.Suffix,
			Handler: a.serve(rt),
			Kind:    route.KindJSON,
			Methods: []route.MethodTypes{ToolsMethodTypes(rt)},
		})
	}
}

// ToolsMethodTypes is rt's types as a binding and its entry carry them.
func ToolsMethodTypes(rt *httpapi.Route) route.MethodTypes {
	status := rt.Status
	if status == http.StatusOK {
		status = 0
	}
	return route.MethodTypes{
		Method: rt.Method, Params: rt.Params, Request: rt.Request, Query: rt.Query, Response: rt.Response, Status: status,
	}
}

func (a *toolsAPI) serve(rt *httpapi.Route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engine, down := a.src.Engine()
		if engine == nil {
			// httpapi's own policy, kept on the refusal too: this answer changes without a deploy.
			w.Header().Set("Cache-Control", "no-store")
			httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable,
				httpreply.ErrorJSONWithCode(down.Error(), errCodeToolsUnavailable))
			return
		}
		rt.Handler(engine).ServeHTTP(w, r)
	})
}
