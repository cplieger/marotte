package server

import (
	"net/http"
	"sync"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/toolbelt/v3/httpapi"
	"github.com/cplieger/webhttp/v3"
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

// toolsAPI serves /api/tools from whichever engine its source holds at the request, so an
// engine that comes up after boot is served without a restart.
type toolsAPI struct {
	src     toolsSource
	engine  *toolbelt.Engine
	handler http.Handler
	mu      sync.Mutex
}

func (a *toolsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	engine, down := a.src.Engine()
	if engine == nil {
		// httpapi's own policy, kept on the refusal too: this answer changes without a deploy.
		w.Header().Set("Cache-Control", "no-store")
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSONWithCode(down.Error(), errCodeToolsUnavailable))
		return
	}
	a.handlerFor(engine).ServeHTTP(w, r)
}

func (a *toolsAPI) handlerFor(engine *toolbelt.Engine) http.Handler {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine != engine {
		a.engine, a.handler = engine, httpapi.Handler(engine, toolsAPIPrefix)
	}
	return a.handler
}
