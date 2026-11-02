package agent

import "net/http"

// runRoutes is the HTTP adapter over the run lifecycle: /api/runs, /api/recipes and schedule CRUD, holding
// its subject and the hub epoch for the live-runs envelope.
type runRoutes struct {
	runs  *Runs
	epoch func() string
}

// register admits every write through the drain gate (lifetime.admitUnlessDraining); the reads stay up.
func (rr *runRoutes) register(mux *http.ServeMux) {
	write := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, rr.runs.lifecycle.admitUnlessDraining(h))
	}
	mux.HandleFunc("GET /api/runs/{id}", rr.handleRun)
	mux.HandleFunc("GET /api/runs/{id}/controls", rr.handleControls)
	mux.HandleFunc("GET /api/runs/live", rr.handleLiveRuns)
	// {path} is one percent-encoded workflow.PathKey: ServeMux splits the ESCAPED path, so a node id's "/"
	// travels as %2F inside the segment.
	mux.HandleFunc("GET /api/runs/{id}/steps/{path}", rr.handleStepTranscript)
	mux.HandleFunc("GET /api/runs/{id}/turns/{turn}", rr.handleTurnRange)
	write("POST /api/runs", rr.handleLaunch)
	write("POST /api/runs/{id}/cancel", rr.handleCancel)
	write("POST /api/runs/{id}/pause", rr.handlePause)
	write("POST /api/runs/{id}/resume", rr.handleResume)
	write("POST /api/runs/{id}/retry", rr.handleRetry)
	write("DELETE /api/runs/{id}", rr.handleDelete)
	write("POST /api/runs/{id}/extend", rr.handleExtend)
	write("POST /api/runs/{id}/finish-loop", rr.handleFinishLoop)
	write("POST /api/runs/{id}/answer", rr.handleAnswer)
	write("POST /api/runs/{id}/steps/{path}/{verb}", rr.handleStepPost)
	mux.HandleFunc("GET /api/recipes", rr.handleRecipes)
	rr.registerSchedule(mux, write)
}
