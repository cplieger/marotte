package agent

import (
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// signalTripwire watches for kirodotdev/Kiro#12281's fix. KAS 2.37.1 to 3.1.0 hold the parallel branch of a
// paused step until the run loop settles, whether the step signalled success or error or a client
// ended it, so while any sibling runs the step's lane does not move on. A watched step's lane
// advancing while a sibling marotte saw running when it paused has not stopped since means KAS no
// longer waits. A step's own end or resume is no evidence: a client's update_status or answer makes
// the same frames, which carry no actor.
type signalTripwire struct {
	// runs holds, per workflow id, each watched paused step by PathKey.
	runs   map[string]map[string]*signalWatch
	fired  int
	logged bool
	mu     sync.Mutex
}

type signalWatch struct {
	// running are the sibling PathKeys running when the step was seen paused and not seen stopped since.
	running map[string]struct{}
	nodeID  string
	lane    workflow.Lane
	// signalled is whether the step was last seen paused with a success or error signal; it outlives
	// the step's end, which clears the signal KAS reports.
	signalled bool
}

// observe folds one inspect of workflowID into the watch.
func (w *signalTripwire) observe(workflowID string, s *workflow.State) {
	steps := workflow.Steps(s)
	w.mu.Lock()
	defer w.mu.Unlock()
	if marotte.RunStatus(s.Status).Terminal() || s.Status == string(marotte.RunStatusPaused) {
		delete(w.runs, workflowID)
		return
	}
	now := make(map[string]*workflow.StepSession, len(steps))
	running := map[string]struct{}{}
	for i := range steps {
		key := workflow.PathKey(steps[i].Path)
		now[key] = &steps[i]
		if steps[i].Status == string(marotte.RunNodeStatusRunning) {
			running[key] = struct{}{}
		}
	}
	watched := w.runs[workflowID]
	for key, sw := range watched {
		if !settle(sw, now[key], running) {
			delete(watched, key)
		}
	}
	for key, st := range now {
		if _, seen := watched[key]; !seen && st.Status == string(marotte.RunNodeStatusPaused) && st.SessionID != "" {
			w.watchLocked(workflowID, key, s, st, running)
		}
	}
}

// settle reads a watched step's new state, dropping the siblings no longer running; false means the
// watch is over: the step is gone or reads running. A running read is a resume, which does not trip.
func settle(sw *signalWatch, st *workflow.StepSession, running map[string]struct{}) bool {
	for sib := range sw.running {
		if _, still := running[sib]; !still {
			delete(sw.running, sib)
		}
	}
	switch {
	case st == nil, st.Status == string(marotte.RunNodeStatusRunning):
		return false
	case st.Status == string(marotte.RunNodeStatusPaused):
		sw.signalled = st.EndSignalled()
	}
	return true
}

func (w *signalTripwire) watchLocked(workflowID, key string, s *workflow.State, st *workflow.StepSession, running map[string]struct{}) {
	if w.runs == nil {
		w.runs = make(map[string]map[string]*signalWatch)
	}
	if w.runs[workflowID] == nil {
		w.runs[workflowID] = make(map[string]*signalWatch)
	}
	sw := &signalWatch{nodeID: st.NodeID, lane: workflow.LaneOf(s, key), signalled: st.EndSignalled(), running: make(map[string]struct{}, len(running))}
	for sib := range running {
		sw.running[sib] = struct{}{}
	}
	w.runs[workflowID][key] = sw
}

// nodeStarted is a node_start frame for nodePath, path its segments: a watched step's own start ends
// its watch, and a start past a watched step in its lane is the advance the tripwire waits for.
func (w *signalTripwire) nodeStarted(workflowID, nodePath string, path []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	watched := w.runs[workflowID]
	for at, sw := range watched {
		switch {
		case at == nodePath:
			delete(watched, at)
		case sw.lane.Passed(path):
			if sw.signalled {
				w.earlyLocked(workflowID, sw)
			}
			delete(watched, at)
		}
	}
}

// nodeStopped is a node_paused or node_complete frame for nodePath: as a sibling it no longer counts
// as running.
func (w *signalTripwire) nodeStopped(workflowID, nodePath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, sw := range w.runs[workflowID] {
		delete(sw.running, nodePath)
	}
}

// runStopped drops a run that paused or ended: every sibling has stopped.
func (w *signalTripwire) runStopped(workflowID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.runs, workflowID)
}

func (w *signalTripwire) earlyLocked(workflowID string, sw *signalWatch) {
	if len(sw.running) == 0 {
		return
	}
	const what = "a signalled paused step's lane moved on while another step still ran"
	w.fired++
	if w.logged {
		slog.Debug(what, "workflow_id", logsafe.Field(workflowID), "node_id", logsafe.Field(sw.nodeID), "count", w.fired)
		return
	}
	w.logged = true
	slog.Info(what+": kirodotdev/Kiro#12281 may be fixed; verify before removing marotte's workarounds for it",
		"workflow_id", logsafe.Field(workflowID), "node_id", logsafe.Field(sw.nodeID))
}
