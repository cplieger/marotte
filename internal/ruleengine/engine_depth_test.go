package ruleengine

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const depthChat marotte.ChatID = "bot1"

// depthEngine is an engine whose loop never runs: the depth taps write their map directly.
func depthEngine() *Engine { return New(&Deps{WakeHold: time.Minute}) }

// openTurn opens turnID and answers its depth, read through LaunchDepth: the test opens one turn at a time.
func openTurn(e *Engine, turnID string, source marotte.TurnOpenSource, caller *marotte.Caller, wake bool) (d, lease int) {
	lease = e.TurnOpened(depthChat, turnID, source, caller, wake)
	return e.LaunchDepth(depthChat), lease
}

func promptCaller(depth int) *marotte.Caller {
	return &marotte.Caller{Kind: marotte.CallerKindRule, Label: "Tidy", Rule: "r1", Event: "every:t1:1", Depth: depth}
}

func TestDepth_AUserTurnIsDepthZero(t *testing.T) {
	e := depthEngine()
	if d, lease := openTurn(e, "t1", marotte.TurnSourcePrompt, nil, false); d != 0 || lease != 0 {
		t.Errorf("turn depth, lease = %d, %d; want 0, 0", d, lease)
	}
}

func TestDepth_APromptTurnTakesItsCallersDepth(t *testing.T) {
	e := depthEngine()
	if d, _ := openTurn(e, "t1", marotte.TurnSourcePrompt, promptCaller(2), false); d != 2 {
		t.Errorf("turn depth = %d, want 2", d)
	}
}

// A run granted after its launching turn closed takes that turn's depth, until the session's life ends.
func TestDepth_LaunchDepthReadsTheLastClosedTurnInTheSameSession(t *testing.T) {
	e := depthEngine()
	e.TurnOpened(depthChat, "t1", marotte.TurnSourcePrompt, promptCaller(1), false)
	e.TurnClosed(depthChat, "t1", promptCaller(1))
	if got := e.LaunchDepth(depthChat); got != 1 {
		t.Errorf("LaunchDepth after the close = %d, want 1", got)
	}
	e.SessionEnded(depthChat)
	if got := e.LaunchDepth(depthChat); got != 0 {
		t.Errorf("LaunchDepth after the session ended = %d, want 0", got)
	}
}

func TestDepth_AFreshEngineAnswersZero(t *testing.T) {
	if got := depthEngine().LaunchDepth(depthChat); got != 0 {
		t.Errorf("LaunchDepth = %d, want 0", got)
	}
}

func TestDepth_AWakeTurnAfterAnAutomaticRunIsOneDeeper(t *testing.T) {
	e := depthEngine()
	e.RunEnded(depthChat, "wf1", 1)
	d, lease := openTurn(e, "t2", marotte.TurnSourceWireTurnStart, nil, true)
	if d != 2 || lease != 1 {
		t.Errorf("turn depth, lease = %d, %d; want 2, 1", d, lease)
	}
}

// A run a person launched starts no chain.
func TestDepth_AWakeTurnAfterADepthZeroRunStaysZero(t *testing.T) {
	e := depthEngine()
	e.RunEnded(depthChat, "wf1", 0)
	if d, lease := openTurn(e, "t2", marotte.TurnSourceWireTurnStart, nil, true); d != 0 || lease != 0 {
		t.Errorf("turn depth, lease = %d, %d; want 0, 0", d, lease)
	}
}

func TestDepth_TheWakingLeaseIsTheHigherPending(t *testing.T) {
	e := depthEngine()
	e.RunEnded(depthChat, "wf1", 1)
	e.RunEnded(depthChat, "wf2", 2)
	if d, lease := openTurn(e, "t2", marotte.TurnSourceWireTurnStart, nil, true); d != 3 || lease != 2 {
		t.Errorf("first wake = %d, %d; want 3, 2", d, lease)
	}
	e.TurnClosed(depthChat, "t2", nil)
	if d, lease := openTurn(e, "t3", marotte.TurnSourceWireTurnStart, nil, true); d != 2 || lease != 1 {
		t.Errorf("second wake = %d, %d; want 2, 1", d, lease)
	}
}

func TestDepth_APendingWakeDepthLastsTheWakeHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := depthEngine()
		e.RunEnded(depthChat, "wf1", 1)
		e.RunEnded(depthChat, "wf2", 2)
		time.Sleep(time.Minute - time.Millisecond)
		if lease := e.TurnOpened(depthChat, "t2", marotte.TurnSourceWireTurnStart, nil, true); lease != 2 {
			t.Errorf("lease depth inside the hold = %d, want 2", lease)
		}
		e.TurnClosed(depthChat, "t2", nil)
		time.Sleep(time.Millisecond)
		if d, lease := openTurn(e, "t3", marotte.TurnSourceWireTurnStart, nil, true); d != 0 || lease != 0 {
			t.Errorf("wake after the hold = %d, %d; want 0, 0", d, lease)
		}
	})
}

func TestDepth_ARunWithNoParentChatKeepsNoWakeDepth(t *testing.T) {
	e := depthEngine()
	e.RunEnded("", "wf1", 2)
	if lease := e.TurnOpened(depthChat, "t2", marotte.TurnSourceWireTurnStart, nil, true); lease != 0 {
		t.Errorf("lease depth = %d, want 0", lease)
	}
}

// A wire turn that took no wake hold continues the chat's last prompt: an automatic prompt's depth, a person's 0.
func TestDepth_AnotherPromptlessTurnTakesTheLastPromptsDepth(t *testing.T) {
	e := depthEngine()
	e.RunEnded(depthChat, "wf1", 3)
	e.TurnOpened(depthChat, "t1", marotte.TurnSourcePrompt, promptCaller(2), false)
	e.TurnClosed(depthChat, "t1", promptCaller(2))
	if d, lease := openTurn(e, "t2", marotte.TurnSourceWireTurnStart, nil, false); d != 2 || lease != 0 {
		t.Errorf("after an automatic prompt = %d, %d; want 2, 0", d, lease)
	}
	e.TurnClosed(depthChat, "t2", nil)
	e.TurnOpened(depthChat, "t3", marotte.TurnSourcePrompt, nil, false)
	e.TurnClosed(depthChat, "t3", nil)
	if d, _ := openTurn(e, "t4", marotte.TurnSourceWireTurnStart, nil, false); d != 0 {
		t.Errorf("after a person's prompt = %d, want 0", d)
	}
}

// A person's prompt between a rule's prompt and a prompt-less turn ends the chain: a person acted.
func TestDepth_APersonsPromptAfterARulesEndsItsChain(t *testing.T) {
	e := depthEngine()
	e.TurnOpened(depthChat, "t1", marotte.TurnSourcePrompt, promptCaller(2), false)
	e.TurnClosed(depthChat, "t1", promptCaller(2))
	e.TurnOpened(depthChat, "t2", marotte.TurnSourcePrompt, nil, false)
	e.TurnClosed(depthChat, "t2", nil)
	if d, _ := openTurn(e, "t3", marotte.TurnSourceWireTurnStart, nil, false); d != 0 {
		t.Errorf("turn depth = %d, want 0", d)
	}
}

func TestDepth_APromptlessTurnInANewSessionIsZero(t *testing.T) {
	e := depthEngine()
	e.TurnOpened(depthChat, "t1", marotte.TurnSourcePrompt, promptCaller(2), false)
	e.TurnClosed(depthChat, "t1", promptCaller(2))
	e.SessionEnded(depthChat)
	if d, _ := openTurn(e, "t2", marotte.TurnSourceWireTurnStart, nil, false); d != 0 {
		t.Errorf("turn depth = %d, want 0", d)
	}
}

// The session's end drops its pending wakes with the holds marotte kept for them.
func TestDepth_ASessionEndDropsItsPendingWakes(t *testing.T) {
	e := depthEngine()
	e.RunEnded(depthChat, "wf1", 2)
	e.SessionEnded(depthChat)
	if lease := e.TurnOpened(depthChat, "t2", marotte.TurnSourceWireTurnStart, nil, true); lease != 0 {
		t.Errorf("lease depth = %d, want 0", lease)
	}
}

// The taps never go through the loop's inbox, so a full one loses no depth.
func TestDepth_AFullInboxLosesNoDepth(t *testing.T) {
	e := depthEngine()
	e.signal()
	for i, id := range []string{"t1", "t2", "t3"} {
		e.TurnOpened(depthChat, id, marotte.TurnSourcePrompt, promptCaller(i+1), false)
		if got := e.LaunchDepth(depthChat); got != i+1 {
			t.Errorf("LaunchDepth after %s = %d, want %d", id, got, i+1)
		}
		e.TurnClosed(depthChat, id, promptCaller(i+1))
	}
}

// Depths are per chat.
func TestDepth_ChatsDoNotShareDepths(t *testing.T) {
	e := depthEngine()
	e.TurnOpened(depthChat, "t1", marotte.TurnSourcePrompt, promptCaller(2), false)
	e.RunEnded(depthChat, "wf1", 2)
	if got := e.LaunchDepth("bot2"); got != 0 {
		t.Errorf("LaunchDepth of another chat = %d, want 0", got)
	}
	if lease := e.TurnOpened("bot2", "t9", marotte.TurnSourceWireTurnStart, nil, true); lease != 0 {
		t.Errorf("another chat's wake lease = %d, want 0", lease)
	}
}
