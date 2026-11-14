package agent

// The user's discard, Delete and "Queue this message instead" beside a notify door's wake that kiro-cli holds, on the
// chat's real run list and steer queue, kiro-cli 3.2.0's steering faked at the bridge.

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func unqueueCmd(id string) marotte.ClientCommand {
	payload, _ := json.Marshal(marotte.UnqueuePromptCommand{MessageID: id})
	return marotte.ClientCommand{Type: marotte.CmdUnqueuePrompt, ChatID: "c1", Payload: payload}
}

func queueInsteadCmd(key string) marotte.ClientCommand {
	payload, _ := json.Marshal(marotte.SteerRemoveCommand{SteerID: key, Queue: true})
	return marotte.ClientCommand{Type: marotte.CmdSteerRemove, ChatID: "c1", Payload: payload}
}

// wakeInKAS runs a prompted turn in c1 and steers a grelot wake n-1 into it, then the user's sends in order, each
// into kiro-cli's buffer.
func wakeInKAS(t *testing.T, users ...[2]string) (*steerHarness, *testChatStore, *kas320Steering, string) {
	t.Helper()
	s, cs, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	placeWake(t, s.h, "c1", "n-1")
	if !reachesState(s.h, "c1", "n-1", marotte.RunInKAS) {
		t.Fatal("Setup: the wake was never steered into the running turn")
	}
	for _, u := range users {
		if got := ackAs(t, awaitAnswer(t, sendAsUser(s.h, "c1", u[0], u[1]))); got != "steered" {
			t.Fatalf("Setup: %s answered %q, want steered", u[0], got)
		}
	}
	return s, cs, k, id
}

// A wake still being handed to kiro-cli cannot be discarded: the discard is refused as already sending, and the wake
// reaches kiro-cli once, not twice and not never.
func TestNotify_ADiscardOfAWakeBeingSentIsRefusedAndItIsSentOnce(t *testing.T) {
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
	placeWake(t, s.h, "c1", "n-1")
	if !reachesState(s.h, "c1", "n-1", marotte.RunOffered) {
		t.Fatal("Setup: the wake is not being handed to kiro-cli")
	}

	rec := postCmd(t, s.h, unqueueCmd("n-1"))

	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body.Reason != "sending" {
		t.Errorf("unqueue = %d %s, want 409 sending", rec.Code, rec.Body.String())
	}
	release()
	if !reachesState(s.h, "c1", "n-1", marotte.RunInKAS) {
		t.Errorf("wake = %q, want in kiro-cli's buffer after the refused discard", stateOf(s.h, "c1", "n-1"))
	}
	joinInflight(t, s.h)
	if got := k.steered(); !slices.Equal(got, []string{grelotWake}) {
		t.Errorf("steers sent = %q, want the wake once", got)
	}
}

// Discarding a wake kiro-cli already holds clears it from kiro-cli's buffer: it leaves the list and no turn takes it.
func TestNotify_ADiscardOfAWakeTheAgentHoldsClearsIt(t *testing.T) {
	s, cs, k, id := wakeInKAS(t)

	rec := postCmd(t, s.h, unqueueCmd("n-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("unqueue = %d %s, want 200", rec.Code, rec.Body.String())
	}
	joinInflight(t, s.h)
	if got := k.clears(); len(got) != 1 || !slices.Equal(got[0], []string{"steer-n-1"}) {
		t.Errorf("steering_cleared frames = %v, want one naming the wake", got)
	}
	if got := k.holds(); len(got) != 0 {
		t.Errorf("KAS holds %v, want nothing", got)
	}
	if got := stateOf(s.h, "c1", "n-1"); got != "" {
		t.Errorf("wake stands %q on the list, want gone", got)
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	idleAfter(t, s.h, "c1", id)
	if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
		t.Errorf("turns opened after the discard = %d, want none", n-1)
	}
	if got := k.steered(); !slices.Equal(got, []string{grelotWake}) {
		t.Errorf("steers sent = %q, want only the wake's first send", got)
	}
}

// Deleting one of the user's steers beside a wake kiro-cli holds resends only the user's kept steers, as one: the
// wake's words never join a user's steer, and the wake waits behind the user's resent steer, then goes once the
// agent read it.
func TestNotify_DeletingAUserSteerBesideAWakeResendsOnlyTheUsersKeptSteers(t *testing.T) {
	s, _, k, _ := wakeInKAS(t, [2]string{"m-u1", "first"}, [2]string{"m-u2", "second"}, [2]string{"m-u3", "third"})

	rec := postCmd(t, s.h, removeCmd("steer-m-u1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("steer_remove = %d %s, want 200", rec.Code, rec.Body.String())
	}
	joinInflight(t, s.h)
	resent := "second\n\nthird"
	if got, want := k.steered(), []string{grelotWake, "first", "second", "third", resent}; !slices.Equal(got, want) {
		t.Errorf("steers sent = %q, want %q: the user's kept steers resent as one", got, want)
	}
	if got, want := listedRows(s.h, "c1"), []string{"m-u2:in_kas", "m-u3:in_kas", "grelot:pending"}; !slices.Equal(got, want) {
		t.Errorf("waiting rows = %v, want %v", got, want)
	}
	held := k.holds()
	if len(held) != 1 {
		t.Fatalf("KAS holds %v, want the user's resent steer alone", held)
	}

	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg(held[0], resent))

	waitFor(t, func() bool { return len(k.steered()) == 6 })
	if got := k.steered(); got[5] != grelotWake {
		t.Errorf("steer after the user's was read = %q, want the wake", got[5])
	}
}

// Deleting the wake beside the user's steer resends the user's steer alone.
func TestNotify_DeletingAWakeResendsTheUsersSteerAlone(t *testing.T) {
	s, _, k, _ := wakeInKAS(t, [2]string{"m-u1", "first"})

	rec := postCmd(t, s.h, removeCmd("steer-n-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("steer_remove = %d %s, want 200", rec.Code, rec.Body.String())
	}
	joinInflight(t, s.h)
	if got, want := k.steered(), []string{grelotWake, "first", "first"}; !slices.Equal(got, want) {
		t.Errorf("steers sent = %q, want %q", got, want)
	}
	if got, want := listedRows(s.h, "c1"), []string{"m-u1:in_kas"}; !slices.Equal(got, want) {
		t.Errorf("waiting rows = %v, want %v", got, want)
	}
}

// "Queue this message instead" on the user's steer beside a wake kiro-cli holds takes both out of kiro-cli's buffer
// and resends neither: the user's message waits as a queued row ahead of the wake, and opens the next turn.
func TestNotify_QueueInsteadPutsTheUsersSteerAheadOfTheWake(t *testing.T) {
	s, cs, k, id := wakeInKAS(t, [2]string{"m-u1", "first"})

	rec := postCmd(t, s.h, queueInsteadCmd("steer-m-u1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("queue instead = %d %s, want 200", rec.Code, rec.Body.String())
	}
	joinInflight(t, s.h)
	if got, want := k.steered(), []string{grelotWake, "first"}; !slices.Equal(got, want) {
		t.Errorf("steers sent = %q, want %q: nothing resent", got, want)
	}
	if got := k.holds(); len(got) != 0 {
		t.Errorf("KAS holds %v, want nothing", got)
	}
	if got, want := listedRows(s.h, "c1"), []string{"m-u1:pending", "grelot:pending"}; !slices.Equal(got, want) {
		t.Errorf("waiting rows = %v, want %v", got, want)
	}
	if f := s.lastFrame("steer-m-u1"); f.State != marotte.SteerRowRemoved {
		t.Errorf("the user's dock frame = %q, want removed: the message is a queued row now", f.State)
	}

	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)

	if next := nextPrompt(t, cs, "c1", 1); next.Text != "first" || next.Caller != nil {
		t.Errorf("next turn = %q (caller %+v), want the user's queued message", next.Text, next.Caller)
	}
	if !grelotWaits(s.h, "c1", "n-1") {
		t.Errorf("list = %+v, want the wake still waiting", listOf(s.h, "c1"))
	}
}

// "Queue this message instead" is the user's own: a wake's steer cannot be moved to the queue, and stays where it is.
func TestNotify_QueueInsteadOfAWakeIsRefused(t *testing.T) {
	s, _, k, _ := wakeInKAS(t)

	rec := postCmd(t, s.h, queueInsteadCmd("steer-n-1"))

	if rec.Code != http.StatusConflict {
		t.Errorf("queue instead = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got := k.clears(); len(got) != 0 {
		t.Errorf("steering_cleared frames = %v, want none", got)
	}
	if got := stateOf(s.h, "c1", "n-1"); got != marotte.RunInKAS {
		t.Errorf("wake = %q, want still in kiro-cli's buffer", got)
	}
}
