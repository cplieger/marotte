package translate

// Each case pins a gate: only a step's own warning with words is its question.

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// notifyParams is the shape KAS puts on `_kiro/session/notify`, with the values a
// test wants to vary.
type notifyParams struct {
	severity   string
	workflowID string
	nodeID     string
	message    string
	caller     string
	// sender is "step" when empty; noSender omits the key, as an older engine does.
	sender string
}

const noSender = "-"

func notifyFrame(p notifyParams) *marotte.RPCResponse {
	params := map[string]any{
		"sessionId":       testParent,
		"callerSessionId": p.caller,
		"message":         p.message,
		"severity":        p.severity,
		"workflowId":      p.workflowID,
		"nodeId":          p.nodeID,
	}
	switch p.sender {
	case "":
		params["sender"] = "step"
	case noSender:
	default:
		params["sender"] = p.sender
	}
	return notif("_kiro/session/notify", params)
}

func TestSessionNotifySignal_Asks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params notifyParams
		wantOK bool
	}{
		{
			// The one severity that parks a run: KAS maps `warning` to the
			// `need_input` completion signal and the run loop stops on it.
			name:   "a warning with a run and a message is an ask",
			params: notifyParams{severity: "warning", workflowID: "wf_1", message: "which branch?"},
			wantOK: true,
		},
		{
			// `info` changes nothing in the lifecycle: nobody is waiting.
			name:   "info parks nothing, so it is not an ask",
			params: notifyParams{severity: "info", workflowID: "wf_1", message: "halfway"},
		},
		{
			// `success` ADVANCES the step. Same reasoning, opposite direction.
			name:   "success advances the step",
			params: notifyParams{severity: "success", workflowID: "wf_1", message: "done"},
		},
		{
			// `error` fails the node; the failure reaches the card through run state.
			name:   "error fails the node",
			params: notifyParams{severity: "error", workflowID: "wf_1", message: "broke"},
		},
		{
			// No question: nothing to act on (the restart path's empty-question ask is separate).
			name:   "an empty message carries no question",
			params: notifyParams{severity: "warning", workflowID: "wf_1"},
		},
		{
			// A session messaging a CHILD run's step is not a question to the reader.
			name: "a parent's message to a child step is not an ask",
			params: notifyParams{
				severity: "warning", workflowID: "wf_1", message: "carry on", sender: "parent",
			},
		},
		{
			name: "an absent sender is not a step's question",
			params: notifyParams{
				severity: "warning", workflowID: "wf_1", message: "which branch?", sender: noSender,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := New(rolesOf(newBaseDeps()))
			got, ok := tr.SessionNotifySignal(notifyFrame(tc.params))
			if !ok {
				t.Fatalf("SessionNotifySignal(%+v) ok = false, want every frame naming a run read", tc.params)
			}
			if got.Asks() != tc.wantOK {
				t.Errorf("Asks() = %v, want %v", got.Asks(), tc.wantOK)
			}
		})
	}
}

// A frame naming no run is a cross-session note: no run moved.
func TestSessionNotifySignal_NoRunIsNoSignal(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	if _, ok := tr.SessionNotifySignal(notifyFrame(notifyParams{severity: "warning", message: "hello"})); ok {
		t.Error("SessionNotifySignal(no workflow id) ok = true, want false")
	}
}

func TestSessionNotifySignal_CarriesTheCaller(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	got, ok := tr.SessionNotifySignal(notifyFrame(notifyParams{
		severity: "warning", workflowID: "wf_1", nodeID: "review",
		message: "which branch should I target?", caller: testStep,
	}))
	if !ok {
		t.Fatal("SessionNotifySignal(a warning) ok = false, want true")
	}
	// The caller is the step session whose turn the question belongs to.
	if got.CallerSessionID != testStep || got.WorkflowID != "wf_1" {
		t.Errorf("SessionNotifySignal = %+v, want wf_1 from %s", got, testStep)
	}
	if got.Message != "which branch should I target?" {
		t.Errorf("Message = %q, want the message verbatim", got.Message)
	}
}
