package agent

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"

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

// With the setting on, both chats' sessions open with messaging on, which KAS's authorizeTarget requires of sender and
// target; the delivery gate itself is KAS's. The notify half pins the pairing: a From row in the receiving chat and a
// To row in the sending one.
func TestChatMessaging_BothChatsOpenWithMessagingOnAndPairThePeerRows(t *testing.T) {
	t.Parallel()
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

// The door states the chat's value: the Settings switch, unless the chat chose out.
func TestChatMessaging_DoorStatesTheChatsChoice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting bool
		off     bool
		want    bool
	}{
		{name: "switch on, chat chose out", setting: true, off: true, want: false},
		{name: "switch off, chat at its default", setting: false, off: false, want: false},
		{name: "switch on, chat at its default", setting: true, off: false, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, configDir := reopenFixture(t)
			writeSetting(t, configDir, settings.KeyChatMessaging, tc.setting)
			if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.MessagingOff = tc.off
				return true
			}); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			sb, err := h.coord.openReleased(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			if got := fakeOf(sb).lastSessionOpts().Features.Messaging; got != tc.want {
				t.Errorf("the door's Features.Messaging = %v, want %v", got, tc.want)
			}
		})
	}
}

// openMessagingChat is c1 open on a runtime with Messages between chats on, and the count of messaging asks its
// session already had.
func openMessagingChat(t *testing.T) (*Runtime, *fakeBridge, int) {
	t.Helper()
	h, configDir := reopenFixture(t)
	writeSetting(t, configDir, settings.KeyChatMessaging, true)
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br := fakeOf(sb)
	return h, br, len(br.messagingAsks())
}

func setChatMessaging(t *testing.T, h *Runtime, enabled bool) {
	t.Helper()
	if code := postTyped(t, h, marotte.CmdSetChatMessaging, fmt.Sprintf(`{"enabled":%v}`, enabled)); code != http.StatusOK {
		t.Fatalf("set_chat_messaging = %d, want 200", code)
	}
}

func TestChatMessaging_AMidTurnFlipLandsAtTheTurnEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br, before := openMessagingChat(t)
		turnID, _ := h.stagePromptTurn(t, "c1")

		setChatMessaging(t, h, false)
		synctest.Wait()
		if got := br.messagingAsks()[before:]; len(got) != 0 {
			t.Fatalf("a flip during the turn asked the session %v before the turn closed, want nothing", got)
		}

		endTurn(t, h, "c1", turnID)
		synctest.Wait()
		if got := br.messagingAsks()[before:]; !slices.Equal(got, []bool{false}) {
			t.Errorf("after the turn's close the session was asked %v, want [false]", got)
		}
	})
}

func TestChatMessaging_ARefusalIsRetriedAtTheNextClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br, before := openMessagingChat(t)
		br.refuseNextMessaging(fmt.Errorf("ACP error %d: %w", kasRefusedMidTurnCode,
			&marotte.RPCError{Code: kasRefusedMidTurnCode, Message: "a turn is running"}))

		setChatMessaging(t, h, false)
		synctest.Wait()
		if got := br.messagingAsks()[before:]; !slices.Equal(got, []bool{false}) {
			t.Fatalf("the idle flip asked the session %v, want one refused [false]", got)
		}
		if rec, ok := h.chatStore.Get(t.Context(), "c1"); !ok || !rec.MessagingOff {
			t.Fatalf("after kiro-cli refused, the record = %+v, want messaging_off kept", rec)
		}

		turnID, _ := h.stagePromptTurn(t, "c1")
		endTurn(t, h, "c1", turnID)
		synctest.Wait()
		if got := br.messagingAsks()[before:]; !slices.Equal(got, []bool{false, false}) {
			t.Errorf("after the next turn's close the session was asked %v, want the refused off sent again", got)
		}
	})
}

// messagingFrame is a config_option_update carrying sessionToolsMessaging at value; a sessionID plus workflow marker
// makes it a step's.
func messagingFrame(t *testing.T, value, sessionID string, workflow bool) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []any{
			map[string]any{"id": marotte.ConfigOptionSessionMessaging, "type": "select", "currentValue": value},
		},
	}
	if workflow {
		update["_meta"] = map[string]any{"kiro": map[string]any{
			"workflow": map[string]any{"workflowId": "wf1", "nodeId": "n1", "type": "step"},
		}}
	}
	params := mustJSON(t, map[string]any{"sessionId": sessionID, "update": mustJSON(t, update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

func (b *fakeBridge) messagingObserved() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observedMessaging
}

// The chat's own report is what EnsureMessaging compares against; a step's session reports its own switch.
func TestConfigOptionUpdate_HandsTheChatsMessagingReportToItsSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sessionID string
		workflow  bool
		want      string
	}{
		{name: "the chat's own frame", want: marotte.SessionMessagingOff},
		{name: "a step's frame", sessionID: "sess-step", workflow: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := healEffortFixture(t, "")

			h.handleSessionUpdate(t.Context(), "c1", messagingFrame(t, marotte.SessionMessagingOff, tc.sessionID, tc.workflow))
			settleHeals(t, h)

			if got := br.messagingObserved(); got != tc.want {
				t.Errorf("the session's observed messaging = %q, want %q", got, tc.want)
			}
		})
	}
}
