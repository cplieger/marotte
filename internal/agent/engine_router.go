package agent

import (
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
)

// engineRouter places the frames no session lane claims: a run's lifecycle frames on its owner's lane (the session
// that launched it, or a chat's), a step's frames and requests on its run's owner, a step replay's frames on the
// engine lane's replay reader. Its tables are written on the read loop, in pipe order, and through three methods
// that do no I/O under its mutex; the read loop takes the engine's lane table and releases it before Route, and no
// method here calls back into the engine, so the two mutexes never nest.
type engineRouter struct {
	// owner: workflowId → the session whose lane takes the run's frames.
	owner map[string]marotte.SessionID
	// steps: step session → its run.
	steps map[marotte.SessionID]string
	// started journals, per engine generation, every run a run_start or node_start was read for and no run_complete
	// or paused since: the read loop records it before the frame's lane is consumed, so an engine end snapshots a start no
	// consumer saw yet.
	started map[uint64]map[string]bool
	// replayOpen reports a step session whose transcript load is open: its replay frames go to the engine lane.
	replayOpen func(marotte.SessionID) bool
	mu         sync.Mutex
}

var _ bridge.Router = genRouter{}

func newEngineRouter() *engineRouter {
	return &engineRouter{
		owner:   map[string]marotte.SessionID{},
		steps:   map[marotte.SessionID]string{},
		started: map[uint64]map[string]bool{},
	}
}

// genRouter is the router as one engine generation sees it, so its start journal is that generation's.
type genRouter struct {
	r   *engineRouter
	gen uint64
}

func (g genRouter) Route(h *bridge.FrameHead) marotte.SessionID { return g.r.route(h, g.gen) }

// StepRun names a step session's run on the engine's dropped-frame line.
func (g genRouter) StepRun(sessionID marotte.SessionID) (string, bool) { return g.r.StepRun(sessionID) }

func (r *engineRouter) route(h *bridge.FrameHead, gen uint64) marotte.SessionID {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h.Replay && h.SessionID != "" && r.replayOpen != nil && r.replayOpen(h.SessionID) {
		return ""
	}
	if strings.HasPrefix(h.Method, workflowMethodPrefix) && h.WorkflowID != "" {
		switch h.Method {
		case methodWFRunStart:
			if h.ParentSessionID != "" {
				r.owner[h.WorkflowID] = h.ParentSessionID
			}
			r.journalLocked(gen, h.WorkflowID)
		case methodWFNodeStart:
			if h.SessionID != "" {
				r.steps[h.SessionID] = h.WorkflowID
			}
			r.journalLocked(gen, h.WorkflowID)
		case methodWFRunComplete, methodWFPaused:
			// A paused run is settled for the snapshot; the start frames of its resume journal it again.
			delete(r.started[gen], h.WorkflowID)
		}
		return r.owner[h.WorkflowID]
	}
	if wf, ok := r.steps[h.SessionID]; ok {
		return r.owner[wf]
	}
	return ""
}

func (r *engineRouter) journalLocked(gen uint64, workflowID string) {
	m := r.started[gen]
	if m == nil {
		m = map[string]bool{}
		r.started[gen] = m
	}
	m[workflowID] = true
}

// takeStarted returns generation gen's journalled starts and forgets them; it runs once the generation's reader
// ended, so nothing journals to gen afterwards.
func (r *engineRouter) takeStarted(gen uint64) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.started[gen]
	delete(r.started, gen)
	return slices.Sorted(maps.Keys(m))
}

// registerOwner makes laneSessionID the owner of workflowID's frames; it returns once the write is visible to Route,
// so a verb written after it is routed by it.
func (r *engineRouter) registerOwner(workflowID string, laneSessionID marotte.SessionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner[workflowID] = laneSessionID
}

// seedSteps adds stepSessionID → workflowID rows from an inspect reply; rows of other runs are kept.
func (r *engineRouter) seedSteps(workflowID string, stepSessionIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range stepSessionIDs {
		if id != "" {
			r.steps[marotte.SessionID(id)] = workflowID
		}
	}
}

// forgetWorkflow drops workflowID's owner row and its step rows.
func (r *engineRouter) forgetWorkflow(workflowID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owner, workflowID)
	for sid, wf := range r.steps {
		if wf == workflowID {
			delete(r.steps, sid)
		}
	}
}

// StepRun reports the run a step session belongs to.
func (r *engineRouter) StepRun(sessionID marotte.SessionID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wf, ok := r.steps[sessionID]
	return wf, ok
}

const workflowMethodPrefix = "_kiro/workflow/"
