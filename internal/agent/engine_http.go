package agent

import (
	"errors"
	"net/http"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
)

// EngineRestartAccepted is POST /api/engine/restart's 202 body, which carries no field.
type EngineRestartAccepted struct{}

// registerEngineRoutes binds the engine's status, its confirmed restart and the configuration standing.
func (rt *Runtime) registerEngineRoutes(m routeMux) {
	m.Bind(route.JSON[struct{}, marotte.EngineStatus]("GET /api/engine", http.HandlerFunc(rt.handleEngineStatus)))
	m.Bind(route.WithStatus(route.JSON[struct{}, EngineRestartAccepted]("POST /api/engine/restart",
		rt.lifecycle.admitUnlessDraining(http.HandlerFunc(rt.handleEngineRestart))), http.StatusAccepted))
	m.Bind(route.JSON[struct{}, marotte.ConfigStandingResponse]("GET /api/config/standing", http.HandlerFunc(rt.standing.handle)))
}

func (rt *Runtime) handleEngineStatus(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, rt.engine.status())
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
		httpserver.WriteJSONStatus(w, http.StatusAccepted, EngineRestartAccepted{})
	}
}
