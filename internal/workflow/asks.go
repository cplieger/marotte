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
	// Reason is KAS's sentence for why this step parked, set only for a park with no signal (a lost
	// send_message signal, or a step that ended its turn without one) and only when it is the run's
	// sole waiting step: the reply carries one run-level reason, so with two waiting steps it names either.
	Reason string
	// Hold is what marotte's own requests on the run block: stopping or pending.
	Hold marotte.RunAskBlocker
	// Wait is what KAS's state blocks, computed without reading Hold, so a verb that registered its
	// own pending before its check still sees KAS's refusal conditions.
	Wait marotte.RunAskBlocker
	// AskedAt is display only.
	AskedAt  string
	NodePath []string
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
	// Pending: marotte has a prompt on the run not yet settled.
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

// PausedOnAsks reports whether reply is a paused run whose every paused leaf waits on a person, the
// state where KAS's resume leaves each step's need_input signal and re-parks it at once.
func PausedOnAsks(reply Inspect) bool {
	if !reply.OK || reply.State == nil || reply.State.Status != statusPaused {
		return false
	}
	asking := false
	for _, v := range nodes(reply.State) {
		if len(v.n.Children) > 0 || v.n.Status != statusPaused {
			continue
		}
		if !v.n.waits() {
			return false
		}
		asking = true
	}
	return asking
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
	sole := len(ParkedSteps(reply)) == 1
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
			Reason:        unsignalledReason(s, v.n, sole),
			Hold:          hold(log),
			Wait:          wait(s, all, i, log),
			AskedAt:       step.EndedAt,
		})
	}
	return out
}

func unsignalledReason(s *State, n *node, sole bool) string {
	if !sole || n.CompletionSignal != "" {
		return ""
	}
	return s.PauseReason
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
