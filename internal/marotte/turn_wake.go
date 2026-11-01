package marotte

import (
	"slices"
	"strconv"
	"strings"
)

// WakeReason is what started an agent-initiated turn, as the turn_wake entry names it. A
// closed enum because the client words each one; WakeReasonUnknown is a reason KAS sent that
// this build does not know, or none.
type WakeReason string

const (
	// WakeReasonStepMessage is a workflow message or KAS's own note about a run reaching the
	// idle chat (send-message-wake).
	WakeReasonStepMessage WakeReason = "step_message"
	// WakeReasonRunFinished is a run the chat launched reaching a terminal status
	// (workflow-complete-wake).
	WakeReasonRunFinished WakeReason = "run_finished"
	// WakeReasonRewake is KAS re-delivering the workflow messages a reader's cancel cleared
	// unread (notification-rewake, or rewakeWithInfo's send-message-wake).
	WakeReasonRewake WakeReason = "rewake"
	// WakeReasonUnknown is a reason this build does not know, or none.
	WakeReasonUnknown WakeReason = "unknown"
)

// WakeNote marks a step message KAS wrote about a run rather than a step's own send_message.
type WakeNote string

const (
	// WakeNoteNetwork is KAS's note that a step is retrying through connection errors, or gave up.
	WakeNoteNetwork WakeNote = "network"
	// WakeNoteRepeatCap is KAS's note that a repeat paused at its iteration limit; Loop and
	// MaxIterations come from the run's typed pause, not the note's sentence.
	WakeNoteRepeatCap WakeNote = "repeat_cap"
)

// WakeSteer is a workflow message a wake names, copied from its row (ID) when the wake was
// recorded, so the header and the rail read the wake alone. RunLabel is the run's name then.
type WakeSteer struct {
	ID       string `json:"id"`
	Run      string `json:"run,omitempty"`
	RunLabel string `json:"run_label,omitempty"`
	Step     string `json:"step,omitempty"`
	Severity string `json:"severity,omitempty"`
	Text     string `json:"text"`
}

// EntryTurnWake is the turn_wake payload: what started an agent-initiated turn, filed once at
// its first agentInitiated frame. It renders nothing at its position; the turn header folds it.
// Steers is the waking message for a step message and the re-delivered rows for a rewake; Run,
// RunLabel, RunStatus, Initiator and StopReason describe a finished run; Note, Loop and
// MaxIterations qualify a step message.
type EntryTurnWake struct {
	Reason        WakeReason  `json:"reason"`
	Note          WakeNote    `json:"note,omitempty"`
	Run           string      `json:"run,omitempty"`
	RunLabel      string      `json:"run_label,omitempty"`
	RunStatus     RunStatus   `json:"run_status,omitempty"`
	Initiator     string      `json:"initiator,omitempty"`
	StopReason    string      `json:"stop_reason,omitempty"`
	Loop          string      `json:"loop,omitempty"`
	Steers        []WakeSteer `json:"steers,omitempty"`
	MaxIterations int         `json:"max_iterations,omitempty"`
}

// WakeInitiatorUser is KAS's stopInitiator for a run the reader stopped or marked complete.
const WakeInitiatorUser = "user"

// WakeCauses is what a chat's log holds that can have started its open turn, for a session:
// Waiting is the unsettled workflow messages sent to it before the turn opened, oldest first;
// Cleared is the rows the newest reader's cancel since the previous turn opened cleared unread,
// when that cancel named a workflow message (KAS re-delivers those in the next turn).
type WakeCauses struct {
	Waiting []WorkflowMessage
	Cleared []WorkflowMessage
}

// PositionedFact is a workflow-message fact and its position in its log, file order.
type PositionedFact struct {
	Fact WorkflowMessageFact
	At   int64
}

// WakeCausesOf reads WakeCauses for session from facts in file order, given where the open turn
// opened and where the turn before it opened (-1 for none). A row filed inside that previous turn
// still counts as waiting: KAS can end its turn and wake the chat before that turn's close lands.
func WakeCausesOf(facts []PositionedFact, session string, opened, prevOpened int64) WakeCauses {
	var out WakeCauses
	if session == "" {
		return out
	}
	before := map[string]struct{}{}
	cancel := -1
	ordered := make([]WorkflowMessageFact, 0, len(facts))
	for i := range facts {
		f := &facts[i]
		ordered = append(ordered, f.Fact)
		if f.At >= opened {
			continue
		}
		if m, ok := f.Fact.Row(); ok && m.Steer.ToSession == session {
			before[m.ID] = struct{}{}
		}
		if f.At > prevOpened && rewakingCancel(&f.Fact, session) {
			cancel = i
		}
	}
	all := FoldWorkflowMessages(ordered)
	out.Waiting = unsettledWhere(&all, func(m *WorkflowMessage) bool {
		_, ok := before[m.ID]
		return ok
	})
	if cancel >= 0 {
		upTo := FoldWorkflowMessages(ordered[:cancel])
		out.Cleared = unsettledWhere(&upTo, func(m *WorkflowMessage) bool { return m.Steer.ToSession == session })
	}
	return out
}

// rewakingCancel reports a reader's cancel emptying session's buffer of a workflow message.
func rewakingCancel(f *WorkflowMessageFact, session string) bool {
	c, ok := f.Clear()
	return ok && c.Cause == SteerClearCauseCancel && c.Session == session &&
		slices.ContainsFunc(c.IDs, IsWorkflowMessageNotifyID)
}

func unsettledWhere(w *WorkflowMessageFold, keep func(*WorkflowMessage) bool) []WorkflowMessage {
	var out []WorkflowMessage
	rows := w.Unsettled()
	for i := range rows {
		if keep(&rows[i]) {
			out = append(out, rows[i])
		}
	}
	return out
}

// shortRunIDLength is `wf_` plus eight characters, the client's runLabelText fallback.
const shortRunIDLength = 11

// WakeRunName is what a wake calls a run: its recorded label, else the id's short form.
func WakeRunName(run, label string) string {
	if label != "" {
		return label
	}
	if len(run) > shortRunIDLength {
		return run[:shortRunIDLength]
	}
	return run
}

// WakeFallbackLine is the line of a wake that names nothing marotte can show.
const WakeFallbackLine = "Agent-initiated turn"

// WakeLine is the wake's one-line label and the message under it ("" when it has none). The
// client's static-src/turn-wake.ts wakeText is its twin; internal/chat/testdata/wake_line.json
// pins both.
func WakeLine(w *EntryTurnWake) (label, body string) {
	switch w.Reason {
	case WakeReasonStepMessage:
		return stepWakeLine(w)
	case WakeReasonRunFinished:
		return runFinishedLine(w), ""
	case WakeReasonRewake:
		return rewakeLine(w.Steers)
	case WakeReasonUnknown:
	}
	return WakeFallbackLine, ""
}

func stepWakeLine(w *EntryTurnWake) (label, body string) {
	if len(w.Steers) == 0 {
		return WakeFallbackLine, ""
	}
	s := &w.Steers[0]
	run := WakeRunName(s.Run, s.RunLabel)
	if w.Note == WakeNoteRepeatCap && w.Loop != "" && w.MaxIterations > 0 {
		return run + " paused: " + w.Loop + " reached its round limit (" + strconv.Itoa(w.MaxIterations) + ")", ""
	}
	from := run + " \u00b7 " + s.Step
	switch {
	case w.Note == WakeNoteNetwork:
		return from + " is waiting for the network", s.Text
	case s.Severity == "warning":
		return from + " asks you", s.Text
	case s.Severity == "error":
		return from + " reports an error", s.Text
	}
	return from + " to you", s.Text
}

func runFinishedLine(w *EntryTurnWake) string {
	if w.Run == "" {
		return WakeFallbackLine
	}
	run := WakeRunName(w.Run, w.RunLabel)
	byUser := w.Initiator == WakeInitiatorUser
	var line string
	switch w.RunStatus {
	case RunStatusCompleted:
		if byUser {
			return run + " marked complete by you"
		}
		line = run + " completed"
	case RunStatusFailed:
		line = run + " failed"
	case RunStatusAborted, RunStatusCancelled:
		line = run + " stopped"
		if byUser {
			line += " by you"
		}
	case RunStatusRunning, RunStatusPaused:
		return WakeFallbackLine
	default:
		return WakeFallbackLine
	}
	if w.StopReason != "" {
		line += ": " + w.StopReason
	}
	return line
}

func rewakeLine(steers []WakeSteer) (label, body string) {
	if len(steers) == 0 {
		return WakeFallbackLine, ""
	}
	label = "Resumed with 1 workflow message you stopped before it was read"
	if len(steers) > 1 {
		label = "Resumed with " + strconv.Itoa(len(steers)) + " workflow messages you stopped before they were read"
	}
	lines := make([]string, 0, len(steers))
	for i := range steers {
		s := &steers[i]
		lines = append(lines, WakeRunName(s.Run, s.RunLabel)+" \u00b7 "+s.Step+": "+s.Text)
	}
	return label, strings.Join(lines, "\n")
}
