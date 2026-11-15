package agent

import (
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
)

// routeMux is what a route registration needs: the server's registrar, or a test's mux. An alias, so
// RegisterRoutes has the server's routeHandler signature.
type routeMux = interface{ Bind(route.Binding) }

// runRoutes is the HTTP adapter over the run lifecycle: /api/runs, /api/recipes and /api/launch-rules, holding
// its subject and the hub epoch for the live-runs envelope.
type runRoutes struct {
	runs  *Runs
	epoch func() string
}

// RunTurnRange is GET /api/runs/{id}/turns/{turn}'s body. The three lists travel as `[]`: a null
// fails the client's array decode and loses the page.
type RunTurnRange struct {
	Entries     []marotte.Entry        `json:"entries"`
	OpenEntries []marotte.OpenEntry    `json:"open_entries"`
	Subject     []marotte.SubjectStamp `json:"subject"`
}

// RunQuery is GET /api/runs/{id}'s query: View "header" or "glance" reads that form (it wins over
// Since), and Since a revision answers only the changes after it.
type RunQuery struct {
	View  string `json:"view,omitempty"`
	Since string `json:"since,omitempty"`
}

// RunTurnQuery is GET /api/runs/{id}/turns/{turn}'s query: the exclusive sequence to read after.
type RunTurnQuery struct {
	After string `json:"after,omitempty"`
}

// RunStepSteerRemoved is POST /api/runs/{id}/steps/{path}/steer-remove's body.
type RunStepSteerRemoved struct {
	Deleted string `json:"deleted"`
}

// register admits every write through the drain gate (lifetime.admitUnlessDraining); the reads stay up.
// {path} is one percent-encoded workflow.PathKey: ServeMux splits the ESCAPED path, so a node id's "/"
// travels as %2F inside the segment.
func (rr *runRoutes) register(m routeMux) {
	write := func(b route.Binding) route.Binding {
		b.Handler = rr.runs.lifecycle.admitUnlessDraining(b.Handler)
		return b
	}
	idp := route.WithParams[marotte.IDPath]
	step := route.WithParams[marotte.RunStepPath]
	ok := func(pattern string, h http.HandlerFunc) route.Binding {
		return write(idp(route.JSON[struct{}, httpreply.OKBody](pattern, h)))
	}
	m.Bind(route.WithQuery[RunQuery](route.WithParams[marotte.IDPath](route.Raw("GET /api/runs/{id}", http.HandlerFunc(rr.handleRun)))))
	m.Bind(idp(route.JSON[struct{}, marotte.RunControlsResponse]("GET /api/runs/{id}/controls", http.HandlerFunc(rr.handleControls))))
	m.Bind(route.JSON[struct{}, marotte.LiveRunsResponse]("GET /api/runs/live", http.HandlerFunc(rr.handleLiveRuns)))
	m.Bind(step(route.JSON[struct{}, marotte.RunStepTranscript]("GET /api/runs/{id}/steps/{path}", http.HandlerFunc(rr.handleStepTranscript))))
	m.Bind(step(route.JSON[struct{}, marotte.RunStepCapture]("GET /api/runs/{id}/steps/{path}/capture", http.HandlerFunc(rr.handleStepCapture))))
	m.Bind(route.WithQuery[RunTurnQuery](route.WithParams[marotte.IDTurnPath](
		route.JSON[struct{}, RunTurnRange]("GET /api/runs/{id}/turns/{turn}", http.HandlerFunc(rr.handleTurnRange)),
	)))
	m.Bind(write(route.JSON[marotte.RunLaunchRequest, marotte.RunLaunchedResponse]("POST /api/runs", http.HandlerFunc(rr.handleLaunch))))
	m.Bind(ok("POST /api/runs/{id}/cancel", rr.handleCancel))
	m.Bind(ok("POST /api/runs/{id}/pause", rr.handlePause))
	m.Bind(ok("POST /api/runs/{id}/resume", rr.handleResume))
	m.Bind(write(idp(route.JSON[struct{}, marotte.RunRetriedResponse]("POST /api/runs/{id}/retry", http.HandlerFunc(rr.handleRetry)))))
	m.Bind(ok("DELETE /api/runs/{id}", rr.handleDelete))
	m.Bind(write(idp(route.JSON[marotte.RunExtendRequest, httpreply.OKBody]("POST /api/runs/{id}/extend", http.HandlerFunc(rr.handleExtend)))))
	m.Bind(write(idp(route.JSON[marotte.RunFinishLoopRequest, httpreply.OKBody]("POST /api/runs/{id}/finish-loop", http.HandlerFunc(rr.handleFinishLoop)))))
	m.Bind(write(idp(route.JSON[marotte.RunAnswerRequest, httpreply.OKBody]("POST /api/runs/{id}/answer", http.HandlerFunc(rr.handleAnswer)))))
	m.Bind(write(step(route.JSON[marotte.RunStepMessageRequest, marotte.RunStepMessageResponse](
		"POST /api/runs/{id}/steps/{path}/message", http.HandlerFunc(rr.handleStepMessage),
	))))
	m.Bind(write(step(route.JSON[marotte.RunStepSteerRequest, RunStepSteerRemoved](
		"POST /api/runs/{id}/steps/{path}/steer-remove", http.HandlerFunc(rr.handleStepSteerRemove),
	))))
	m.Bind(write(step(route.JSON[struct{}, httpreply.OKBody]("POST /api/runs/{id}/steps/{path}/steer-clear", http.HandlerFunc(rr.handleStepSteerClear)))))
	m.Bind(write(step(route.JSON[marotte.RunStepEndRequest, httpreply.OKBody]("POST /api/runs/{id}/steps/{path}/end", http.HandlerFunc(rr.handleStepEnd)))))
	m.Bind(route.JSON[struct{}, marotte.RecipesResponse]("GET /api/recipes", http.HandlerFunc(rr.handleRecipes)))
	rr.registerLaunchRules(m, write)
}
