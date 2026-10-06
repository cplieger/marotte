package agent

// `user_message_id_assigned` at the dispatcher: its id rides `update._meta.kiro`, and it is attributed by session.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// userMessageIDParams is the whole `session/update` params, `_meta` nested in `update` as KAS sends it.
func userMessageIDParams(t *testing.T, sessionID, kasID string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": string(marotte.ACPUpdateSessionInfo),
			"_meta": map[string]any{
				"kiro": map[string]any{
					"kind":          "user_message_id_assigned",
					"userMessageId": kasID,
				},
			},
		},
	})
}

// bindsOf decodes every turn_bind in entries, in file order.
func bindsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryTurnBind {
	t.Helper()
	var out []marotte.EntryTurnBind
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnBind {
			continue
		}
		var b marotte.EntryTurnBind
		if err := json.Unmarshal(entries[i].Payload, &b); err != nil {
			t.Fatalf("decode turn_bind %q: %v", entries[i].ID, err)
		}
		out = append(out, b)
	}
	return out
}

// The id KAS assigns on the chat's own session names the persisted prompt, the only id
// `_kiro/checkpoint/revertMultiple` accepts.
func TestHandleSessionUpdate_StampsTheKASMessageIDFromTheChatsOwnSession(t *testing.T) {
	const chatID = marotte.ChatID("chat-own")
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	seedChat(t, cs, chatID)
	// The binding scopes the replay merge on the record's session.
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("parent-A")
		return true
	}); err != nil {
		t.Fatalf("recording the chat's session: %v", err)
	}
	h.stagePromptTurn(t, chatID)

	h.handleSessionUpdate(t.Context(), chatID, &marotte.RPCResponse{
		Method: "session/update",
		Params: userMessageIDParams(t, "parent-A", "kas-own"),
	})

	binds := bindsOf(t, logOf(t, cs, chatID))
	if len(binds) != 1 || binds[0].KASMessageID != "kas-own" {
		t.Fatalf("turn_binds = %+v, want one binding kas-own", binds)
	}
	if binds[0].SessionID != "parent-A" {
		t.Errorf("turn_bind session_id = %q, want the chat's session parent-A", binds[0].SessionID)
	}
}

// A step's answer prompt gets an id too, byte-identical to the chat's; only the session says it is not the
// reader's, and stamping it would point rewind at the wrong row.
func TestHandleSessionUpdate_AStepSessionsAssignedIDStampsNothing(t *testing.T) {
	const (
		chatID  = marotte.ChatID("chat-step-id")
		stepSID = "step-session-1"
	)
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, "wf_1", "build", "build")
	seedChat(t, cs, chatID)
	h.stagePromptTurn(t, chatID)

	h.handleSessionUpdate(t.Context(), chatID, &marotte.RPCResponse{
		Method: "session/update",
		Params: userMessageIDParams(t, stepSID, "kas-step"),
	})

	if binds := bindsOf(t, logOf(t, cs, chatID)); len(binds) != 0 {
		t.Errorf("turn_binds = %+v, want none: a step's id names a prompt in the step's own log", binds)
	}
}
