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
	// WakeNoteNetwork is KAS's note that a step is still retrying through connection errors.
	WakeNoteNetwork WakeNote = "network"
	// WakeNoteNetworkLost is KAS's note that a step stopped retrying them and the run paused.
	WakeNoteNetworkLost WakeNote = "network_lost"
	// WakeNoteRepeatCap is KAS's note that a repeat paused at its iteration limit; Loop and
	// MaxIterations come from the run's typed pause, not the note's sentence.
	WakeNoteRepeatCap WakeNote = "repeat_cap"
)

// WakeSteer is a workflow message a wake names, copied from its row (ID) when the wake was
// recorded, so the header and the rail read the wake alone. It carries no run name: the server
// knows none for an agent-launched run, and the client names the run from its run store.
type WakeSteer struct {
	ID       string `json:"id"`
	Run      string `json:"run,omitempty"`
	Step     string `json:"step,omitempty"`
	Severity string `json:"severity,omitempty"`
	Text     string `json:"text"`
}

// EntryTurnWake is the turn_wake payload: what started an agent-initiated turn, filed once at
// its first agentInitiated frame. It renders nothing at its position; the turn header folds it.
// Steers is the waking message for a step message and the re-delivered rows for a rewake; Run,
// RunLabel (KAS's runLabel, else workflowName), RunStatus, Initiator and StopReason describe a
// finished run; Note, Loop and MaxIterations qualify a step message.
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
// Waiting is the unsettled workflow messages sent to it since the previous turn opened and before
// this one did, oldest first; Cleared is the rows the newest reader's cancel since the previous
// turn opened cleared unread, when that cancel named a workflow message (KAS re-delivers those in
// the next turn); Racing is the Waiting rows filed after that cancel, which can have woken the
// chat before the re-delivery did.
type WakeCauses struct {
	Waiting []WorkflowMessage
	Cleared []WorkflowMessage
	Racing  []WorkflowMessage
}

// PositionedFact is a workflow-message fact and its position in its log, file order.
type PositionedFact struct {
	Fact WorkflowMessageFact
	At   int64
}

// WakeCausesOf reads WakeCauses for session from facts in file order, given where the open turn
// opened and where the turn before it opened (-1 for none). A row filed inside that previous turn
// still counts as waiting: KAS can end its turn and wake the chat before that turn's close lands.
// A row filed earlier is not: it had a turn to wake or be read in, and one left unsettled (a wake
// KAS failed to start) must not name every later wake.
func WakeCausesOf(facts []PositionedFact, session string, opened, prevOpened int64) WakeCauses {
	var out WakeCauses
	if session == "" {
		return out
	}
	filedAt := map[string]int{}
	cancel := -1
	ordered := make([]WorkflowMessageFact, 0, len(facts))
	for i := range facts {
		f := &facts[i]
		ordered = append(ordered, f.Fact)
		if f.At >= opened || f.At < prevOpened {
			continue
		}
		if m, ok := f.Fact.Row(); ok && m.Steer.ToSession == session {
			filedAt[m.ID] = i
		}
		if rewakingCancel(&f.Fact, session) {
			cancel = i
		}
	}
	all := FoldWorkflowMessages(ordered)
	out.Waiting = unsettledWhere(&all, func(m *WorkflowMessage) bool {
		_, ok := filedAt[m.ID]
		return ok
	})
	if cancel >= 0 {
		upTo := FoldWorkflowMessages(ordered[:cancel])
		out.Cleared = unsettledWhere(&upTo, func(m *WorkflowMessage) bool { return m.Steer.ToSession == session })
		out.Racing = slices.DeleteFunc(slices.Clone(out.Waiting), func(m WorkflowMessage) bool { return filedAt[m.ID] < cancel })
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

// WakeRunName is what a wake calls a run: label when known, else the id's short form.
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

// wakeLabel is a wake's label split where the client draws the run's name as a button: name
// (empty when the label opens with no run) then rest.
type wakeLabel struct {
	name string
	rest string
}

// WakeLine is the wake's one-line label and the message under it ("" when it has none). The
// client's static-src/turn-wake.ts wakeLine is its twin; internal/chat/testdata/wake_line.json
// pins both.
func WakeLine(w *EntryTurnWake) (label, body string) {
	l, body := wakeLineOf(w)
	return l.name + l.rest, body
}

// WakeDrawnSteers is the workflow-message rows a wake draws in its header, which draw nothing where they
// were filed: a step message's waker and a re-delivery's rows.
func WakeDrawnSteers(w *EntryTurnWake) []string {
	if w.Reason != WakeReasonStepMessage && w.Reason != WakeReasonRewake {
		return nil
	}
	out := make([]string, len(w.Steers))
	for i := range w.Steers {
		out[i] = w.Steers[i].ID
	}
	return out
}

// WakeSearchTexts is what find-in-chat matches in a wake's header: the label after the run's name,
// which the client draws from its run store, then each message; nil for the fallback line.
func WakeSearchTexts(w *EntryTurnWake) []string {
	l, _ := wakeLineOf(w)
	if l.name == "" && l.rest == WakeFallbackLine {
		return nil
	}
	out := []string{strings.TrimPrefix(strings.TrimPrefix(l.rest, ":"), " ")}
	if w.Reason == WakeReasonStepMessage || w.Reason == WakeReasonRewake {
		for i := range w.Steers {
			out = append(out, w.Steers[i].Text)
		}
	}
	return out
}

var fallbackLabel = wakeLabel{rest: WakeFallbackLine}

func wakeLineOf(w *EntryTurnWake) (label wakeLabel, body string) {
	switch w.Reason {
	case WakeReasonStepMessage:
		return stepWakeLine(w)
	case WakeReasonRunFinished:
		return runFinishedLine(w), ""
	case WakeReasonRewake:
		return rewakeLine(w.Steers)
	case WakeReasonUnknown:
	}
	return fallbackLabel, ""
}

func stepWakeLine(w *EntryTurnWake) (label wakeLabel, body string) {
	if len(w.Steers) == 0 {
		return fallbackLabel, ""
	}
	s := &w.Steers[0]
	run := WakeRunName(s.Run, "")
	if w.Note == WakeNoteRepeatCap && w.Loop != "" && w.MaxIterations > 0 {
		return wakeLabel{run, " paused: " + w.Loop + " reached its round limit (" + strconv.Itoa(w.MaxIterations) + ")"}, ""
	}
	from := " \u00b7 " + s.Step
	switch {
	case w.Note == WakeNoteNetwork:
		return wakeLabel{run, from + " is waiting for the network"}, s.Text
	case w.Note == WakeNoteNetworkLost:
		return wakeLabel{run, from + " lost the network"}, s.Text
	case s.Severity == "warning":
		return wakeLabel{run, from + " asks you"}, s.Text
	case s.Severity == "error":
		return wakeLabel{run, from + " reports an error"}, s.Text
	}
	return wakeLabel{run, from + " to you"}, s.Text
}

func runFinishedLine(w *EntryTurnWake) wakeLabel {
	if w.Run == "" {
		return fallbackLabel
	}
	run := WakeRunName(w.Run, w.RunLabel)
	byUser := w.Initiator == WakeInitiatorUser
	var rest string
	switch w.RunStatus {
	case RunStatusCompleted:
		if byUser {
			return wakeLabel{run, " marked complete by you"}
		}
		rest = " completed"
	case RunStatusFailed:
		rest = " failed"
	case RunStatusAborted, RunStatusCancelled:
		rest = " stopped"
		if byUser {
			rest += " by you"
		}
	case RunStatusRunning, RunStatusPaused:
		return fallbackLabel
	default:
		return fallbackLabel
	}
	if w.StopReason != "" {
		rest += ": " + w.StopReason
	}
	return wakeLabel{run, rest}
}

func rewakeLine(steers []WakeSteer) (label wakeLabel, body string) {
	if len(steers) == 0 {
		return fallbackLabel, ""
	}
	rest := "Resumed with 1 workflow message you stopped before it was read"
	if len(steers) > 1 {
		rest = "Resumed with " + strconv.Itoa(len(steers)) + " workflow messages you stopped before they were read"
	}
	lines := make([]string, 0, len(steers))
	for i := range steers {
		s := &steers[i]
		lines = append(lines, WakeRunName(s.Run, "")+" \u00b7 "+s.Step+": "+s.Text)
	}
	return wakeLabel{rest: rest}, strings.Join(lines, "\n")
}
