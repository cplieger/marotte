package agent

// The composer's Send bound for the running turn as a steer, through the chat's run list and steer record, the engine
// faked at the bridge.

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A Send placed while the steer ahead of it waits for kiro-cli's answer is a steer too: it answers steered once
// kiro-cli holds it, never queued, and reaches kiro-cli after the one ahead.
func TestCmdPrompt_ASendBehindASteerInFlightIsSteeredAfterIt(t *testing.T) {
	s, _, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	unlock, err := s.h.steerQueue.LockSteerOps(t.Context(), "c1")
	if err != nil {
		t.Fatalf("Setup: steer lock: %v", err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	t.Cleanup(release)
	first := sendAsUser(s.h, "c1", "m-1", "first")
	if !reachesState(s.h, "c1", "m-1", marotte.RunOffered) {
		t.Fatal("Setup: the first send never stood on the list as an offered steer")
	}
	second := sendAsUser(s.h, "c1", "m-2", "second")
	if !reachesState(s.h, "c1", "m-2", marotte.RunPending) {
		t.Fatal("Setup: the second send never reached the list")
	}

	release()

	for name, got := range map[string]string{"first": ackAs(t, awaitAnswer(t, first)), "second": ackAs(t, awaitAnswer(t, second))} {
		if got != "steered" {
			t.Errorf("the %s send answered %q, want steered", name, got)
		}
	}
	joinInflight(t, s.h)
	if sent, want := k.steered(), []string{"first", "second"}; !slices.Equal(sent, want) {
		t.Errorf("steers sent = %q, want %q", sent, want)
	}
}

// A steer the agent reads before kiro-cli's answer to it folds is answered steered: the read is its outcome.
func TestCmdPrompt_ASteerReadBeforeKiroCLIAnswersIsSteered(t *testing.T) {
	s, _, br := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	answer := make(chan struct{})
	t.Cleanup(func() { close(answer) })
	br.setBlockOn(marotte.MethodSessionSteer, answer)
	sent := sendAsUser(s.h, "c1", "m-1", "first")
	waitForCall(t, br, marotte.MethodSessionSteer)

	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg("steer-m-1", "first"))
	got := ackAs(t, awaitAnswer(t, sent))

	if got != "steered" {
		t.Errorf("the send answered %q, want steered", got)
	}
}

// A steered Send that a stop leaves unread waits paused, and when the user sends it now it takes the next turn as its
// own prompt, whose open leaves the composer alone: the steer's POST owed no composer settle, and the composer may
// hold the user's next words by then.
func TestCmdPrompt_ASteeredSendTakenLaterAsATurnLeavesTheComposer(t *testing.T) {
	s, cs, _ := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	if got := ackAs(t, awaitAnswer(t, sendAsUser(s.h, "c1", "m-1", "first"))); got != "steered" {
		t.Fatalf("Setup: the send answered %q, want steered", got)
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Draft = "my next thought"
		return true
	}); err != nil {
		t.Fatalf("Setup: draft: %v", err)
	}
	held := s.kasHolds()

	if rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{}`)}); rec.Code != 200 {
		t.Fatalf("Setup: cancel = %d %s", rec.Code, rec.Body.String())
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg(held...))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)
	if !reachesState(s.h, "c1", "m-1", marotte.RunPaused) {
		t.Fatalf("Setup: the unread steer is %q, want paused by the stop", stateOf(s.h, "c1", "m-1"))
	}
	now := marotte.ClientCommand{Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-1"}`)}
	if rec := postCmd(t, s.h, now); rec.Code != 200 {
		t.Fatalf("send now = %d %s", rec.Code, rec.Body.String())
	}

	var next *marotte.EntryPrompt
	waitFor(t, func() bool {
		opens := opensOf(t, logOf(t, cs, "c1"))
		if len(opens) < 2 {
			return false
		}
		next = opens[1].Prompt
		return true
	})
	if next == nil || next.Text != "first" {
		t.Fatalf("next turn = %+v, want the unread steer's words", next)
	}
	joinInflight(t, s.h)
	if c, _ := cs.Get(t.Context(), "c1"); c.Draft != "my next thought" {
		t.Errorf("draft = %q, want the user's next words kept", c.Draft)
	}
}
