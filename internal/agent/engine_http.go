package agent

import (
	"errors"
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v4"
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

// handleEngineRestart admits a confirmed restart (engineOwner.confirmRestart) and answers 202; the restart runs under
// the runtime's lifetime, so a browser that leaves after the answer cannot stop it halfway through a reap. 409 while
// one runs, or while config.json cannot be read and reasons wait.
func (rt *Runtime) handleEngineRestart(w http.ResponseWriter, r *http.Request) {
	switch err := rt.engine.confirmRestart(r.Context()); {
	case errors.Is(err, errRestartHeld):
		httpreply.Conflict(w, "config.json cannot be read, so what waits for the agent engine's restart cannot apply yet. "+
			"Fix the file, then choose Restart now again.")
	case err != nil:
		httpreply.Conflict(w, "The agent engine is already restarting.")
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}
