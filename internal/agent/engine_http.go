package agent

import (
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// registerEngineRoutes mounts the engine's status, its confirmed restart and the configuration standing.
func (rt *Runtime) registerEngineRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/engine", rt.handleEngineStatus)
	mux.Handle("POST /api/engine/restart", rt.lifecycle.admitUnlessDraining(http.HandlerFunc(rt.handleEngineRestart)))
	mux.HandleFunc("GET /api/config/standing", rt.standing.handle)
}

func (rt *Runtime) handleEngineStatus(w http.ResponseWriter, _ *http.Request) {
	webhttp.WriteJSON(w, rt.engine.status())
}

// handleEngineRestart admits a confirmed restart and answers 202; the restart runs under the runtime's lifetime, so
// a browser that leaves after the answer cannot stop it halfway through a reap. 409 while one runs.
func (rt *Runtime) handleEngineRestart(w http.ResponseWriter, _ *http.Request) {
	st := rt.engine.status()
	if !rt.engine.requestRestart(restartCause{Kind: restartConfirmed, Reasons: st.Restart.Reasons}) {
		httpreply.Conflict(w, "The agent engine is already restarting.")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
