package agent

import (
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// signalTripwire watches for kirodotdev/Kiro#11733's fix. KAS 2.32.0 neither resumes a paused step
// nor applies its recorded success or error until the run loop settles, so while any sibling runs
// the step stays paused and the run tab labels a signalled one done-pending. A watched step that
// resumes, or a signalled one that ends, while a sibling marotte saw running when it paused has not
// stopped since means KAS no longer waits, or that another client resumed or ended it: the lifecycle
// frames carry no actor, so the line names both.
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
	// signalled is whether the step was last seen paused with a success or error signal.
	signalled bool
}

func stepEnded(status string) bool {
	return status == string(marotte.RunNodeStatusCompleted) || status == string(marotte.RunNodeStatusFailed)
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
		if !w.settleLocked(workflowID, sw, now[key], running) {
			delete(watched, key)
		}
	}
	for key, st := range now {
		if _, seen := watched[key]; !seen && st.Status == string(marotte.RunNodeStatusPaused) && st.SessionID != "" {
			w.watchLocked(workflowID, key, st, running)
		}
	}
}

// settleLocked reads a watched step's new state, dropping the siblings no longer running; false
// means the watch is over.
func (w *signalTripwire) settleLocked(workflowID string, sw *signalWatch, st *workflow.StepSession, running map[string]struct{}) bool {
	for sib := range sw.running {
		if _, still := running[sib]; !still {
			delete(sw.running, sib)
		}
	}
	switch {
	case st == nil:
	case st.Status == string(marotte.RunNodeStatusPaused):
		sw.signalled = st.EndSignalled()
		return true
	case st.Status == string(marotte.RunNodeStatusRunning):
		w.earlyLocked(workflowID, sw, "a paused step resumed before the run settled")
	case stepEnded(st.Status) && sw.signalled:
		w.earlyLocked(workflowID, sw, "a signalled paused step ended before the run settled")
	}
	return false
}

func (w *signalTripwire) watchLocked(workflowID, key string, st *workflow.StepSession, running map[string]struct{}) {
	if w.runs == nil {
		w.runs = make(map[string]map[string]*signalWatch)
	}
	if w.runs[workflowID] == nil {
		w.runs[workflowID] = make(map[string]*signalWatch)
	}
	sw := &signalWatch{nodeID: st.NodeID, signalled: st.EndSignalled(), running: make(map[string]struct{}, len(running))}
	for sib := range running {
		sw.running[sib] = struct{}{}
	}
	w.runs[workflowID][key] = sw
}

// nodeStarted is a node_start frame for nodePath.
func (w *signalTripwire) nodeStarted(workflowID, nodePath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sw, ok := w.runs[workflowID][nodePath]; ok {
		w.earlyLocked(workflowID, sw, "a paused step resumed before the run settled")
		delete(w.runs[workflowID], nodePath)
	}
}

// nodeStopped is a node_paused or node_complete frame for nodePath: a watched step's own end is
// checked, and as a sibling it no longer counts as running.
func (w *signalTripwire) nodeStopped(workflowID, nodePath, status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	watched := w.runs[workflowID]
	if sw, ok := watched[nodePath]; ok && stepEnded(status) {
		if sw.signalled {
			w.earlyLocked(workflowID, sw, "a signalled paused step ended before the run settled")
		}
		delete(watched, nodePath)
	}
	for _, sw := range watched {
		delete(sw.running, nodePath)
	}
}

// forget drops the step at nodePath before marotte's own end of it, which KAS applies at once.
func (w *signalTripwire) forget(workflowID, nodePath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.runs[workflowID], nodePath)
}

// runStopped drops a run that paused or ended: every sibling has stopped.
func (w *signalTripwire) runStopped(workflowID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.runs, workflowID)
}

func (w *signalTripwire) earlyLocked(workflowID string, sw *signalWatch, what string) {
	if len(sw.running) == 0 {
		return
	}
	w.fired++
	if w.logged {
		slog.Debug(what, "workflow_id", logsafe.Field(workflowID), "node_id", logsafe.Field(sw.nodeID), "count", w.fired)
		return
	}
	w.logged = true
	slog.Info(what+"; another client may have done it, or kirodotdev/Kiro#11733 may be fixed; verify before removing the done-pending label",
		"workflow_id", logsafe.Field(workflowID), "node_id", logsafe.Field(sw.nodeID))
}
