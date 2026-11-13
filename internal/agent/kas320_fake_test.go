package agent

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// kas320Notice is kiro-cli 3.2.0's K4e: a steer whose text matches is filed as kind notification, any other as user.
var kas320Notice = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]`)

type kas320Steer struct {
	id     string
	notice bool
}

// kas320Steering is the steering buffer of kiro-cli 3.2.0's ACP server for one chat's session, the version
// entrypoint.sh pins (dist/server/acp-server.js in its bundle). Each frame folds into the chat before the answer
// that follows it, as KAS emits them.
type kas320Steering struct {
	fold    func(*marotte.RPCResponse)
	buffer  []kas320Steer
	cleared [][]string
	// sent is every _session/steer's message, in call order, dropped or not.
	sent []string
	mu   sync.Mutex
	// epochMoves is a turn boundary landing while the next steer persists (handleSessionSteer's epoch check).
	epochMoves bool
}

// kas320On answers chatID's _session/steer, _session/steer/clear and session/cancel as kiro-cli 3.2.0 does. It takes
// over br's onCall and onNotify.
func kas320On(h *Runtime, chatID marotte.ChatID, br *fakeBridge) *kas320Steering {
	k := &kas320Steering{fold: func(m *marotte.RPCResponse) { h.translateACPEvent(chatID, h.originOf(chatID), m) }}
	br.mu.Lock()
	br.onCall = k.call
	br.onNotify = k.notify
	br.mu.Unlock()
	return k
}

// boundaryDuringNextSteer makes the next _session/steer meet a moved steeringEpoch.
func (k *kas320Steering) boundaryDuringNextSteer() {
	k.mu.Lock()
	k.epochMoves = true
	k.mu.Unlock()
}

// holds is every message id the buffer holds, in buffer order.
func (k *kas320Steering) holds() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	ids := make([]string, 0, len(k.buffer))
	for _, s := range k.buffer {
		ids = append(ids, s.id)
	}
	return ids
}

// steered is the message of every _session/steer the buffer was sent, in order.
func (k *kas320Steering) steered() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.sent)
}

// clears is the messageIds of every steering_cleared the buffer emitted, in order.
func (k *kas320Steering) clears() [][]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.cleared)
}

func (k *kas320Steering) call(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
	switch method {
	case marotte.MethodSessionSteer:
		return k.steer(p), nil, true
	case marotte.MethodSessionSteerClear:
		raw, _ := json.Marshal(map[string]any{"cleared": true, "messageIds": k.boundary()})
		return raw, nil, true
	}
	return nil, nil, false
}

// steer is handleSessionSteer: the id is the sent messageId under its kind's prefix (WD), and a steer whose epoch
// moved while it persisted is dropped with no steering_queued.
func (k *kas320Steering) steer(p map[string]any) json.RawMessage {
	wireID, _ := p["messageId"].(string)
	text, _ := p["message"].(string)
	notice := kas320Notice.MatchString(text)
	prefix := marotte.SteerIDPrefix
	if notice {
		prefix = "notify-"
	}
	id := wireID
	if !strings.HasPrefix(id, prefix) {
		id = prefix + id
	}
	k.mu.Lock()
	k.sent = append(k.sent, text)
	if k.epochMoves {
		k.epochMoves = false
		k.mu.Unlock()
		raw, _ := json.Marshal(map[string]any{"queued": false, "messageId": id, "dropped": "epoch_changed"})
		return raw
	}
	k.buffer = append(k.buffer, kas320Steer{id: id, notice: notice})
	k.mu.Unlock()
	frame := map[string]any{"kind": "steering_queued", "messageId": id, "content": text}
	if m := kas320Notice.FindStringSubmatch(text); m != nil {
		frame["notificationSeverity"] = m[1]
	}
	k.fold(newSessionInfoMsg(frame))
	raw, _ := json.Marshal(map[string]any{"queued": true, "messageId": id})
	return raw
}

// notify is cancel: cancelLocalTurn clears the buffer at a turn boundary and re-wakes only the notification-kind rows
// (rewakeClearedNotifications), so a user-kind steer starts no turn.
func (k *kas320Steering) notify(method string) {
	if method != marotte.MethodCancel {
		return
	}
	k.mu.Lock()
	for _, s := range k.buffer {
		if s.notice {
			k.mu.Unlock()
			panic("kas320Steering: the re-wake of a notification-kind steer at cancel is not modelled")
		}
	}
	k.mu.Unlock()
	k.boundary()
}

// boundary is clearSteeringAtTurnBoundary: an empty buffer emits nothing; else one steering_cleared names every id.
func (k *kas320Steering) boundary() []string {
	k.mu.Lock()
	ids := make([]string, 0, len(k.buffer))
	for _, s := range k.buffer {
		ids = append(ids, s.id)
	}
	k.buffer = nil
	if len(ids) > 0 {
		k.cleared = append(k.cleared, ids)
	}
	k.mu.Unlock()
	if len(ids) > 0 {
		k.fold(clearedMsg(ids...))
	}
	return ids
}

func postSteer(t *testing.T, h *Runtime, messageID, text string) int {
	t.Helper()
	payload, _ := json.Marshal(marotte.SteerCommand{Text: text, MessageID: messageID})
	return postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdSteer, ChatID: "c1", Payload: payload}).Code
}

// A Stop over two unread user steers on kiro-cli 3.2.0: the cancel clears both in one steering_cleared and KAS starts
// no turn from them; marotte keeps the user's words on its queue, paused by the Stop, in order.
func TestFakeEngine_CancelClearsUserSteersAndRewakesNone(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.recs.note = s.h.coord.recordSteer
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	for _, st := range [][2]string{{"a", "check the logs"}, {"b", "then the tests"}} {
		if code := postSteer(t, s.h, st[0], st[1]); code != http.StatusOK {
			t.Fatalf("Setup: steer %s = %d, want 200", st[0], code)
		}
	}
	if got := k.holds(); !slices.Equal(got, []string{"steer-a", "steer-b"}) {
		t.Fatalf("Setup: KAS holds %v, want steer-a and steer-b", got)
	}

	if rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)

	if got := k.clears(); len(got) != 1 || !slices.Equal(got[0], []string{"steer-a", "steer-b"}) {
		t.Errorf("steering_cleared frames = %v, want one naming steer-a and steer-b", got)
	}
	waitFor(t, func() bool {
		return stateOf(s.h, "c1", "a") == marotte.RunPaused && stateOf(s.h, "c1", "b") == marotte.RunPaused
	})
	joinInflight(t, s.h)
	if got := listedRows(s.h, "c1"); !slices.Equal(got, []string{"a:paused", "b:paused"}) {
		t.Errorf("waiting rows = %v, want both steers paused in send order", got)
	}
	if opens := opensOf(t, logOf(t, cs, "c1")); len(opens) != 1 {
		t.Errorf("turns opened = %d, want only the stopped turn, none re-woken by KAS", len(opens))
	}
}

// A steer kiro-cli 3.2.0 answers {"queued":false,"dropped":"epoch_changed"} is not in its buffer: marotte keeps the
// user's words and sends them as the next turn's prompt.
func TestFakeEngine_SteerEpochDrop(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.recs.note = s.h.coord.recordSteer
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	k.boundaryDuringNextSteer()

	if code := postSteer(t, s.h, "a", "check the logs"); code != http.StatusOK {
		t.Fatalf("steer = %d, want 200: the words are kept", code)
	}

	if got := k.holds(); len(got) != 0 {
		t.Fatalf("Setup: KAS holds %v, want nothing after the drop", got)
	}
	if got := s.kasHolds(); slices.Contains(got, "steer-a") {
		t.Errorf("marotte counts steer-a in KAS's buffer (%v) after KAS dropped it", got)
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	if p := resendPrompt(t, cs); p == nil || p.Text != "check the logs" || !slices.Equal(p.Resends, []string{"steer-a"}) {
		t.Errorf("next prompt = %+v, want the dropped steer's words resending steer-a", p)
	}
}
