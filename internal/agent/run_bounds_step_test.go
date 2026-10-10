package agent

// The idle window's evidence is scoped to the step, not the carrier chat: a parallel run is several step
// turns on one carrier. The link is the ACP session, recorded on each
// step's turn_open and carried by terminal/create.

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// terminalsBySession is each chat's live-terminal sessions, so a case can put a terminal on a step or on the carrier alone.
type terminalsBySession map[marotte.ChatID][]string

func (t terminalsBySession) LiveTerminalForSession(chatID marotte.ChatID, sessions map[string]struct{}) bool {
	return slices.ContainsFunc(t[chatID], func(s string) bool {
		_, ok := sessions[s]
		return ok
	})
}

func openStep(t *testing.T, h *Runtime, workflowID string, path []string, sessionID string) {
	t.Helper()
	step := &translate.RunStep{RunID: workflowID, NodePath: workflow.PathKey(path), NodeID: path[len(path)-1], SessionID: sessionID}
	if _, _, err := h.runs.log.open(t.Context(), step, runChatID(workflowID)); err != nil {
		t.Fatalf("open step %q on %s: %v", path, sessionID, err)
	}
}

// TestCancelExpired_AStepWaitingOnItsOwnCommandStillYields pins that a step holding a terminal on its own session
// is working. It also catches a narrowing keyed on turn ids, a disjoint id space from the sessions.
func TestCancelExpired_AStepWaitingOnItsOwnCommandStillYields(t *testing.T) {
	h, _, br := newTestHub()
	const (
		id      = "wf_1"
		stepSID = "step-session-a"
	)
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, []string{"root", "a"}, stepSID)
	h.runs.terminals = terminalsBySession{runChatID(id): {stepSID}}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != "" {
		t.Errorf("a step waiting on its OWN command was cancelled %q; want the window refilled", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a run whose own step holds a terminal: %v", br.callLog())
	}
	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("the refill left %v of budget, want a full idle window %v",
			budget.Round(time.Second), runIdleWindow)
	}
}

// TestCancelExpired_AnUnrelatedSessionOnTheCarrierDoesNotHoldTheWindow pins that a carrier terminal on a non-step
// session must not hold the window, or an idle run lasts the backstop instead of 15 minutes.
func TestCancelExpired_AnUnrelatedSessionOnTheCarrierDoesNotHoldTheWindow(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, []string{"root", "a"}, "step-session-a")
	openStep(t, h, id, []string{"root", "b"}, "step-session-b")
	// A carrier terminal on a session no step names.
	h.runs.terminals = terminalsBySession{runChatID(id): {"chat-session"}}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("endReason = %q, want %q: no step of this run holds a terminal, so the run is stalled "+
			"whatever else the carrier chat is running", got, runEndStalled)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for the stalled run: %v", br.callLog())
	}
}

// TestStepWorking_EveryAbsenceAnswersFalse pins that each way of knowing nothing must bound the run.
func TestStepWorking_EveryAbsenceAnswersFalse(t *testing.T) {
	const id = "wf_1"
	cases := []struct {
		arrange func(t *testing.T, h *Runtime)
		name    string
	}{{
		name: "no terminal registry",
		arrange: func(t *testing.T, h *Runtime) {
			h.runs.terminals = nil
			h.runs.log = newRunLog(t.TempDir())
			openStep(t, h, id, []string{"root", "a"}, "step-session-a")
		},
	}, {
		name: "no open step turn",
		arrange: func(t *testing.T, h *Runtime) {
			h.runs.terminals = terminalsBySession{runChatID(id): {"step-session-a"}}
			h.runs.log = newRunLog(t.TempDir())
		},
	}, {
		name: "a step with no session",
		arrange: func(t *testing.T, h *Runtime) {
			h.runs.terminals = terminalsBySession{runChatID(id): {""}}
			h.runs.log = newRunLog(t.TempDir())
			openStep(t, h, id, []string{"root", "a"}, "")
		},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			tc.arrange(t, h)
			leased(t, h.runs, id)

			if h.runs.stepWorking("", id) {
				t.Errorf("stepWorking(%q) = true with %s; want false, so the run stays bounded",
					id, tc.name)
			}
		})
	}
}

// TestLiveTerminalForSession_ReadsTheCreatesOwnSession pins the per-terminal answer: an unasked live one,
// an asked exited one and a session-less one all answer false.
func TestLiveTerminalForSession_ReadsTheCreatesOwnSession(t *testing.T) {
	t.Parallel()
	const chat = marotte.ChatID("c1")
	at := bareTerminals()
	live := func(id, session string) {
		term := newAgentTerminal(nil, chat, 64)
		term.session = session
		at.terms[id] = term
		at.byChatID[chat] = append(at.byChatID[chat], id)
	}
	live("t-step", "step-session-a")
	live("t-chat", "chat-session")
	live("t-bare", "")
	exited := newAgentTerminal(nil, chat, 64)
	exited.session = "step-session-b"
	close(exited.done)
	at.terms["t-exited"] = exited
	at.byChatID[chat] = append(at.byChatID[chat], "t-exited")

	cases := []struct {
		name     string
		sessions []string
		want     bool
	}{
		{name: "a live terminal on an asked session", sessions: []string{"step-session-a"}, want: true},
		{name: "one asked session of several", sessions: []string{"step-session-a", "step-session-x"}, want: true},
		{name: "a live terminal on no asked session", sessions: []string{"step-session-x"}, want: false},
		{name: "an exited terminal on an asked session", sessions: []string{"step-session-b"}, want: false},
		{name: "no sessions asked about", sessions: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := make(map[string]struct{}, len(tc.sessions))
			for _, s := range tc.sessions {
				set[s] = struct{}{}
			}
			if got := at.LiveTerminalForSession(chat, set); got != tc.want {
				t.Errorf("LiveTerminalForSession(%q, %v) = %v, want %v", chat, tc.sessions, got, tc.want)
			}
		})
	}
}
