package agent

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// twoChatsWithMessaging is two open chats, c1 and c2, on a runtime whose config.json has Messages between chats on.
func twoChatsWithMessaging(t *testing.T) (*Runtime, *testChatStore, [2]*sharedBridge) {
	t.Helper()
	configDir := t.TempDir()
	writeSetting(t, configDir, settings.KeyChatMessaging, true)
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil),
		cs, WithConfigDir(configDir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	var open [2]*sharedBridge
	for i, id := range []marotte.ChatID{"c1", "c2"} {
		seedChat(t, cs, id)
		sb, err := h.coord.openReleased(t.Context(), id, "")
		if err != nil {
			t.Fatalf("Setup: open %s: %v", id, err)
		}
		open[i] = sb
	}
	return h, cs, open
}

func peerRowsOf(t *testing.T, cs *testChatStore, id marotte.ChatID) []marotte.EntrySteer {
	t.Helper()
	var out []marotte.EntrySteer
	for _, e := range logOf(t, cs, id) {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var s marotte.EntrySteer
		decodePayload(t, e, &s)
		if s.Origin == marotte.SteerOriginPeer {
			out = append(out, s)
		}
	}
	return out
}

// With the setting on, both chats' sessions open with messaging on (KAS authorizes a peer send_message only when the
// sender's and the target's are both on), and the message KAS delivers is a From row in the receiving chat and a To
// row in the sending one.
func TestChatMessaging_APeerMessageBetweenOpenChatsIsDelivered(t *testing.T) {
	h, cs, open := twoChatsWithMessaging(t)
	for i, sb := range open {
		if opts := fakeOf(sb).lastSessionOpts(); opts == nil || !opts.Features.Messaging {
			t.Fatalf("chat %d opened with %+v, want messaging on", i+1, opts)
		}
	}
	receiver, sender := open[0].SessionID(), open[1].SessionID()
	if receiver == "" || sender == "" || receiver == sender {
		t.Fatalf("Setup: sessions %q and %q, want two distinct ids", receiver, sender)
	}

	h.translateACPEvent("c1", h.originOf("c1"), runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": string(receiver), "callerSessionId": string(sender), "message": "the schema moved",
		"severity": "info", "sender": "peer", "notifyId": "notify-p1", "deliveryCount": 1,
	}))
	// Each open chat's forward loop is inflight work until its session closes, so the rows are awaited by their effect.
	waitFor(t, func() bool { return len(peerRowsOf(t, cs, "c1")) > 0 && len(peerRowsOf(t, cs, "c2")) > 0 })

	got, sent := peerRowsOf(t, cs, "c1"), peerRowsOf(t, cs, "c2")
	if len(got) != 1 || got[0].Chat != "c2" || got[0].Text != "the schema moved" {
		t.Errorf("c1's peer rows = %+v, want one from c2 carrying the message", got)
	}
	if len(sent) != 1 || sent[0].Chat != "c1" {
		t.Errorf("c2's peer rows = %+v, want one to c1", sent)
	}
}
