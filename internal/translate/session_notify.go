package translate

// `_kiro/session/notify`: KAS's `send_message` builtin, the ONLY frame carrying a workflow
// step's question text (the run pause and the steering-buffer copy carry none). This file reads
// the frame; the ask core (internal/agent/run_asks.go) decides what is waiting from inspect.

import "github.com/cplieger/marotte/internal/marotte"

// `sessionId` is the target and `callerSessionId` the sender. On a step's question the caller is
// the PAUSED STEP's session; a session/prompt there is rerouted into the run by KAS.
type kasSessionNotify struct {
	SessionID       string `json:"sessionId"`
	CallerSessionID string `json:"callerSessionId"`
	Message         string `json:"message"`
	Severity        string `json:"severity"`
	WorkflowID      string `json:"workflowId"`
	// Sender is "step" when a step messages its parent and "parent" when a session
	// messages a child run's step; only the first is a question to the reader.
	Sender string `json:"sender"`
}

// KAS's send_message roles, on the notify frame and on the row it persists.
const (
	senderStep   = "step"
	senderParent = "parent"
)

// severityWarning is the ONE severity that parks a run (KAS maps it to `need_input`); the
// others leave nobody waiting.
const severityWarning = "warning"

// NotifySignal is the part of a `_kiro/session/notify` frame naming a run that the ask core reads.
type NotifySignal struct {
	WorkflowID      string
	CallerSessionID string
	Severity        string
	Sender          string
	Message         string
}

// Asks reports whether the frame is a step's question to the reader: a `warning` the step sent,
// with words. Every other notify naming a run is still motion on it (a parent's message is how a
// deferred answer resumes the step).
func (s *NotifySignal) Asks() bool {
	return s.Severity == severityWarning && s.Sender == senderStep && s.Message != ""
}

// SessionNotifySignal reads a notify frame naming a run, false for one naming none.
func (*Translator) SessionNotifySignal(msg *marotte.RPCResponse) (NotifySignal, bool) {
	p, ok := unmarshalParams[kasSessionNotify](msg, "_kiro/session/notify")
	if !ok || p.WorkflowID == "" {
		return NotifySignal{}, false
	}
	return NotifySignal{
		WorkflowID: p.WorkflowID, CallerSessionID: p.CallerSessionID,
		Severity: p.Severity, Sender: p.Sender, Message: p.Message,
	}, true
}
