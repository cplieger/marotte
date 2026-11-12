package agent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// openTurn gives c1 an open turn of its own, the turn a close parks its finish behind; the returned func closes it.
func openTurn(t *testing.T, h *Runtime) (closeIt func()) {
	t.Helper()
	h.coord.turns.ensureTestLife("c1")
	lc, _ := h.coord.turns.lookup("c1")
	lc.mu.Lock()
	lc.own = &activeTurn{Source: marotte.TurnSourcePrompt}
	lc.mu.Unlock()
	return func() {
		lc.mu.Lock()
		lc.own = nil
		lc.mu.Unlock()
	}
}

func closeClean(t *testing.T, h *Runtime, focus string) {
	t.Helper()
	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonEndTurn), focus)
}

func closeStopped(t *testing.T, h *Runtime) {
	t.Helper()
	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonCancelled), "")
}

// settleAsk withdraws c1's ask filed as KAS's request acpID, through the settle path a real answer takes.
func settleAsk(t *testing.T, h *Runtime, acpID int64) {
	t.Helper()
	if !h.bus.PendingPermsWithdraw("c1", "tool-"+strconv.FormatInt(acpID, 10)) {
		t.Fatalf("Setup: no ask filed as request %d", acpID)
	}
}

func TestPushTurnOutcome_ParksTheFinishWhileATurnIsOpen(t *testing.T) {
	h, fp := newWithholdHub(t)
	closeB := openTurn(t, h)

	closeClean(t, h, "focus-A")
	awaitNoPush(t, h, fp, "the chat still has an open turn")

	closeB()
	closeClean(t, h, "focus-B")
	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-B") {
		t.Errorf("push body = %q, want the newer close's", got.body)
	}
	awaitNoPush(t, h, fp, "the parked finish was superseded, not raised beside the newer one")
}

func TestPushTurnOutcome_ReleasesItAtTheLastAskSettle(t *testing.T) {
	h, fp := newWithholdHub(t)
	addPermission(t, h, nil, 1, "")
	addPermission(t, h, nil, 2, "")

	closeClean(t, h, "focus-A")
	awaitNoPush(t, h, fp, "the chat has pending asks")
	settleAsk(t, h, 1)
	awaitNoPush(t, h, fp, "one ask is still pending")
	settleAsk(t, h, 2)

	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-A") {
		t.Errorf("push body = %q, want the parked finish", got.body)
	}
}

func TestPushTurnOutcome_DropsItWithTheChat(t *testing.T) {
	h, fp := newWithholdHub(t)
	addPermission(t, h, nil, 1, "")

	closeClean(t, h, "focus-A")
	if _, err := h.chatStore.(*testChatStore).Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Setup: delete c1: %v", err)
	}
	settleAsk(t, h, 1)

	awaitNoPush(t, h, fp, "the chat the finish was about is gone")
}

func TestPushTurnOutcome_ReleasesItWhenTheBlockingTurnClosesUnderALiveRun(t *testing.T) {
	h, fp := newWithholdHub(t)
	closeB := openTurn(t, h)

	closeClean(t, h, "focus-A")
	awaitNoPush(t, h, fp, "the chat still has an open turn")

	seedRunLease(t, h, "wf_1", "c1")
	closeB()
	closeClean(t, h, "focus-B")
	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-A") {
		t.Errorf("push body = %q, want A's parked finish: B closed under a live run and stores nothing", got.body)
	}
	awaitNoPush(t, h, fp, "the finish is raised once")
}

func TestPushTurnOutcome_DropsItAtTheChatsLastTabClose(t *testing.T) {
	for name, push := range map[string]bool{"with a push service": true, "with no push service": false} {
		t.Run(name, func(t *testing.T) {
			h, fp := newWithholdHub(t)
			closeB := openTurn(t, h)
			closeClean(t, h, "focus-A")

			role := noticeRole{coord: h.coord}
			if push {
				role.push = h.push
			}
			role.TabClosed(marotte.TabKindChat, "c1")
			closeB()
			closeStopped(t, h)

			awaitNoPush(t, h, fp, "the chat's last tab closed, which drops its parked finish")
		})
	}
}

func TestPushTurnOutcome_ParksABrokenCloseBehindAnAsk(t *testing.T) {
	h, fp := newWithholdHub(t)
	addPermission(t, h, nil, 1, "")

	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonError), "")
	awaitNoPush(t, h, fp, "the chat has a pending ask")
	settleAsk(t, h, 1)

	if got := awaitRunPush(t, fp); got.kind != marotte.PushKindAgentFinished {
		t.Errorf("push kind = %q, want the failed turn's agent_finished", got.kind)
	}
}

func TestPushTurnOutcome_ALiveRunDoesNotHoldAParkedFinish(t *testing.T) {
	h, fp := newWithholdHub(t)
	addPermission(t, h, nil, 1, "")
	closeClean(t, h, "focus-A")
	awaitNoPush(t, h, fp, "the chat has a pending ask")

	seedRunLease(t, h, "wf_1", "c1")
	settleAsk(t, h, 1)

	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-A") {
		t.Errorf("push body = %q, want the parked finish", got.body)
	}
}

func TestPushTurnOutcome_ARunAskDoesNotHoldThePark(t *testing.T) {
	h, fp := newWithholdHub(t)
	addPermission(t, h, nil, 1, "wf_1")

	closeClean(t, h, "focus-A")

	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-A") {
		t.Errorf("push body = %q, want the finish at close", got.body)
	}
}

func TestPushTurnOutcome_AnAskOnAnotherChatDoesNotPark(t *testing.T) {
	h, fp := newWithholdHub(t)
	h.bus.pendingPerms.add(1, permNeeded("c2", 0), nil)

	closeClean(t, h, "focus-A")

	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-A") {
		t.Errorf("push body = %q, want the finish at close", got.body)
	}
}

func TestPushTurnOutcome_RaisesAtOnceWithNothingOutstanding(t *testing.T) {
	h, fp := newWithholdHub(t)

	closeClean(t, h, "focus-A")

	if got := awaitRunPush(t, fp); !strings.Contains(got.body, "focus-A") {
		t.Errorf("push body = %q, want the finish at close", got.body)
	}
}
