package command

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	wakeText  = "Workflow \"Fix\" (wf_1) paused: loop review-loop reached its iteration limit after 5 iterations."
	wakeLabel = "Loop limit reached: Fix"
)

func steerCalls(b *steerBridge) int {
	n := 0
	for _, m := range b.methods() {
		if m == marotte.MethodSessionSteer {
			n++
		}
	}
	return n
}

func TestDeliverForUser_PromptsAnIdleChatUnderTheLabel(t *testing.T) {
	f := newMergeFixture(t)
	if _, err := f.host.Mutate(t.Context(), "c-parent", func(c *marotte.Chat, _ bool) bool {
		c.Draft = "half-typed reply"
		return true
	}); err != nil {
		t.Fatal(err)
	}

	if err := deliverForUser(t.Context(), f.roles, "c-parent", wakeText, wakeLabel); err != nil {
		t.Fatalf("deliverForUser = %v, want delivered", err)
	}
	f.host.waitInflight(t)

	if n := f.host.promptCount(); n != 1 {
		t.Fatalf("prompts opened = %d, want 1", n)
	}
	if p := f.host.opened[0]; p.Text != wakeText || p.Label != wakeLabel {
		t.Errorf("prompt = %+v, want the wake's text under its label", p)
	}
	if n := steerCalls(f.host.bridge.(*steerBridge)); n != 0 {
		t.Errorf("steers sent = %d, want none to an idle chat", n)
	}
	if c, _ := f.host.Get(t.Context(), "c-parent"); c == nil || c.Draft != "half-typed reply" {
		t.Errorf("parent draft = %+v, want the user's draft untouched", c)
	}
}

func TestDeliverForUser_SteersARunningTurn(t *testing.T) {
	b := newSteerBridge()
	h := newResendHost(b, AdmissionBusy, marotte.TurnSourcePrompt, true)
	roles := steerRolesOf(h, NewSteerLedger(), newStubSteerQueue())

	if err := deliverForUser(t.Context(), roles, "c1", wakeText, wakeLabel); err != nil {
		t.Fatalf("deliverForUser = %v, want delivered", err)
	}

	if n := steerCalls(b); n != 1 {
		t.Errorf("steers sent = %d, want exactly 1", n)
	}
	if len(h.opened) != 0 {
		t.Errorf("prompts opened = %d, want none into a running turn", len(h.opened))
	}
}

// A turn that ends between the admission read and the steer leaves an idle chat: prompt it.
func TestDeliverForUser_ATurnEndingBeforeTheSteerGetsThePrompt(t *testing.T) {
	f := newMergeFixture(t)
	f.host.admit = AdmissionBusy
	f.host.onReserve = func() { f.host.onReserve = func() { f.host.admit = AdmissionAcquired } }

	if err := deliverForUser(t.Context(), f.roles, "c-parent", wakeText, wakeLabel); err != nil {
		t.Fatalf("deliverForUser = %v, want delivered", err)
	}
	f.host.waitInflight(t)

	if n := f.host.promptCount(); n != 1 {
		t.Errorf("prompts opened = %d, want 1", n)
	}
}

var droppedSteer = map[string]any{"queued": false, "dropped": "epoch_changed"}

// KAS refuses a steer whose turn already ended inside it: the steer record carries the row into the next prompt, so
// the delivery opens no prompt of its own and sends no second steer.
func TestDeliverForUser_ASteerKASDroppedIsLeftToTheRecord(t *testing.T) {
	b := newSteerBridge()
	b.results[marotte.MethodSessionSteer] = droppedSteer
	h := newResendHost(b, AdmissionBusy, marotte.TurnSourcePrompt, true)
	q := newStubSteerQueue()
	roles := steerRolesOf(h, NewSteerLedger(), q)

	if err := deliverForUser(t.Context(), roles, "c1", wakeText, wakeLabel); err != nil {
		t.Fatalf("deliverForUser = %v, want delivered through the record", err)
	}

	if n := steerCalls(b); n != 1 {
		t.Errorf("steers sent = %d, want the 1 KAS dropped", n)
	}
	if len(h.opened) != 0 {
		t.Errorf("prompts opened = %d, want none: the record sends the dropped row", len(h.opened))
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.sent) != 1 || q.sent[0].queued {
		t.Errorf("steer record told = %+v, want the one send reported unqueued", q.sent)
	}
}

func TestDeliverForUser_APromptTheAgentNeverReceivedFails(t *testing.T) {
	f := newMergeFixture(t)
	f.host.unbound["c-parent"] = "The agent could not start."

	if err := deliverForUser(t.Context(), f.roles, "c-parent", wakeText, wakeLabel); err == nil {
		t.Error("deliverForUser = nil, want an error for a turn KAS never bound")
	}
	f.host.waitInflight(t)
}

func TestDeliverForUser_ABusyChatWithNoTurnToSteerFails(t *testing.T) {
	b := newSteerBridge()
	h := newResendHost(b, AdmissionStarting, marotte.TurnSourceLocalShell, true)
	roles := steerRolesOf(h, NewSteerLedger(), newStubSteerQueue())

	if err := deliverForUser(t.Context(), roles, "c1", wakeText, wakeLabel); err == nil {
		t.Error("deliverForUser = nil, want an error: a shell turn reads no steer")
	}
	if n := steerCalls(b); n != 0 || len(h.opened) != 0 {
		t.Errorf("steers = %d, prompts = %d, want nothing sent", n, len(h.opened))
	}
}
