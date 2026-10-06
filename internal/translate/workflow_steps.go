package translate

// The step-session registry and its classifier. A step runs as its own ACP session on the
// launching chat's process. A `session/update` self-describes via
// `params.update._meta.kiro.workflow`; a host request or `_kiro/*` notification carries
// only a session id, which `node_start` alone announces. The run's `inspect` state is the
// durable copy, so nothing is persisted here.

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// FrameOwner is who a frame on a chat's connection belongs to.
type FrameOwner uint8

// The three owners: the chat's own unless the session id differs, then a run step when the
// registry or frame metadata says so, else a subagent.
const (
	// OwnerChat is the launching chat itself: no session id, or the chat's own.
	OwnerChat FrameOwner = iota
	// OwnerSubagent is a session id that differs and is not a known run step (v3 attributes
	// subagents by `agentSubtaskId` on the parent session).
	OwnerSubagent
	// OwnerStep is a workflow step session.
	OwnerStep
)

// FrameAttribution is who a `session/update` frame belongs to, in the form a per-kind
// handler acts on. Three owners: the chat and a run step must not share one empty value.
type FrameAttribution struct {
	// SubSessionID is the frame's session id when a SUBAGENT owns it, empty for the chat and
	// a run step. Non-empty means "attribute to a subagent" only; never "not the chat".
	SubSessionID string
	// SessionID is the frame's session id as it arrived, which a run turn's turn_open records.
	SessionID string
	// RunID and NodePath address a step's turn: the run selects the log and the node PATH the
	// turn (a parallel node is several open turns). Set with Step.
	RunID    string
	NodePath string
	// Step reports a workflow STEP session owns the frame: its content is the run log's and
	// its metering never reaches the chat's own accounting.
	Step bool
}

// ChatOwned reports whether the chat's OWN session sent the frame; a step also has an
// empty SubSessionID.
func (a FrameAttribution) ChatOwned() bool {
	return !a.Step && a.SubSessionID == ""
}

// Attribute classifies a frame: the one derivation point for a `session/update`. wf is the
// frame's own workflow meta, which classifies a CONTENT frame even with a cold registry;
// without it the run and path come from the registry.
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

// StepAttribution is the attribution the parentless run bridge hands a step's content
// frame: Step set, the run from the bridge's key, the path from the meta or the registry.
func (t *Translator) StepAttribution(runID, sessionID string, wf *ACPWorkflowMeta) FrameAttribution {
	attr := FrameAttribution{Step: true, SessionID: sessionID, RunID: runID}
	if wf != nil && wf.WorkflowID != "" {
		attr.NodePath = runNodePath(wf)
	} else {
		attr.NodePath = t.steps.refFor(sessionID).NodePath
	}
	return attr
}

// runNodePath is the step's address within its run, the ONE join for the run log's turn key
// and the run card's row key. The PATH, because a repeat's iterations share an id. Falls
// back to the id: a misplaced row beats vanished content.
func runNodePath(w *ACPWorkflowMeta) string {
	if w == nil {
		return ""
	}
	if len(w.NodePath) > 0 {
		return strings.Join(w.NodePath, "/")
	}
	return w.NodeID
}

// stepRegistry maps a step's ACP session id to its run and node. One Translator serves
// many chats' forward goroutines, hence the mutex.
type stepRegistry struct {
	byID  map[string]StepRef
	byRun map[string]map[string]struct{}
	mu    sync.RWMutex
}

// StepRef names the run and node a step session is executing. NodePath, recorded at
// node_start, lets a frame with no workflow meta still find its turn.
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

// refFor resolves a frame's session id to its run and node when it is a step's; the empty
// StepRef otherwise is harmless (omitempty), so an ask handler stamps unconditionally.
func (s *stepRegistry) refFor(sessionID string) StepRef {
	if sessionID == "" {
		return StepRef{}
	}
	ref, _ := s.lookup(sessionID)
	return ref
}

// forgetRun drops every step session of a terminated run. Only on a TERMINAL
// `run_complete` (the caller gates; see ForgetRunSteps): `paused` arrives on the same
// frame, and wiping then empties the registry MID-RUN, losing the resumed run's asks.
func (s *stepRegistry) forgetRun(workflowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byRun[workflowID] {
		delete(s.byID, id)
	}
	delete(s.byRun, workflowID)
}

// ForgetRunSteps drops the step sessions of a run that has ENDED. Exported because the
// status gate is `internal/agent`'s, made in the same branch as `forgetBounds`. Both
// `record` doors refuse an empty id, so no guard is needed.
func (t *Translator) ForgetRunSteps(workflowID string) {
	t.steps.forgetRun(workflowID)
}

// RecordStepSession notes a step session learned from an `inspect` read: the recovery path
// after a restart, when a resumed run's session ids were never announced here.
func (t *Translator) RecordStepSession(sessionID, workflowID, nodeID, nodePath string) {
	if sessionID == "" || workflowID == "" {
		return
	}
	t.steps.record(sessionID, workflowID, nodeID, nodePath)
}

// RecordRunSteps seeds the registry from a raw `_kiro/workflow/inspect` reply on every run
// read, so a restart does not leave a resumed run's frames classified as a subagent's.
// Best-effort: a decode failure must not fail the read.
func (t *Translator) RecordRunSteps(raw json.RawMessage) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	for _, st := range workflow.StepSessions(res.State) {
		t.RecordStepSession(st.SessionID, res.State.WorkflowID, st.NodeID, strings.Join(st.Path, "/"))
	}
}

// ClassifyFrame decides who a frame belongs to from its chat and session id; the single
// classifier. `workflowMarked` is the frame's own answer, which works with a cold registry.
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

// foreignSession reports whether a frame belongs to something OTHER than its chat, subagent
// or step alike. Dedup guards (code_references, governance, safety) drop such frames;
// labelled asks use deriveSubSession instead, where a step must answer no.
func (t *Translator) foreignSession(chatID marotte.ChatID, sessionID string) bool {
	return t.ClassifyFrame(chatID, sessionID, false) != OwnerChat
}

// ReportStepProgress tells the host a run's step produced a frame, rolling its idle window
// forward. Any live frame of the step is progress, so it is called ahead of every handler:
// those make rendering decisions, and this is liveness.
func (t *Translator) ReportStepProgress(attr FrameAttribution) {
	if !attr.Step || attr.RunID == "" {
		return
	}
	t.runBounds.RunMadeProgress(attr.RunID)
}
