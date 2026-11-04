package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// postCmdWithin is postCmd on its own goroutine, failing the test when the command has not answered within 5 s.
func postCmdWithin(t *testing.T, h *Runtime, cmd marotte.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	rec := answeredWithin(t, func(w http.ResponseWriter) {
		h.handleCommand(w, httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body)))
	})
	return rec
}

func chatOf(t *testing.T, h *Runtime, id marotte.ChatID) *marotte.Chat {
	t.Helper()
	c, ok := h.chatStore.Get(t.Context(), id)
	if !ok {
		t.Fatalf("chat %s has no record", id)
	}
	return c
}

// A session change kiro-cli applies after the browser stops waiting is answered still_applying in time, and lands on
// the chat when kiro-cli answers: today's 30 s abort would report a change kiro-cli then made as failed.
func TestSessionChanges_AStalledEngineAnswersStillApplyingAndTheChangeLands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		cmdType marotte.CommandType
		payload string
		landed  func(h *Runtime) bool
	}{
		{
			name: "set_mode", method: marotte.MethodSetMode, cmdType: marotte.CmdSetMode,
			payload: `{"mode_id":"planner"}`,
			landed: func(h *Runtime) bool {
				c, _ := h.chatStore.Get(t.Context(), "c1")
				return c != nil && c.CurrentModeID == "planner" && len(eventsOfType(h, marotte.EventModeChanged)) == 1
			},
		},
		{
			name: "set_effort", method: marotte.MethodSetConfigOption, cmdType: marotte.CmdSetEffort,
			payload: `{"level":"high"}`,
			landed: func(h *Runtime) bool {
				c, _ := h.chatStore.Get(t.Context(), "c1")
				return c != nil && c.Effort == "high"
			},
		},
		{
			name: "set_supervised_mode", method: marotte.MethodSetConfigOption, cmdType: marotte.CmdSetSupervisedMode,
			payload: `{"enabled":true}`,
			landed: func(h *Runtime) bool {
				c, _ := h.chatStore.Get(t.Context(), "c1")
				return c != nil && c.SupervisedMode
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answerBudgetOf(t, 50*time.Millisecond)
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			openChat(t, h, "c1")
			release := holdCall(t, br, tc.method)

			res := postCmdWithin(t, h, marotte.ClientCommand{Type: tc.cmdType, ChatID: "c1", Payload: json.RawMessage(tc.payload)})
			assertCommandStillApplying(t, res)
			if tc.landed(h) {
				t.Fatal("the change landed before kiro-cli answered")
			}
			release()
			waitFor(t, func() bool { return tc.landed(h) })
		})
	}
}

// A tangent kiro-cli forks after the browser stops waiting opens bound to that forked session, never as an unbound
// tangent beside an orphaned one.
func TestForkChat_AStalledEngineAnswersStillApplyingAndTheTangentBindsTheFork(t *testing.T) {
	answerBudgetOf(t, 50*time.Millisecond)
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("tabs.NewStore: %v", err)
	}
	h, _, br := newTestHub(WithTabs(st))
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	br.setCallResult(marotte.MethodSessionFork, json.RawMessage(`{"sessionId":"sess_forked"}`))
	release := holdCall(t, br, marotte.MethodSessionFork)

	res := postCmdWithin(t, h, marotte.ClientCommand{
		Type:    marotte.CmdForkChat,
		Payload: json.RawMessage(`{"parent_chat_id":"c1","op_id":"op-fork"}`),
	})
	assertCommandStillApplying(t, res)
	release()
	waitFor(t, func() bool {
		return slices.ContainsFunc(h.chatStore.List(t.Context()), func(c marotte.ChatHeader) bool {
			return c.ID != "c1" && chatOf(t, h, marotte.ChatID(c.ID)).ACPSessionID == "sess_forked"
		})
	})
}

// A checkpoint revert kiro-cli applies after the browser stops waiting is recorded on the chat, so the transcript
// matches the files kiro-cli wrote back.
func TestRewindChat_AStalledEngineAnswersStillApplyingAndTheRevertIsRecorded(t *testing.T) {
	answerBudgetOf(t, 50*time.Millisecond)
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	runPrompt(t, h, cs, "c1", "m-1")
	br.setCallResult(marotte.MethodCheckpointRevertMultiple, json.RawMessage(`{"success":true}`))
	release := holdCall(t, br, marotte.MethodCheckpointRevertMultiple)

	res := postCmdWithin(t, h, marotte.ClientCommand{
		Type: marotte.CmdRewindChat, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-1"}`),
	})
	assertCommandStillApplying(t, res)
	release()
	waitFor(t, func() bool {
		return slices.ContainsFunc(logOf(t, cs, "c1"), func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindTurnRevert })
	})
}

// A compaction kiro-cli runs after the browser stops waiting is answered still_applying rather than as a failed call.
func TestCompact_AStalledEngineAnswersStillApplying(t *testing.T) {
	answerBudgetOf(t, 50*time.Millisecond)
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	release := holdCall(t, br, marotte.MethodSessionCompact)

	res := postCmdWithin(t, h, marotte.ClientCommand{Type: marotte.CmdCompact, ChatID: "c1"})
	assertCommandStillApplying(t, res)
	release()
}

// A model switch whose tier stop kiro-cli applies after the browser stops waiting is answered still_applying, and
// the stop lands with the switch: the stop runs inside the switch, so only the switch's request may answer for it.
func TestSwitchModel_AStalledEngineAnswersStillApplyingAndTheStopLands(t *testing.T) {
	answerBudgetOf(t, 50*time.Millisecond)
	d, h := switchFixture(t)
	release := holdCall(t, fakeOf(h.coord.bridgeFor("c1")), marotte.MethodSetConfigOption)

	res := postCmdWithin(t, h, marotte.ClientCommand{
		Type: marotte.CmdSwitchModel, ChatID: "c1", Payload: json.RawMessage(`{"model":"fake-model","effort":"high"}`),
	})
	assertCommandStillApplying(t, res)
	release()
	waitFor(t, func() bool {
		c, _ := d.cs.Get(t.Context(), "c1")
		return c != nil && c.Effort == "high"
	})
}

// A refusal kiro-cli answers in time stays the error it is, with nothing persisted.
func TestSetMode_AnOnTimeRefusalStaysAnError(t *testing.T) {
	answerBudgetOf(t, 5*time.Second)
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	br.setCallErr(marotte.MethodSetMode, errors.New("ACP error -32602: no such mode"))

	res := postCmdWithin(t, h, marotte.ClientCommand{
		Type: marotte.CmdSetMode, ChatID: "c1", Payload: json.RawMessage(`{"mode_id":"planner"}`),
	})
	if res.Code != http.StatusBadGateway {
		t.Errorf("an on-time refusal answered %d, want 502", res.Code)
	}
	if got := chatOf(t, h, "c1").CurrentModeID; got == "planner" {
		t.Error("a refused mode was persisted")
	}
}

func assertCommandStillApplying(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.Code != http.StatusGatewayTimeout || body.Reason != "still_applying" {
		t.Errorf("answer = %d reason %q, want 504 still_applying: kiro-cli may still apply the change", res.Code, body.Reason)
	}
}
