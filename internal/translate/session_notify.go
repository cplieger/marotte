package translate

// `_kiro/session/notify`: KAS's `send_message` builtin, the ONLY frame carrying a workflow
// step's question (the run pause and the steering-buffer copy carry no text). This file is a
// PURE derivation from frame to ask; the host (internal/agent/run_ask.go) owns the broadcast,
// registry and answer path.

import (
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
)

// `sessionId` is the target and `callerSessionId` the sender. On a step's question the caller is
// the PAUSED STEP's session; a session/prompt there is rerouted into the run by KAS.
type kasSessionNotify struct {
	SessionID       string `json:"sessionId"`
	CallerSessionID string `json:"callerSessionId"`
	Message         string `json:"message"`
	Severity        string `json:"severity"`
	WorkflowID      string `json:"workflowId"`
	NodeID          string `json:"nodeId"`
	AgentName       string `json:"agentName"`
	NotifyID        string `json:"notifyId"`
	// Sender is "step" when a step messages its parent and "parent" when a session
	// messages a child run's step; only the first is a question to the reader.
	Sender string `json:"sender"`
}

const (
	senderStep   = "step"
	senderParent = "parent"
)

// severityWarning is the ONE severity that parks a run (KAS maps it to `need_input`); the
// others leave nobody waiting.
const severityWarning = "warning"

// SessionNotifyAsk derives the run ask a notify frame carries, or false. Gates: severity
// `warning`, sender `step`, a `workflowId`, a non-empty `message`. The node id comes from the
// frame, else from the step-session registry by `callerSessionId`. It returns the payload
// rather than broadcasting.
func (t *Translator) SessionNotifyAsk(msg *marotte.RPCResponse) (marotte.RunInputNeededPayload, bool) {
	p, ok := unmarshalParams[kasSessionNotify](msg, "_kiro/session/notify")
	if !ok || p.Severity != severityWarning || p.Sender != senderStep ||
		p.WorkflowID == "" || p.Message == "" {
		return marotte.RunInputNeededPayload{}, false
	}
	node := p.NodeID
	if node == "" {
		node = t.steps.refFor(p.CallerSessionID).NodeID
	}
	return marotte.RunInputNeededPayload{
		WorkflowID: p.WorkflowID,
		// The notification's own id when sent, else the caller session plus the message: stable for
		// a redelivered frame, distinct for a second question.
		AskID:         askIDOf(&p),
		NodeID:        node,
		StepSessionID: p.CallerSessionID,
		AgentName:     p.AgentName,
		Question:      p.Message,
		AskedAt:       time.Now().UTC().Format(time.RFC3339),
	}, true
}

// askIDOf composes a live ask's id with keyenc (every part is wire text). DETERMINISTIC, so a
// redelivered frame is idempotent.
func askIDOf(p *kasSessionNotify) string {
	if p.NotifyID != "" {
		return keyenc.Join("notify", p.NotifyID)
	}
	return keyenc.Join("ask", p.CallerSessionID, p.Message)
}
