package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// stepInfoMsg is a step session's session_info_update as KAS composes one: no _meta.kiro.workflow,
// so a chat bridge attributes it by session id alone.
func stepInfoMsg(t *testing.T, session string, kiro map[string]any) *marotte.RPCResponse {
	t.Helper()
	update := mustJSON(t, map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"kiro": kiro}})
	params := mustJSON(t, map[string]any{"sessionId": session, "update": update})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

func stepTurnStart(t *testing.T, session string) *marotte.RPCResponse {
	t.Helper()
	return stepInfoMsg(t, session, map[string]any{"kind": "turn_start", "turnStart": true})
}

func stepTurnEnd(t *testing.T, session string) *marotte.RPCResponse {
	t.Helper()
	return stepInfoMsg(t, session, map[string]any{"kind": "turn_end", "turnEnd": map[string]any{"stopReason": "end_turn"}})
}

// A step's turn brackets reach the ask core through the door its frames arrive at, a parentless
// run's carrier or the launching chat's bridge: turn_start moves the ask's id and settles an
// answer marotte sent, and turn_end fixes the warning that turn sent as the question.
func TestRunAsks_TurnBracketsArriveThroughEitherDoor(t *testing.T) {
	doors := map[string]func(*testing.T) (h *Runtime, door marotte.ChatID, workflowID, session string){
		"a parentless run's carrier": func(t *testing.T) (*Runtime, marotte.ChatID, string, string) {
			h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
			return h, runChatID("wf_1"), "wf_1", "sess_step"
		},
		"the launching chat's bridge": func(t *testing.T) (*Runtime, marotte.ChatID, string, string) {
			h, _ := chatParentedHub(t, runlease.Lease{WorkflowID: recordedRun, ChatID: "c1", ParentSessionID: "sess_parent"})
			return h, "c1", recordedRun, recordedSession
		},
	}
	for name, open := range doors {
		t.Run(name, func(t *testing.T) {
			h, door, wf, session := open(t)
			send := func(msg *marotte.RPCResponse) { h.translateACPEvent(door, h.originOf(door), msg) }
			c := h.runs.cellFor(wf)
			// The first read also teaches the translator the step's session, as a live run's does.
			before := readNow(t, h, wf).asks[0].ID

			send(stepTurnStart(t, session))
			send(runNotif(methodKiroSessionNotify, map[string]any{
				"sessionId": "sess_parent", "callerSessionId": session, "message": "Which branch?",
				"severity": "warning", "sender": "step", "workflowId": wf,
			}))
			send(stepTurnEnd(t, session))
			joinInflight(t, h)

			a := readNow(t, h, wf).asks[0]
			if a.ID == before {
				t.Error("the step's turn_start left the ask's id unchanged, so an answer typed before it is not stale")
			}
			if a.Text != "Which branch?" || a.TextSource != marotte.RunAskTextAsked {
				t.Errorf("ask text = %q (%s), want the warning the turn ended on", a.Text, a.TextSource)
			}

			if err := h.runs.answerInput(t.Context(), wf, a.ID, "teal"); err != nil {
				t.Fatalf("answer = %v", err)
			}
			if c.pendingOf() == nil {
				t.Fatal("Setup: the admitted answer left no pending send")
			}
			send(stepTurnStart(t, session))
			if c.pendingOf() != nil {
				t.Error("the answered step's turn_start left pending set, so the card stays disabled")
			}
		})
	}
}
