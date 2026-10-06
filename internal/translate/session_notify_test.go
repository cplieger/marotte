package translate

// Each case pins a gate: the three non-parking severities must not produce cards.

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
	agentName  string
	message    string
	caller     string
	notifyID   string
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
		"agentName":       p.agentName,
		"notifyId":        p.notifyID,
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

func TestSessionNotifyAsk_Gates(t *testing.T) {
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
			// No run: a cross-session note, nothing to answer into.
			name:   "no workflow id is a cross-session note, not a run ask",
			params: notifyParams{severity: "warning", message: "hello"},
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
			got, ok := tr.SessionNotifyAsk(notifyFrame(tc.params))
			if ok != tc.wantOK {
				t.Fatalf("SessionNotifyAsk(%+v) ok = %v, want %v", tc.params, ok, tc.wantOK)
			}
			if !ok && got.WorkflowID != "" {
				t.Errorf("SessionNotifyAsk(%+v) returned a payload on a dropped frame: %+v",
					tc.params, got)
			}
		})
	}
}

func TestSessionNotifyAsk_CarriesTheAnswerAddress(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	got, ok := tr.SessionNotifyAsk(notifyFrame(notifyParams{
		severity:   "warning",
		workflowID: "wf_1",
		nodeID:     "review",
		agentName:  "reviewer",
		message:    "which branch should I target?",
		caller:     testStep,
		notifyID:   "n1",
	}))
	if !ok {
		t.Fatal("SessionNotifyAsk(a warning) ok = false, want true")
	}
	// callerSessionId is where the answer is sent.
	if got.StepSessionID != testStep {
		t.Errorf("StepSessionID = %q, want %q", got.StepSessionID, testStep)
	}
	if got.WorkflowID != "wf_1" || got.NodeID != "review" || got.AgentName != "reviewer" {
		t.Errorf("SessionNotifyAsk = %+v, want wf_1/review/reviewer", got)
	}
	if got.Question != "which branch should I target?" {
		t.Errorf("Question = %q, want the message verbatim", got.Question)
	}
	if got.AskID == "" {
		t.Error("AskID = \"\", want an id an answer can name")
	}
	if got.AskedAt == "" {
		t.Error("AskedAt = \"\", want a timestamp")
	}
}

func TestSessionNotifyAsk_ResolvesTheNodeFromTheStepRegistry(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	// node_start is the only frame announcing a step's session.
	tr.RecordStepSession(testStep, "wf_1", "review", "review")

	got, ok := tr.SessionNotifyAsk(notifyFrame(notifyParams{
		severity:   "warning",
		workflowID: "wf_1",
		message:    "which branch?",
		caller:     testStep,
	}))
	if !ok {
		t.Fatal("SessionNotifyAsk(a warning) ok = false, want true")
	}
	if got.NodeID != "review" {
		t.Errorf("NodeID = %q, want %q resolved from the step registry", got.NodeID, "review")
	}
}

func TestSessionNotifyAsk_NoNodeIsStillAnAsk(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	// A cold registry and no nodeId: the ask survives with an empty node.
	got, ok := tr.SessionNotifyAsk(notifyFrame(notifyParams{
		severity:   "warning",
		workflowID: "wf_1",
		message:    "which branch?",
		caller:     "sess_unknown",
	}))
	if !ok {
		t.Fatal("SessionNotifyAsk(a warning with no resolvable node) ok = false, want true")
	}
	if got.NodeID != "" {
		t.Errorf("NodeID = %q, want \"\" when nothing can resolve it", got.NodeID)
	}
}

func TestSessionNotifyAsk_IDIsStableForOneFrame(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	p := notifyParams{
		severity: "warning", workflowID: "wf_1", message: "which branch?",
		caller: testStep, notifyID: "n1",
	}
	first, ok1 := tr.SessionNotifyAsk(notifyFrame(p))
	second, ok2 := tr.SessionNotifyAsk(notifyFrame(p))
	if !ok1 || !ok2 {
		t.Fatal("SessionNotifyAsk twice: want both ok")
	}
	// Deterministic, so a redelivered frame de-duplicates.
	if first.AskID != second.AskID {
		t.Errorf("AskID = %q then %q, want one stable id per frame", first.AskID, second.AskID)
	}
	// A different question from the same step gets its own id.
	p.message = "and which reviewer?"
	p.notifyID = "n2"
	third, _ := tr.SessionNotifyAsk(notifyFrame(p))
	if third.AskID == first.AskID {
		t.Errorf("two questions share AskID %q, want distinct ids", third.AskID)
	}
}

func TestSessionNotifyAsk_IDDistinguishesTwoQuestionsWithoutANotifyID(t *testing.T) {
	t.Parallel()
	tr := New(rolesOf(newBaseDeps()))
	base := notifyParams{severity: "warning", workflowID: "wf_1", caller: testStep}
	base.message = "which branch?"
	first, _ := tr.SessionNotifyAsk(notifyFrame(base))
	base.message = "which reviewer?"
	second, _ := tr.SessionNotifyAsk(notifyFrame(base))
	if first.AskID == second.AskID {
		t.Errorf("two questions share AskID %q with no notifyId, want distinct ids", first.AskID)
	}
}
