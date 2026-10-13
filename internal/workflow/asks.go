package workflow

import (
	"cmp"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
)

// Ask is one step waiting on a person, derived from one inspect reply and marotte's run log.
type Ask struct {
	ID            string
	Position      string
	NodeID        string
	StepSessionID string
	AgentName     string
	Text          string
	TextSource    marotte.RunAskText
	// Hold is what marotte's own requests on the run block: stopping or pending.
	Hold marotte.RunAskBlocker
	// Wait is what KAS's state blocks, computed without reading Hold, so a verb that registered its
	// own pending before its check still sees KAS's refusal conditions.
	Wait marotte.RunAskBlocker
	// AskedAt is display only.
	AskedAt  string
	NodePath []string
	// CanContinue is a fact of the reply alone: the run is paused and KAS's `update` would target
	// this execution, its path and its session. A card combines it with Blocked.
	CanContinue bool
}

// Blocked is the one blocker a card and the wire carry.
func (a *Ask) Blocked() marotte.RunAskBlocker { return cmp.Or(a.Hold, a.Wait) }

// RunLog is what marotte itself observed or did on a run, the second input of OpenAsks.
type RunLog struct {
	// Steps is keyed by step session id.
	Steps map[string]StepLog
	// Epoch keys an ask whose step marotte saw no turn_start for this boot.
	Epoch string
	// Stopping: a marotte cancel or delete is queued on the run.
	Stopping bool
	// Pending: marotte has a prompt or a Continue on the run not yet settled.
	Pending bool
	// HostLost: the run's host bridge ended after its latest arm.
	HostLost bool
	// Quiet: nothing on the run moved for the settle window.
	Quiet bool
}

// StepLog is one step session's turn record as marotte saw it this boot.
type StepLog struct {
	// Marker is the newest turn_start's marker, "" when none was seen. A turn_end never moves it,
	// so an ask's id is fixed for the life of its parking turn.
	Marker string
	// Asked is the warning text of the newest turn that ended, "" when that turn sent none.
	Asked string
	// Said is the parking turn's last agent words from the run's own record.
	Said    string
	EndedAt string
	// Open is true while the newest edge is a turn_start with no turn_end after it.
	Open bool
}

// ParkedSteps lists the steps that satisfy the wait predicate in pre-order, none for a terminal or
// unreadable reply. OpenAsks answers exactly these.
func ParkedSteps(reply Inspect) []StepSession {
	if !reply.OK || marotte.RunStatus(reply.State.Status).Terminal() {
		return nil
	}
	var out []StepSession
	for _, v := range nodes(reply.State) {
		if v.n.waits() {
			out = append(out, StepSession{NodeID: v.n.NodeID, SessionID: v.n.SessionID, Status: v.n.Status, Path: v.path})
		}
	}
	return out
}

// AskPosition is one step execution's ask identity across its turns: an Ask's Position, and the
// first component of every Ask.ID asked there.
func AskPosition(workflowID string, st StepSession) string {
	return keyenc.Join(workflowID, PathKey(st.Path), st.SessionID)
}

// OpenAsks is every step of reply waiting on a person, in pre-order. Pure: it reads no clock and
// does no I/O. An unreadable reply answers none, which the caller must not publish as "answered".
func OpenAsks(reply Inspect, log RunLog) []Ask {
	if !reply.OK || marotte.RunStatus(reply.State.Status).Terminal() {
		return nil
	}
	s := reply.State
	all := nodes(s)
	target, hasTarget := StatusUpdateTarget(s)
	var out []Ask
	for i, v := range all {
		if !v.n.waits() {
			continue
		}
		step := log.Steps[v.n.SessionID]
		position := AskPosition(s.WorkflowID, StepSession{Path: v.path, SessionID: v.n.SessionID})
		marker := cmp.Or(step.Marker, keyenc.Join(log.Epoch, "0"))
		text, source := askText(step)
		out = append(out, Ask{
			ID:            keyenc.Join(position, marker),
			Position:      position,
			NodeID:        v.n.NodeID,
			NodePath:      v.path,
			StepSessionID: v.n.SessionID,
			AgentName:     v.n.AgentName,
			Text:          text,
			TextSource:    source,
			Hold:          hold(log),
			Wait:          wait(s, all, i, log),
			CanContinue: s.Status == statusPaused && hasTarget &&
				PathKey(target.Path) == PathKey(v.path) && target.SessionID == v.n.SessionID,
			AskedAt: step.EndedAt,
		})
	}
	return out
}

func askText(step StepLog) (string, marotte.RunAskText) {
	if step.Asked != "" && !step.Open {
		return step.Asked, marotte.RunAskTextAsked
	}
	if step.Said != "" {
		return step.Said, marotte.RunAskTextLastMessage
	}
	return "", marotte.RunAskTextUnseen
}

func hold(log RunLog) marotte.RunAskBlocker {
	switch {
	case log.Stopping:
		return marotte.RunAskStopping
	case log.Pending:
		return marotte.RunAskPending
	}
	return ""
}

// wait never reads Stopping or Pending. A container reads running whenever a descendant does, so
// branch_running counts leaves only; `type` is an open enum, so any running leaf counts.
func wait(s *State, all []visit, self int, log RunLog) marotte.RunAskBlocker {
	if s.Status == statusPaused {
		return ""
	}
	switch {
	case log.HostLost:
		return marotte.RunAskRecovering
	case otherLeafRunning(all, self):
		return marotte.RunAskBranchRunning
	case log.Quiet:
		return marotte.RunAskUnsettled
	}
	return marotte.RunAskSettling
}

func otherLeafRunning(all []visit, self int) bool {
	for i, v := range all {
		if i != self && len(v.n.Children) == 0 && v.n.Status == statusRunning {
			return true
		}
	}
	return false
}
