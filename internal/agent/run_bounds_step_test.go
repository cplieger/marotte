package agent

// The idle window's evidence rule is scoped to the STEP, not to the carrier chat.
// A parallel run is several step turns on one carrier, so a chat-scoped question
// answers true while ANY of them holds a terminal — and a step whose own tool call is
// hung then reads as working because a sibling step is compiling, or because the
// carrier chat's own conversation holds a shell. The link that carries the step is
// the ACP session: the run log records it on each step's turn_open, and
// terminal/create carries it on the wire.

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// terminalsBySession is the sessions of each chat's live terminals, so a case can
// put a terminal on one of the run's own steps and a case can put one on the carrier
// chat and nothing else — which is the pair the scope decides between.
type terminalsBySession map[marotte.ChatID][]string

func (t terminalsBySession) LiveTerminalForSession(chatID marotte.ChatID, sessions map[string]struct{}) bool {
	return slices.ContainsFunc(t[chatID], func(s string) bool {
		_, ok := sessions[s]
		return ok
	})
}

// openStep opens one step turn of the run on its own session, the state a
// node_start leaves behind.
func openStep(t *testing.T, h *Runtime, workflowID, nodePath, sessionID string) {
	t.Helper()
	if _, _, err := h.runs.log.Open(t.Context(), workflowID, nodePath, sessionID, runChatID(workflowID)); err != nil {
		t.Fatalf("open step %s on %s: %v", nodePath, sessionID, err)
	}
}

// TestCancelExpired_AStepWaitingOnItsOwnCommandStillYields is the guard on the
// whole unit and runs before anything else in it: a step holding a terminal on its
// OWN session is working, and cancelling it at runIdleWindow is the regression the
// landed run-bounds rewrite exists to prevent (run_bounds.go's cancelExpired doc).
//
// It is also the case that reddens if the narrowing is built on the run's step TURN
// ids instead of their sessions — the two id spaces are disjoint (a terminal records
// the CHAT registry's turn, while OpenSeqs hands out run-log ids), so a turn-keyed
// version answers false for every run and never yields.
func TestCancelExpired_AStepWaitingOnItsOwnCommandStillYields(t *testing.T) {
	h, _, br := newTestHub()
	const (
		id      = "wf_1"
		stepSID = "step-session-a"
	)
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, "root/a", stepSID)
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

// TestCancelExpired_AnUnrelatedSessionOnTheCarrierDoesNotHoldTheWindow is the
// parallel-step case: two step turns on one carrier, neither of them holding a
// terminal, while the carrier chat holds one on a session that is not a step's.
// Under the chat-scoped rule that terminal answers for the whole run, so a run
// producing nothing was bounded by runBackstop's 36 hours instead of by the idle
// window. Under the step-scoped rule the run is bounded at 15 minutes.
func TestCancelExpired_AnUnrelatedSessionOnTheCarrierDoesNotHoldTheWindow(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, "root/a", "step-session-a")
	openStep(t, h, id, "root/b", "step-session-b")
	// A terminal on the carrier chat, on a session none of the run's steps names.
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

// TestStepWorking_EveryAbsenceAnswersFalse: false means no evidence of work, so the
// bound applies. A registry that is not wired, a run this process holds no open turn
// for, and a step whose turn_open carried no session are three ways to know nothing,
// and each of them must bound the run rather than lift the bound.
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
			openStep(t, h, id, "root/a", "step-session-a")
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
			openStep(t, h, id, "root/a", "")
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

// TestLiveTerminalForSession_ReadsTheCreatesOwnSession pins the registry half of the
// narrowing, which the bound cannot see through its fake: the answer is per TERMINAL,
// so a live terminal on an unasked session and an exited one on an asked session both
// answer false, and a session-less terminal is never a member of any set.
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
