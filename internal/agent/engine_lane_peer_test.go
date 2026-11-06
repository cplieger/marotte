package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const peerTarget = "sess_owned"

// peerNotifyTo is KAS's notify for another chat's send_message to target.
func peerNotifyTo(target string) *marotte.RPCResponse {
	return runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": target, "callerSessionId": "sess_sender", "message": "the schema moved",
		"severity": "info", "sender": "peer", "notifyId": "notify-p1", "deliveryCount": 1,
	})
}

// peerWakeFrames is what the engine lane receives for a peer message to target's closed chat: the notify, then the
// turn KAS woke, with an ask in it.
func peerWakeFrames(t *testing.T, h *Runtime, target string, askID int64) {
	t.Helper()
	src := fakeSourceOf(t, h)
	h.engineFrame(src.current(), peerNotifyTo(target))
	h.engineFrame(src.current(), withSessionID(t, newTurnStartMsg(), target))
	h.engineFrame(src.current(), withSessionID(t, newChunkMsg("Picking it up."), target))
	params, _ := json.Marshal(map[string]any{"sessionId": target, "toolCall": map[string]any{"toolCallId": "t-peer"}})
	h.engineFrame(src.current(), &marotte.RPCResponse{Method: marotte.MethodRequestPermission, ID: &askID, Params: params})
}

// A peer message to a chat with no resident session makes that chat resident on its own session and hands it the
// notify and the woken turn's frames, so the turn is the chat's and its ask waits for the reader.
func TestEngineLane_APeerMessageMakesItsClosedChatResident(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession(peerTarget)
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}

	peerWakeFrames(t, h, peerTarget, 91)
	// The ask waits for the reader on a tracked goroutine, so the hand-off is awaited by its effect.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.bus.pendingPerms.idsForChat("c1")) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if opts := br.lastSessionOpts(); opts == nil || opts.SessionID != peerTarget || opts.Role != marotte.SessionRoleChat {
		t.Fatalf("the door was %+v, want the chat's own session loaded through its chat", opts)
	}
	if sb := h.bridge.mgr.get("c1"); sb == nil || !sb.session().Open() {
		t.Fatal("the messaged chat was not made resident")
	}
	var rows int
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind == marotte.EntryKindSteer && strings.Contains(string(e.Payload), `"origin":"peer"`) {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("the chat's log holds %d peer message rows, want the notify's one", rows)
	}
	if _, ok := h.coord.turns.lookup("c1"); !ok {
		t.Error("the woken turn's frames minted no lifecycle for the chat")
	}
	if _, answered := fakeSourceOf(t, h).lane.responseTo(91); answered {
		t.Error("the woken turn's ask was answered on the engine lane, want it held for the chat")
	}
	if ids := h.bus.pendingPerms.idsForChat("c1"); len(ids) != 1 {
		t.Errorf("pending asks for the chat = %v, want the woken turn's one", ids)
	}
}

// A peer message to a session no chat records is dropped: its turn's ask is answered cancelled on the engine lane,
// and no chat opens.
func TestEngineLane_APeerMessageToAnUnknownSessionAnswersItsAsks(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })

	peerWakeFrames(t, h, "sess_nobody", 92)
	joinInflight(t, h)

	if r, ok := fakeSourceOf(t, h).lane.responseTo(92); !ok || !isCancelledOutcome(r.result) {
		t.Errorf("the ask = %+v (answered %v), want answered cancelled on the engine lane", r, ok)
	}
	if opts := br.lastSessionOpts(); opts != nil {
		t.Errorf("a session door opened (%+v) for a session no chat records", opts)
	}
	if !h.peerHolds.empty() {
		t.Error("the dropped message left its hold open")
	}
}
