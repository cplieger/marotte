package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ruleengine"
)

// tapCall is one turn tap the runtime made, with the chat's LastTurnEndedAt as the store held it at the call.
type tapCall struct {
	caller  *marotte.Caller
	tap     string
	turn    string
	endedAt int64
}

// tapRecorder is a rule engine that records its turn taps.
type tapRecorder struct {
	cs    *testChatStore
	calls []tapCall
	mu    sync.Mutex
}

func (r *tapRecorder) record(tap string, chatID marotte.ChatID, turnID string, caller *marotte.Caller) {
	var ended int64
	if c, ok := r.cs.Get(context.Background(), chatID); ok {
		ended = c.LastTurnEndedAt
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, tapCall{tap: tap, turn: turnID, caller: caller, endedAt: ended})
}

func (r *tapRecorder) TurnOpened(
	chatID marotte.ChatID, turnID string, _ marotte.TurnOpenSource, caller *marotte.Caller, _ bool,
) (leaseDepth int) {
	r.record("opened", chatID, turnID, caller)
	return 0
}

func (r *tapRecorder) TurnClosed(chatID marotte.ChatID, turnID string, caller *marotte.Caller) {
	r.record("closed", chatID, turnID, caller)
}

func (*tapRecorder) LaunchDepth(marotte.ChatID) int { return 0 }

func (*tapRecorder) RunEnded(marotte.ChatID, string, int) {}

func (*tapRecorder) SessionEnded(marotte.ChatID) {}

func (*tapRecorder) Reindex(marotte.ChatID) {}

func (*tapRecorder) RunNow(context.Context, marotte.ChatID, string, string) (command.RunNowAnswer, error) {
	return command.RunNowAnswer{}, nil
}

func (*tapRecorder) Status(marotte.ChatID) ruleengine.Status { return ruleengine.Status{} }

func (r *tapRecorder) snapshot() []tapCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tapCall(nil), r.calls...)
}

// tapsHub is a runtime over a ready bridge for c1 with taps wired as its rule engine.
func tapsHub(t *testing.T) (*Runtime, *tapRecorder) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	taps := &tapRecorder{cs: cs}
	h.SetRuleEngine(taps)
	return h, taps
}

// A rule's prompt opens its turn with the open tap carrying the turn_open id and the rule's caller, and the close tap
// follows once the turn's end is in the header, with the same id and caller.
func TestRuleTaps_ATurnReportsItsOpenAndItsCloseByTheTurnID(t *testing.T) {
	h, taps := tapsHub(t)
	from := &marotte.Caller{Kind: marotte.CallerKindRule, Label: "Tidy", Rule: "r1", Event: "every:t1:1", Depth: 1}

	id, _ := h.stagePromptTurnFrom(t, "c1", from, nil)
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnEndMsg("end_turn"))
	h.coord.releaseTurn("c1", id)
	waitFor(t, func() bool { return len(taps.snapshot()) == 2 })

	got := taps.snapshot()
	if got[0].tap != "opened" || got[0].turn != id || got[0].caller != from {
		t.Errorf("first tap = %+v, want opened for turn %s with the rule's caller", got[0], id)
	}
	if got[0].endedAt != 0 {
		t.Errorf("LastTurnEndedAt at the open = %d, want 0: no turn has ended", got[0].endedAt)
	}
	if got[1].tap != "closed" || got[1].turn != id || got[1].caller != from {
		t.Errorf("second tap = %+v, want closed for turn %s with the rule's caller", got[1], id)
	}
	if got[1].endedAt == 0 {
		t.Error("LastTurnEndedAt at the close = 0, want the turn's end written before the tap")
	}
}

// A turn no caller's prompt opened (the user's) taps with a nil caller.
func TestRuleTaps_AUsersTurnTapsWithNoCaller(t *testing.T) {
	h, taps := tapsHub(t)

	id, _ := h.stagePromptTurn(t, "c1")

	got := taps.snapshot()
	if len(got) != 1 || got[0].tap != "opened" || got[0].turn != id || got[0].caller != nil {
		t.Errorf("taps = %+v, want one open for turn %s with no caller", got, id)
	}
}

// With no engine wired a turn opens and closes as before.
func TestRuleTaps_NoEngineTapsNothing(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))

	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnEndMsg("end_turn"))
	h.coord.releaseTurn("c1", id)
	waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, "c1"))) == 1 })
}
