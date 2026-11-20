package agent

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// wake is a run-attention wake through the notify door.
func (w *tableWorld) wake(id string) {
	w.doorFrom(id, "[marotte] wake "+id, &marotte.Caller{Kind: marotte.CallerKindMarotte, Label: "marotte"})
}

// closes takes the attention ends the list recorded since the last call.
func (w *tableWorld) closes() []attentionClose {
	return w.l.takeEffects().closed
}

// steeredHead is a running user turn m-a whose prompt is written, with wake d offered to it and queued by kiro-cli.
func steeredHead(t *testing.T) *tableWorld {
	t.Helper()
	w := newTableWorld()
	w.head()
	w.wake("d")
	w.advance()
	if e := w.entry(t, "d"); e.state != marotte.RunOffered {
		t.Fatalf("Setup: d = %s, want offered", e.state)
	}
	w.queued("d")
	w.closes()
	return w
}

func wantCloses(t *testing.T, got []attentionClose, want ...attentionClose) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("attention ends = %+v, want %+v", got, want)
	}
}

// Every way an attention entry leaves the list, or its carrying turn closes, ends it exactly once, and none goes back
// on the list once it failed.
func TestRunList_EveryAttentionEntryEndsOnce(t *testing.T) {
	t.Parallel()
	t.Run("taken as its own turn", func(t *testing.T) {
		w := newTableWorld()
		w.wake("d")
		w.running("d")
		w.closes()
		w.closed("d", listClose{outcome: marotte.TurnOutcomeFailed})
		wantCloses(t, w.closes(), attentionClose{entryID: "d", outcome: marotte.TurnOutcomeFailed})
	})
	t.Run("read as a steer, ended with the turn that carried it", func(t *testing.T) {
		w := steeredHead(t)
		w.l.steerFates([]entryFate{{id: "d", offer: w.entry(t, "d").offer, fate: fateRead}})
		wantCloses(t, w.closes())
		w.closed("m-a", listClose{outcome: marotte.TurnOutcomeInterrupted, clean: false})
		wantCloses(t, w.closes(), attentionClose{entryID: "d", outcome: marotte.TurnOutcomeInterrupted})
	})
	t.Run("read after the turn that carried it closed", func(t *testing.T) {
		w := steeredHead(t)
		offer := w.entry(t, "d").offer
		w.closed("m-a", listClose{outcome: marotte.TurnOutcomeCompleted, clean: true})
		w.closes()
		w.l.steerFates([]entryFate{{id: "d", offer: offer, fate: fateRead}})
		wantCloses(t, w.closes(), attentionClose{entryID: "d", outcome: marotte.TurnOutcomeCompleted})
	})
	for _, tc := range []struct {
		name string
		fate steerFate
		want marotte.TurnOutcome
	}{
		{"handed back unread by a turn end", fateUnread, attnLost},
		{"cleared from kiro-cli's buffer", fateCleared, attnLost},
		{"removed by the reader from the dock", fateGone, attnDiscarded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := steeredHead(t)
			w.l.steerFates([]entryFate{{id: "d", offer: w.entry(t, "d").offer, fate: tc.fate}})
			wantCloses(t, w.closes(), attentionClose{entryID: "d", outcome: tc.want})
			if _, e := w.l.find("d"); e != nil {
				t.Errorf("d stayed on the list as %s", e.state)
			}
			w.closed("m-a", listClose{outcome: marotte.TurnOutcomeCompleted, clean: true})
			if fx := w.l.takeEffects(); len(fx.closed) != 0 || slices.ContainsFunc(fx.launch, func(e *runEntry) bool { return e.id == "d" }) {
				t.Errorf("after the turn's close: ends %+v, launches %v; want neither", fx.closed, fx.launch)
			}
		})
	}
	t.Run("a launch refused before its turn opened", func(t *testing.T) {
		w := newTableWorld()
		w.wake("d")
		w.closes()
		w.failLaunch(t, "d")
		wantCloses(t, w.closes(), attentionClose{entryID: "d", outcome: attnLost})
		w.user("m-b", marotte.RunWantPrompt)
		w.running("m-b")
		w.closed("m-b", listClose{outcome: marotte.TurnOutcomeCompleted, clean: true})
		if _, e := w.l.find("d"); e != nil {
			t.Errorf("a clean close of another turn left d on the list as %s", e.state)
		}
	})
	t.Run("deleted by the reader while queued", func(t *testing.T) {
		w := newTableWorld()
		w.head()
		w.user("m-b", marotte.RunWantPrompt)
		w.wake("d")
		w.closes()
		if err := w.l.unqueue("d"); err != nil {
			t.Fatalf("unqueue: %v", err)
		}
		wantCloses(t, w.closes(), attentionClose{entryID: "d", outcome: attnDiscarded})
	})
	t.Run("the chat's life ends", func(t *testing.T) {
		w := steeredHead(t)
		w.l.steerFates([]entryFate{{id: "d", offer: w.entry(t, "d").offer, fate: fateRead}})
		w.user("m-b", marotte.RunWantPrompt)
		w.wake("e")
		next, _, lost := w.l.endLife()
		if !slices.Equal(lost, []string{"e", "d"}) {
			t.Errorf("the life's end lost %v, want the waiting wake e and the carried d", lost)
		}
		if slices.ContainsFunc(next, func(e marotte.RunEntry) bool { return e.ID == "d" || e.ID == "e" }) {
			t.Errorf("the next life's list %+v keeps a wake", next)
		}
	})
}

// A wake a crash left in the stored list is dropped when a life loads it; a grelot row beside it is kept.
func TestRunList_LoadDropsAStoredAttentionEntry(t *testing.T) {
	t.Parallel()
	stored := []marotte.RunEntry{
		{
			ID: "w-1", Kind: marotte.RunKindRow, State: marotte.RunPending, Origin: marotte.RunFromNotify, Want: marotte.RunWantEither,
			Row: &marotte.QueuedPrompt{ID: "w-1", Text: "wake", Caller: &marotte.Caller{Kind: marotte.CallerKindMarotte, Label: "marotte"}},
		},
		{
			ID: "g-1", Kind: marotte.RunKindRow, State: marotte.RunPending, Origin: marotte.RunFromNotify, Want: marotte.RunWantEither,
			Row: &marotte.QueuedPrompt{ID: "g-1", Text: "1 new", Caller: &marotte.Caller{Label: "marotte"}},
		},
	}
	l := &runList{}
	l.load(stored, stored, 1)
	if got := (&tableWorld{l: l}).order(); got != "g-1:pending" {
		t.Errorf("loaded list = %q, want the outside caller's row alone", got)
	}
}

// The model gate is consulted where an attention entry's turn starts: a held wake stays pending at its place, a
// person's row behind it is taken instead, and the effects name when the gate opens.
func TestRunList_TheModelGateHoldsAWakesTake(t *testing.T) {
	t.Parallel()
	open := time.Unix(1_800_000_030, 0)
	var asked []string
	admit := false
	w := newTableWorld()
	w.gate = func(e *runEntry) (bool, time.Time) {
		asked = append(asked, e.id)
		return admit, open
	}
	w.wake("d")
	if got := w.order(); got != "d:pending" {
		t.Fatalf("a held wake = %q, want pending", got)
	}
	if fx := w.l.takeEffects(); !fx.gateAt.Equal(open) {
		t.Errorf("gateAt = %v, want %v", fx.gateAt, open)
	}
	w.user("m-b", marotte.RunWantPrompt)
	if got := w.order(); got != "m-b:starting d:pending" {
		t.Errorf("with the wake held, the list = %q, want the person's row taken", got)
	}
	if e := w.entry(t, "d"); e.gateHeld {
		t.Error("the hold outlived the advance")
	}
	w.running("m-b")
	admit = true
	w.closed("m-b", listClose{outcome: marotte.TurnOutcomeCompleted, clean: true})
	if got := w.order(); got != "d:starting" {
		t.Errorf("once the gate admits, the list = %q, want the wake taken", got)
	}
	if !slices.Contains(asked, "d") || slices.Contains(asked, "m-b") {
		t.Errorf("the gate was asked for %v, want the wake alone", asked)
	}

	// Another chat's start can land between the gate's check and this take's start.
	w = newTableWorld()
	w.gate = func(*runEntry) (bool, time.Time) { return true, time.Time{} }
	w.gateStart = func(*runEntry) (bool, time.Time) { return false, open }
	w.wake("d")
	w.l.takeEffects()
	w.user("m-b", marotte.RunWantPrompt)
	if got := w.order(); got != "m-b:starting d:pending" {
		t.Errorf("with the wake's start refused after its check, the list = %q, want the person's row taken", got)
	}
	if fx := w.l.takeEffects(); !fx.gateAt.Equal(open) {
		t.Errorf("after a refused start gateAt = %v, want %v", fx.gateAt, open)
	}
	if e := w.entry(t, "d"); e.gateHeld {
		t.Error("the hold from a refused start outlived the advance")
	}
}
