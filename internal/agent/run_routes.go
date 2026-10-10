package agent

import "net/http"

// runRoutes is the HTTP adapter over the run lifecycle: /api/runs, /api/recipes and schedule CRUD, holding
// its subject and the hub epoch for the live-runs envelope.
type runRoutes struct {
	runs  *Runs
	epoch func() string
}

// register mounts every run and schedule endpoint.
func (rr *runRoutes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/{id}", rr.handleRun)
	mux.HandleFunc("GET /api/runs/{id}/controls", rr.handleControls)
	mux.HandleFunc("GET /api/runs/live", rr.handleLiveRuns)
	// {path} is one percent-encoded workflow.PathKey: ServeMux splits the ESCAPED path, so a node id's "/"
	// travels as %2F inside the segment.
	mux.HandleFunc("GET /api/runs/{id}/steps/{path}", rr.handleStepTranscript)
	mux.HandleFunc("GET /api/runs/{id}/turns/{turn}", rr.handleTurnRange)
	mux.HandleFunc("POST /api/runs", rr.handleLaunch)
	mux.HandleFunc("POST /api/runs/{id}/cancel", rr.handleCancel)
	mux.HandleFunc("POST /api/runs/{id}/pause", rr.handlePause)
	mux.HandleFunc("POST /api/runs/{id}/resume", rr.handleResume)
	mux.HandleFunc("POST /api/runs/{id}/retry", rr.handleRetry)
	mux.HandleFunc("DELETE /api/runs/{id}", rr.handleDelete)
	mux.HandleFunc("POST /api/runs/{id}/step", rr.handleStepStatus)
	mux.HandleFunc("POST /api/runs/{id}/extend", rr.handleExtend)
	mux.HandleFunc("POST /api/runs/{id}/finish-loop", rr.handleFinishLoop)
	mux.HandleFunc("POST /api/runs/{id}/answer", rr.handleAnswer)
	mux.HandleFunc("GET /api/recipes", rr.handleRecipes)
	rr.registerSchedule(mux)
}
