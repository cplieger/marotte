package translate

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	peerChat    marotte.ChatID = "c2"
	peerSession                = "sess_peer"
)

// peerDeps is messageDeps with a second resident chat, peerChat on peerSession.
func peerDeps(t *testing.T) (*baseDeps, *Translator) {
	t.Helper()
	deps, tr := messageDeps(t)
	deps.sessionChats = map[string]marotte.ChatID{peerSession: peerChat, testParent: testChat}
	return deps, tr
}

// peerNotify is KAS's notify for one chat's send_message to another chat's session.
func peerNotify(caller, target, severity, text, notifyID string) *marotte.RPCResponse {
	return notif("_kiro/session/notify", map[string]any{
		"sessionId": target, "callerSessionId": caller, "message": text, "severity": severity,
		"sender": "peer", "notifyId": notifyID, "deliveryCount": 1,
	})
}

func peerRows(t *testing.T, deps *baseDeps, chat marotte.ChatID) []steerRow {
	t.Helper()
	var out []steerRow
	for _, e := range deps.turns.chatLog[chat] {
		if e.Kind == marotte.EntryKindSteer && strings.HasPrefix(e.ID, marotte.WorkflowMessageIDPrefix) {
			out = append(out, steerRow{ID: e.ID, EntrySteer: decodePayload[marotte.EntrySteer](t, &e)})
		}
	}
	return out
}

// A peer message is one send with both ends: the receiver's row names the sending chat and the sender's copy the
// receiving one, so neither reads as an agent note.
func TestWorkflowMessage_APeerMessageIsARowInBothChats(t *testing.T) {
	deps, tr := peerDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, peerNotify(peerSession, testParent, "info", "the schema moved", "notify-p1"))

	got, sent := peerRows(t, deps, testChat), peerRows(t, deps, peerChat)
	if len(got) != 1 || len(sent) != 1 {
		t.Fatalf("rows: receiver %d, sender %d, want one each", len(got), len(sent))
	}
	if got[0].ID != sent[0].ID {
		t.Errorf("row ids differ: receiver %q, sender %q", got[0].ID, sent[0].ID)
	}
	if r := got[0].EntrySteer; r.Origin != marotte.SteerOriginPeer || r.Chat != peerChat || r.ToSession != testParent || r.KASID != "notify-p1" {
		t.Errorf("receiver row = %+v, want a peer message from %s sent to %s under notify-p1", r, peerChat, testParent)
	}
	if s := sent[0].EntrySteer; s.Origin != marotte.SteerOriginPeer || s.Chat != testChat || s.ToSession != "" || s.Step != "" {
		t.Errorf("sender row = %+v, want a peer message to %s with no receiver session", s, testChat)
	}
}

// A sender marotte holds no chat for still leaves the receiver its row, naming no chat.
func TestWorkflowMessage_APeerFromAnUnknownSessionIsTheReceiversRowOnly(t *testing.T) {
	deps, tr := peerDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, peerNotify("sess_gone", testParent, "info", "hello", "notify-p1"))

	got := peerRows(t, deps, testChat)
	if len(got) != 1 || got[0].Origin != marotte.SteerOriginPeer || got[0].Chat != "" {
		t.Errorf("receiver rows = %+v, want one peer row naming no chat", got)
	}
	if sent := peerRows(t, deps, peerChat); len(sent) != 0 {
		t.Errorf("sender rows = %+v, want none", sent)
	}
}

// KAS wakes an idle receiver for every peer message, success included (deliverSendMessageAdmitted buffers only a
// step's success), so the wake turn delivers it in both chats.
func TestWorkflowMessage_APeerSuccessIsDeliveredByItsWake(t *testing.T) {
	deps, tr := peerDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, peerNotify(peerSession, testParent, "success", "done on my side", "notify-p1"))

	chatWake(t, tr, "send-message-wake")

	got := peerRows(t, deps, testChat)
	if len(got) != 1 {
		t.Fatalf("receiver rows = %+v, want one", got)
	}
	if d := chatLogStamps(t, deps)[got[0].ID]; !deliveredOnce(d) {
		t.Errorf("receiver stamps = %+v, want one delivered at the wake", d)
	}
	var mirrored []marotte.EntrySteerDelivered
	for _, d := range deliveredOf(t, deps.turns.chatLog[peerChat]) {
		if d.SteerID == got[0].ID {
			mirrored = append(mirrored, d)
		}
	}
	if !deliveredOnce(mirrored) || mirrored[0].KASID != "" {
		t.Errorf("sender stamps = %+v, want the sender's copy delivered without KAS's id", mirrored)
	}
}
