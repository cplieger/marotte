package translate

// The step-session registry, and the classifier that needs it. A workflow step runs as its own
// ACP session on the launching chat's process. A `session/update` self-describes via
// `params.update._meta.kiro.workflow` (under `update`, not `params`); a host request or `_kiro/*`
// notification carries no marker, so there the only handle is the session id and `node_start` is
// the one frame that announces it — which is the case this registry serves. Nothing is persisted:
// the run's own `inspect` state carries `sessionId` on every node as the durable copy.

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// FrameOwner is who a frame on a chat's connection belongs to.
type FrameOwner uint8

// The three owners. A frame is the chat's own unless its session id says otherwise, and a
// differing id is a run step when the registry or the frame's metadata says so, else a subagent.
const (
	// OwnerChat is the launching chat itself: no session id, or the chat's own.
	OwnerChat FrameOwner = iota
	// OwnerSubagent is a session id that differs and is not a known run step. Retained rather
	// than assumed away: v3 attributes subagents by `agentSubtaskId` on the parent session.
	OwnerSubagent
	// OwnerStep is a workflow step session.
	OwnerStep
)

// FrameAttribution is who a `session/update` frame belongs to, in the form a per-kind handler
// can act on. Three owners rather than the two a bare `subSessionID string` could express: that
// gave the chat and a run step the same empty value, so `HandleSessionInfoUpdate` counted every
// step's `turn_completion` as one of the launching chat's own turns.
type FrameAttribution struct {
	// SubSessionID is the frame's session id when a SUBAGENT owns it, empty for the chat and
	// for a run step. Handlers stamp it onto a tool call or an ask, so a non-empty value means
	// "attribute this to a subagent" and nothing else; do not repurpose it as "not the chat".
	SubSessionID string
	// SessionID is the frame's own session id as it arrived, the step's for a step frame,
	// which a run turn's turn_open records.
	SessionID string
	// RunID and NodePath address a step's turn in the run's log: the run selects the log and
	// the node PATH the turn, because a parallel node is several open turns in one log. Set
	// with Step, from the frame's own workflow meta or from the step registry.
	RunID    string
	NodePath string
	// Step reports that a workflow STEP session owns the frame: its content is the run log's
	// and never the chat's, and its metering must not reach the chat's own accounting.
	Step bool
}

// ChatOwned reports whether the chat's OWN session sent the frame. Both fields are
// tested: an empty SubSessionID alone does not mean the chat owns the frame, since a
// step has one too.
func (a FrameAttribution) ChatOwned() bool {
	return !a.Step && a.SubSessionID == ""
}

// Attribute classifies a frame and returns what its handlers need: the one derivation point for
// a `session/update`, so a step cannot classify differently depending on which handler reads it.
// wf is the frame's own workflow meta when it carries one, which is how a CONTENT frame
// classifies correctly even with the step registry cold after a restart; a frame with none
// takes its run and path from the registry's record for the session.
func (t *Translator) Attribute(chatID marotte.ChatID, sessionID string, wf *ACPWorkflowMeta) FrameAttribution {
	switch t.ClassifyFrame(chatID, sessionID, wf != nil) {
	case OwnerSubagent:
		return FrameAttribution{SubSessionID: sessionID, SessionID: sessionID}
	case OwnerStep:
		attr := FrameAttribution{Step: true, SessionID: sessionID}
		if wf != nil && wf.WorkflowID != "" {
			attr.RunID, attr.NodePath = wf.WorkflowID, runNodePath(wf)
		} else {
			ref := t.steps.refFor(sessionID)
			attr.RunID, attr.NodePath = ref.WorkflowID, ref.NodePath
		}
		return attr
	case OwnerChat:
		return FrameAttribution{SessionID: sessionID}
	}
	return FrameAttribution{SessionID: sessionID}
}

// StepAttribution is the attribution the parentless run bridge's dispatcher hands a step's
// content frame: Step set, the run from the bridge's own key, the path from the frame's meta
// or the registry.
func (t *Translator) StepAttribution(runID, sessionID string, wf *ACPWorkflowMeta) FrameAttribution {
	attr := FrameAttribution{Step: true, SessionID: sessionID, RunID: runID}
	if wf != nil && wf.WorkflowID != "" {
		attr.NodePath = runNodePath(wf)
	} else {
		attr.NodePath = t.steps.refFor(sessionID).NodePath
	}
	return attr
}

// runNodePath is the step's address within its run, and the ONE join for it, so the
// run log's turn key and the run card's row key cannot diverge. The node PATH
// rather than the id, because a repeat's iterations share an id and two passes of
// a loop body would stream into each other's rows. Falls back to the id when KAS
// sends no path: a row in the wrong place beats content that vanishes.
func runNodePath(w *ACPWorkflowMeta) string {
	if w == nil {
		return ""
	}
	if len(w.NodePath) > 0 {
		return strings.Join(w.NodePath, "/")
	}
	return w.NodeID
}

// stepRegistry maps a step's ACP session id to the run and node it belongs to. Written and
// read from the bridge-forward goroutine, but a runtime has many chats and one Translator, so
// the mutex is real contention protection.
type stepRegistry struct {
	byID  map[string]StepRef
	byRun map[string]map[string]struct{}
	mu    sync.RWMutex
}

// StepRef names the run and node a step session is executing. NodePath is the
// step's address in the run's log, recorded at node_start, so a frame carrying no
// workflow meta (an update for a step's call) still finds its turn.
type StepRef struct {
	WorkflowID string
	NodeID     string
	NodePath   string
}

func newStepRegistry() *stepRegistry {
	return &stepRegistry{
		byID:  make(map[string]StepRef),
		byRun: make(map[string]map[string]struct{}),
	}
}

// record notes that sessionID is executing node nodeID of run workflowID at nodePath.
func (s *stepRegistry) record(sessionID, workflowID, nodeID, nodePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[sessionID] = StepRef{WorkflowID: workflowID, NodeID: nodeID, NodePath: nodePath}
	ids, ok := s.byRun[workflowID]
	if !ok {
		ids = make(map[string]struct{})
		s.byRun[workflowID] = ids
	}
	ids[sessionID] = struct{}{}
}

// lookup resolves a session id to its step, if it is one.
func (s *stepRegistry) lookup(sessionID string) (StepRef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.byID[sessionID]
	return ref, ok
}

// refFor resolves a frame's session id to its run and node, when it is a step's. The empty
// StepRef for everything else lets an ask handler stamp unconditionally: omitempty keeps two
// empty strings off the wire, so the miss is not an error.
func (s *stepRegistry) refFor(sessionID string) StepRef {
	if sessionID == "" {
		return StepRef{}
	}
	ref, _ := s.lookup(sessionID)
	return ref
}

// forgetRun drops every step session of a terminated run, bounding growth that
// would otherwise accumulate one entry per step forever. The hook is a TERMINAL `run_complete`,
// gated by the caller — see ForgetRunSteps.
//
// The status test is the whole correctness of the bound: KAS reports a step parked on a question
// through this same frame with `status: paused`, so wiping on every `run_complete` empties the
// registry MID-RUN and the resumed run's next ask resolves no run id at all — invisible to every
// run-scoped surface while still lighting the launching chat's tab dot.
func (s *stepRegistry) forgetRun(workflowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byRun[workflowID] {
		delete(s.byID, id)
	}
	delete(s.byRun, workflowID)
}

// ForgetRunSteps drops the step sessions of a run that has ENDED. Exported
// rather than driven off the `run_complete` frame this translator already handles, because the
// gate is a status test owned by `internal/agent`: the branch that already makes this decision
// for `forgetBounds` makes it here too, rather than a second copy of the predicate that is a
// second place to get `paused` wrong. No empty-id guard is needed — both `record` doors refuse
// one, so an empty argument names an empty bucket.
func (t *Translator) ForgetRunSteps(workflowID string) {
	t.steps.forgetRun(workflowID)
}

// RecordStepSession notes a step session learned from an `inspect` read rather than a
// `node_start` frame. That is the recovery path: after a restart the registry is empty and a
// resumed run's step frames carry session ids nothing announced in this process.
func (t *Translator) RecordStepSession(sessionID, workflowID, nodeID, nodePath string) {
	if sessionID == "" || workflowID == "" {
		return
	}
	t.steps.record(sessionID, workflowID, nodeID, nodePath)
}

// RecordRunSteps seeds the registry from a raw `_kiro/workflow/inspect` reply, on every run read,
// because that read is the only other moment the durable step→session mapping is in hand: a
// restart empties the registry while the run carries on, and a resumed run's frames would then be
// classified as a subagent's. Best-effort by design — the run endpoint passes the same bytes
// through and is useful either way, so a decode failure must not fail a read, and the cost of
// missing it is one run's frames misclassified until the next read.
func (t *Translator) RecordRunSteps(raw json.RawMessage) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	for _, st := range workflow.StepSessions(res.State) {
		t.RecordStepSession(st.SessionID, res.State.WorkflowID, st.NodeID, strings.Join(st.Path, "/"))
	}
}

// ClassifyFrame decides who a frame belongs to, from the chat it arrived on and the session id it
// carries; the single classifier both derivation sites use. `workflowMarked` is the frame's own
// answer when it has one, which classifies a `session/update` correctly even with a cold registry.
func (t *Translator) ClassifyFrame(chatID marotte.ChatID, sessionID string, workflowMarked bool) FrameOwner {
	parent := t.sessions.ParentACPSession(chatID)
	if sessionID == "" || parent == "" || sessionID == parent {
		return OwnerChat
	}
	if workflowMarked {
		return OwnerStep
	}
	if _, ok := t.steps.lookup(sessionID); ok {
		return OwnerStep
	}
	return OwnerSubagent
}

// foreignSession reports whether a frame belongs to something OTHER than the chat it arrived on,
// subagent or workflow step alike. Distinct from deriveSubSession, and both exist because the
// questions differ: code_references, governance and safety DROP a non-chat frame for dedup (KAS
// fans one account-global payload out to every live session), while elicitation, permission and
// user_input emit either way and only need to know whether to LABEL the ask as a subagent's —
// where a step must answer no, or its ask names a subagent that does not exist.
func (t *Translator) foreignSession(chatID marotte.ChatID, sessionID string) bool {
	return t.ClassifyFrame(chatID, sessionID, false) != OwnerChat
}

// ReportStepProgress tells the host a run's step produced a frame, one of the two signals
// that roll its idle window forward (node_complete, the other, is wrapped host-side). ANY
// live frame of the step's session is progress: a text or thinking chunk, a tool call, a
// permission ask. Called at the frame's entry point, ahead of every handler, because
// those make RENDERING decisions and this is LIVENESS; sat under them a display
// preference would decide whether a run reads as stalled.
func (t *Translator) ReportStepProgress(attr FrameAttribution) {
	if !attr.Step || attr.RunID == "" {
		return
	}
	t.runBounds.RunMadeProgress(attr.RunID)
}
