package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// user places a user row whose delivery is want, as a send placed against the list as it stands would be.
func (w *tableWorld) user(id string, want marotte.RunWant) {
	w.l.submit(&runEntry{
		id: id, kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, want: want,
		row: &marotte.QueuedPrompt{ID: id, Text: id},
	}, w.facts(), 0, false)
}

// door is a notify-door send from grelot.
func (w *tableWorld) door(id, text string) doorPlace {
	e := &runEntry{
		id: id, kind: marotte.RunKindRow, origin: marotte.RunFromNotify,
		row: &marotte.QueuedPrompt{ID: id, Text: text, Caller: &marotte.Caller{Label: "grelot"}},
	}
	if w.mode == marotte.InterruptQueue {
		e.want = marotte.RunWantPrompt
	}
	_, at := w.l.doorSend(e, w.facts(), 0, false)
	return at
}

// prompted marks id's turn prompt written, then lets the list deliver.
func (w *tableWorld) prompted(id string) {
	_, e := w.l.find(id)
	w.l.turnPrompted(e.turnID)
	w.advance()
}

// head is a running turn m-a whose prompt is written.
func (w *tableWorld) head() {
	w.user("m-a", marotte.RunWantPrompt)
	w.running("m-a")
	w.prompted("m-a")
}

// queued is kiro-cli queueing the steer of id.
func (w *tableWorld) queued(id string) {
	w.l.steerQueued(id)
	w.advance()
}

// steered lists, in order, the entries the list handed out as steers.
func (w *tableWorld) steered() string {
	ids := make([]string, 0, len(w.l.fx.steer))
	for _, e := range w.l.fx.steer {
		ids = append(ids, e.id)
	}
	return strings.Join(ids, " ")
}

func (w *tableWorld) entry(t *testing.T, id string) *runEntry {
	t.Helper()
	_, e := w.l.find(id)
	if e == nil {
		t.Fatalf("no entry %s on %q", id, w.order())
	}
	return e
}

func (w *tableWorld) wantOrder(t *testing.T, step, want string) {
	t.Helper()
	if got := w.order(); got != want {
		t.Errorf("%s: order = %q, want %q", step, got, want)
	}
}

// The list's events, one script per row of DESIGN section 3's table: a door entry (grelot's wake) is steered only
// with nothing of the user's waiting and never into a turn the user's stop or send-now asked for, waits behind every
// user entry waiting ahead of it, is never stranded on an idle list, and one prompt never joins two senders.
func TestRunList_Events(t *testing.T) {
	cases := []struct {
		run       func(t *testing.T, w *tableWorld)
		name      string
		wantOrder string
		wantSteer string
	}{
		{
			name:      "an idle list takes a door entry as its own turn",
			run:       func(_ *testing.T, w *tableWorld) { w.door("d", "1 new") },
			wantOrder: "d:starting",
		},
		{
			name: "a prompted turn with nothing of the user's waiting takes a door entry as a steer",
			run: func(_ *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
			},
			wantOrder: "m-a:running d:offered", wantSteer: "d",
		},
		{
			name: "a door entry waits behind the user's queued row",
			run: func(_ *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.door("d", "1 new")
			},
			wantOrder: "m-a:running u:pending d:pending",
		},
		{
			name: "nothing is steered into a turn whose prompt is not written",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.door("d", "1 new")
				w.wantOrder(t, "before the prompt is written", "m-a:running d:pending")
				if got := w.steered(); got != "" {
					t.Errorf("steered %q before the prompt was written, want nothing", got)
				}
				w.prompted("m-a")
			},
			wantOrder: "m-a:running d:offered", wantSteer: "d",
		},
		{
			name: "a wire head counts as prompted",
			run: func(_ *testing.T, w *tableWorld) {
				w.wireOpen("t-w", true)
				w.door("d", "1 new")
			},
			wantOrder: "t-w:running d:offered", wantSteer: "d",
		},
		{
			name: "a door send under the Queue setting is steered into no turn",
			run: func(_ *testing.T, w *tableWorld) {
				w.mode = marotte.InterruptQueue
				w.head()
				w.door("d", "1 new")
			},
			wantOrder: "m-a:running d:pending",
		},
		{
			name: "a door entry waits behind the user's steer in kiro-cli and is steered once it is read",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantEither)
				w.queued("u")
				w.door("d", "1 new")
				w.wantOrder(t, "while the user's steer is unread", "m-a:running u:in_kas d:pending")
				w.l.steerRead("u")
				w.advance()
			},
			wantOrder: "m-a:running d:offered", wantSteer: "u d",
		},
		{
			name: "one steer is offered at a time, in list order",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u1", marotte.RunWantEither)
				w.user("u2", marotte.RunWantEither)
				w.wantOrder(t, "while u1's steer is out", "m-a:running u1:offered u2:pending")
				w.queued("u1")
			},
			wantOrder: "m-a:running u1:in_kas u2:offered", wantSteer: "u1 u2",
		},
		{
			name: "a user steer kiro-cli refused is its own turn, ahead of a later door entry",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantEither)
				w.l.steerBack("u")
				w.door("d", "1 new")
				w.advance()
				w.wantOrder(t, "after the refusal", "m-a:running u:pending d:pending")
				if got := w.entry(t, "u").want; got != marotte.RunWantPrompt {
					t.Errorf("refused steer's want = %q, want prompt", got)
				}
				w.closed("m-a", listClose{clean: true})
			},
			wantOrder: "u:starting d:pending", wantSteer: "u",
		},
		{
			name: "a door entry kiro-cli refused is not offered again in that turn, and is taken after it",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
				w.l.steerBack("d")
				w.advance()
				w.wantOrder(t, "after the refusal", "m-a:running d:pending")
				w.closed("m-a", listClose{clean: true})
			},
			wantOrder: "d:starting", wantSteer: "d",
		},
		{
			name: "a user steer arriving while a door entry's turn starts is steered once its prompt is written",
			run: func(t *testing.T, w *tableWorld) {
				w.door("d", "1 new")
				w.running("d")
				w.user("u", marotte.RunWantEither)
				w.wantOrder(t, "before d's prompt is written", "d:running u:pending")
				w.prompted("d")
			},
			wantOrder: "d:running u:offered", wantSteer: "u",
		},
		{
			name: "a turn end returns the user's steers ahead of a door steer and joins only theirs into one prompt",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
				w.queued("d")
				w.user("u1", marotte.RunWantEither)
				w.queued("u1")
				w.user("u2", marotte.RunWantEither)
				w.queued("u2")
				w.l.turnEnded([]string{"d", "u1", "u2"})
				w.advance()
				w.wantOrder(t, "after the turn end", "m-a:running u1:pending u2:pending d:pending")
				for _, id := range []string{"u1", "u2", "d"} {
					if !w.entry(t, id).unread {
						t.Errorf("%s not marked unread", id)
					}
				}
				w.closed("m-a", listClose{clean: true})
				u1 := w.entry(t, "u1")
				if len(u1.joined) != 1 || u1.joined[0].id != "u2" {
					t.Errorf("u1's prompt carries %v, want u2 alone", joinedIDs(u1.joined))
				}
			},
			wantOrder: "u1:starting d:pending", wantSteer: "d u1 u2",
		},
		{
			name: "after a plain stop the user's steer returned unread waits paused ahead of an earlier door entry until Resume",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.door("d", "1 new")
				w.user("u", marotte.RunWantEither)
				w.prompted("m-a")
				w.queued("u")
				w.l.readerStop("m-a", 1)
				w.l.turnEnded([]string{"u"})
				w.wantOrder(t, "after the stop's turn end", "m-a:stopping u:paused d:paused")
				w.closed("m-a", listClose{})
				w.wantOrder(t, "after the stopped close", "u:paused d:paused")
				w.l.unpauseRows()
				w.advance()
				if !w.entry(t, "u").userNext {
					t.Error("u's turn is not marked as one the user's stop asked for")
				}
				w.running("u")
				w.prompted("u")
			},
			wantOrder: "u:running d:pending", wantSteer: "u",
		},
		{
			name: "a stop pauses the user's rows and a door entry, which stays paused once the user's row is discarded",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u1", marotte.RunWantPrompt)
				w.door("d", "1 new")
				w.l.readerStop("m-a", 1)
				w.wantOrder(t, "at the stop", "m-a:stopping u1:paused d:paused")
				w.closed("m-a", listClose{})
				w.wantOrder(t, "after the stopped close", "u1:paused d:paused")
				if err := w.l.unqueue("u1"); err != nil {
					t.Fatalf("unqueue u1: %v", err)
				}
				w.advance()
			},
			wantOrder: "d:paused",
		},
		{
			name: "a door entry arriving after a stop joins the queue paused, behind the user's paused row",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u1", marotte.RunWantPrompt)
				w.l.readerStop("m-a", 1)
				w.door("d", "1 new")
				w.wantOrder(t, "after the door send", "m-a:stopping u1:paused d:paused")
				w.closed("m-a", listClose{})
			},
			wantOrder: "u1:paused d:paused",
		},
		{
			name: "a door entry refused in one turn is offered into kiro-cli's next turn",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
				w.l.steerBack("d")
				w.kasRunning = true
				w.closed("m-a", listClose{clean: true})
				w.wireOpen("t-w", true)
				w.advance()
			},
			wantOrder: "t-w:running d:offered", wantSteer: "d d",
		},
		{
			name: "a door entry pending at a stop is paused, and the stopped turn's close takes nothing",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.door("d", "1 new")
				w.l.readerStop("m-a", 1)
				w.wantOrder(t, "at the stop", "m-a:stopping d:paused")
				w.closed("m-a", listClose{})
			},
			wantOrder: "d:paused",
		},
		{
			name: "a second send from the same caller replaces the waiting entry's text in place",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.door("d", "1 new; run grelot inbox")
				w.user("u", marotte.RunWantPrompt)
				if got := w.door("d2", "2 new; run grelot inbox"); got != doorReplaced {
					t.Errorf("second send = %v, want replaced", got)
				}
				if got := w.entry(t, "d").row.Text; got != "2 new; run grelot inbox" {
					t.Errorf("waiting entry's text = %q, want the second send's", got)
				}
			},
			wantOrder: "m-a:running d:pending u:pending",
		},
		{
			name: "a send from a caller whose entry is with kiro-cli adds nothing",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
				if got := w.door("d2", "2 new"); got != doorWithKAS {
					t.Errorf("send while d is offered = %v, want with kiro-cli", got)
				}
				w.queued("d")
				if got := w.door("d3", "3 new"); got != doorWithKAS {
					t.Errorf("send while d is in kiro-cli's buffer = %v, want with kiro-cli", got)
				}
			},
			wantOrder: "m-a:running d:in_kas", wantSteer: "d",
		},
		{
			name: "a door entry offered or in kiro-cli's buffer is not discarded as a row",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
				if err := w.l.unqueue("d"); !errors.Is(err, errRowSending) {
					t.Errorf("unqueue while offered = %v, want already sending", err)
				}
				w.queued("d")
				if err := w.l.unqueue("d"); !errors.Is(err, errRowInKAS) {
					t.Errorf("unqueue while in kiro-cli's buffer = %v, want left to a clear of that buffer", err)
				}
			},
			wantOrder: "m-a:running d:in_kas", wantSteer: "d",
		},
		{
			name: "send-now refuses a door entry and a steer in flight",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantEither)
				if _, err := w.l.sendNow("u", "", false); !errors.Is(err, errRowSending) {
					t.Errorf("send-now on an offered steer = %v, want already sending", err)
				}
				w.user("u2", marotte.RunWantPrompt)
				w.door("d", "1 new")
				if _, err := w.l.sendNow("d", "", false); !errors.Is(err, errRowNotQueued) {
					t.Errorf("send-now on a door entry = %v, want not queued", err)
				}
				if w.entry(t, "d").next {
					t.Error("the door entry was marked send-now")
				}
			},
			wantOrder: "m-a:running u:offered u2:pending d:pending", wantSteer: "u",
		},
		{
			name: "rows a stop paused are turns of their own once the stop is undone",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.user("u1", marotte.RunWantEither)
				w.user("u2", marotte.RunWantEither)
				w.l.stopHead("m-a", 7)
				w.wantOrder(t, "at the stop", "m-a:stopping u1:paused u2:paused")
				w.l.stopUndone(7, func(string) bool { return true })
				w.prompted("m-a")
				w.wantOrder(t, "after the undo", "m-a:running u1:pending u2:pending")
				w.closed("m-a", listClose{clean: true})
				w.running("u1")
				w.prompted("u1")
			},
			wantOrder: "u1:running u2:pending",
		},
		{
			name: "a user row queued behind a door entry's turn is not steered into it",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				_, e := w.l.find("m-a")
				w.nextSeq++
				w.l.turnOpened(openFacts{turnID: "t-m-a", admission: e.res.Token, seq: w.nextSeq})
				w.door("d", "1 new")
				w.user("u", marotte.RunWantPrompt)
				w.closed("m-a", listClose{clean: true})
				w.wantOrder(t, "after the head closed", "d:starting u:pending")
				w.running("d")
				w.prompted("d")
				w.wantOrder(t, "during d's turn", "d:running u:pending")
				w.closed("d", listClose{clean: true})
			},
			wantOrder: "u:starting",
		},
		{
			name: "send-now takes the next turn ahead of an earlier door entry, and no door entry is steered into it",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.door("d", "1 new")
				w.user("u", marotte.RunWantPrompt)
				h, err := w.l.sendNow("u", "", false)
				if err != nil || h == nil || h.id != "m-a" {
					t.Fatalf("send-now = (%v, %v), want m-a to stop", h, err)
				}
				w.l.readerStop("m-a", 1)
				w.closed("m-a", listClose{})
				if !w.entry(t, "u").userNext {
					t.Error("u's turn is not marked as one the user's send-now asked for")
				}
				w.running("u")
				w.prompted("u")
				w.wantOrder(t, "during the send-now turn", "u:running d:paused")
				w.closed("u", listClose{clean: true})
			},
			wantOrder: "d:starting",
		},
		{
			name: "send-now on the user's steer in kiro-cli leads the next turn, ahead of an earlier door entry",
			run: func(t *testing.T, w *tableWorld) {
				w.user("m-a", marotte.RunWantPrompt)
				w.running("m-a")
				w.door("d", "1 new")
				w.user("u1", marotte.RunWantEither)
				w.user("u2", marotte.RunWantEither)
				w.prompted("m-a")
				w.queued("u1")
				w.queued("u2")
				w.wantOrder(t, "before the press", "m-a:running d:pending u1:in_kas u2:in_kas")
				if w.l.leadSteer("d") {
					t.Error("a door entry was marked as the send-now lead")
				}
				if !w.l.leadSteer("u2") {
					t.Fatal("the user's steer in kiro-cli was not marked as the send-now lead")
				}
				w.l.readerStop("m-a", 1)
				w.l.turnEnded([]string{"u1", "u2"})
				w.closed("m-a", listClose{})
				e := w.entry(t, "u2")
				if !e.userNext {
					t.Error("u2's turn is not marked as one the user's send-now asked for")
				}
				if got := joinedIDs(e.joined); len(got) != 1 || got[0] != "u1" {
					t.Errorf("the lead's prompt joins %v, want u1 behind it", got)
				}
			},
			wantOrder: "u2:starting d:paused", wantSteer: "u1 u2",
		},
		{
			name: "only the user's steer handed to kiro-cli is marked as the send-now lead",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("d", "1 new")
				w.queued("d")
				w.user("u", marotte.RunWantPrompt)
				if w.l.leadSteer("d") {
					t.Error("a door entry in kiro-cli was marked as the send-now lead")
				}
				if w.l.leadSteer("u") {
					t.Error("a queued row was marked as a steer's send-now lead")
				}
			},
			wantOrder: "m-a:running d:in_kas u:pending", wantSteer: "d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTableWorld()
			tc.run(t, w)
			w.wantOrder(t, "at the end", tc.wantOrder)
			if got := w.steered(); got != tc.wantSteer {
				t.Errorf("steered %q, want %q", got, tc.wantSteer)
			}
		})
	}
}

func joinedIDs(es []*runEntry) []string {
	ids := make([]string, 0, len(es))
	for _, e := range es {
		ids = append(ids, e.id)
	}
	return ids
}

// The queue cap counts every row but the notify door's (one entry per caller) and the user's steers in flight, which
// count under the steer cap; an API caller's rows still count.
func TestQueueRefusal_LeavesDoorEntriesAndSteersOutOfTheCap(t *testing.T) {
	head := marotte.RunEntry{
		ID: "m-a", Kind: marotte.RunKindRow, State: marotte.RunRunning, Origin: marotte.RunFromPrompt,
		Steerable: true, Row: &marotte.QueuedPrompt{ID: "m-a", Text: "a"},
	}
	fill := func(n int, state marotte.RunEntryState, origin marotte.RunEntryOrigin, caller func(int) *marotte.Caller) []marotte.RunEntry {
		out := []marotte.RunEntry{head}
		for i := range n {
			id := "m-" + strings.Repeat("x", i+1)
			out = append(out, marotte.RunEntry{
				ID: id, Kind: marotte.RunKindRow, State: state, Origin: origin,
				Row: &marotte.QueuedPrompt{ID: id, Text: id, Caller: caller(i)},
			})
		}
		return out
	}
	user := func(int) *marotte.Caller { return nil }
	callers := func(i int) *marotte.Caller { return &marotte.Caller{Label: "svc" + strings.Repeat("y", i)} }
	row := &marotte.QueuedPrompt{ID: "m-new", Text: "new"}
	full := marotte.MaxQueuedPrompts - 2
	t.Run("the user's waiting rows fill it", func(t *testing.T) {
		if queueRefusal(fill(full, marotte.RunPending, marotte.RunFromPrompt, user), row) == nil {
			t.Errorf("%d waiting user rows: queued, want refused full", full)
		}
	})
	t.Run("an API caller's rows fill it", func(t *testing.T) {
		if queueRefusal(fill(full, marotte.RunPending, marotte.RunFromPrompt, callers), row) == nil {
			t.Errorf("%d waiting API caller rows: queued, want refused full", full)
		}
	})
	t.Run("door entries do not", func(t *testing.T) {
		if err := queueRefusal(fill(full, marotte.RunPending, marotte.RunFromNotify, callers), row); err != nil {
			t.Errorf("%d door entries: refused %v, want queued", full, err)
		}
	})
	t.Run("the user's steers in kiro-cli's buffer do not", func(t *testing.T) {
		if err := queueRefusal(fill(full, marotte.RunInKAS, marotte.RunFromPrompt, user), row); err != nil {
			t.Errorf("%d user steers in kiro-cli's buffer: refused %v, want queued", full, err)
		}
	})
}

// A fate the steer record reports for an offer moves the row only while that offer is the row's latest: a row
// kiro-cli refused and that was offered again keeps its new offer when the old one's late fate lands.
func TestRunList_SteerFatesApplyOnlyToTheirOffer(t *testing.T) {
	w := newTableWorld()
	w.head()
	w.door("d", "1 new")
	first := w.entry(t, "d").offer
	if w.entry(t, "d").resent {
		t.Error("the first offer is marked resent")
	}
	w.l.steerFates([]entryFate{{id: "d", offer: first, fate: fateBack}})
	w.advance()
	w.wantOrder(t, "after kiro-cli refused it", "m-a:running d:pending")
	w.prompted("m-a")
	w.wantOrder(t, "offered again once a prompt is written", "m-a:running d:offered")
	if e := w.entry(t, "d"); e.offer == first || !e.resent {
		t.Fatalf("second offer = token %d (first %d), resent %v, want a new token marked resent", e.offer, first, e.resent)
	}

	w.l.steerFates([]entryFate{{id: "d", offer: first, fate: fateInKAS}, {id: "d", offer: first, fate: fateRead}})

	w.wantOrder(t, "after the first offer's late fates", "m-a:running d:offered")
	w.l.steerFates([]entryFate{{id: "d", offer: w.entry(t, "d").offer, fate: fateInKAS}})
	w.wantOrder(t, "after the second offer's own fate", "m-a:running d:in_kas")
}

// A door entry a clear of the user's took out of kiro-cli's buffer waits behind the user's steer kiro-cli still
// holds, and is steered into the same turn once the agent read the user's.
func TestRunList_AClearedDoorEntryWaitsBehindTheUsersSteer(t *testing.T) {
	w := newTableWorld()
	w.head()
	w.door("d", "1 new")
	w.queued("d")
	w.user("u", marotte.RunWantEither)
	w.queued("u")
	w.wantOrder(t, "Setup", "m-a:running d:in_kas u:in_kas")

	w.l.steerFates([]entryFate{{id: "d", offer: w.entry(t, "d").offer, fate: fateCleared}})
	w.advance()

	w.wantOrder(t, "after the clear", "m-a:running u:in_kas d:pending")
	if e := w.entry(t, "d"); e.unread || e.want != marotte.RunWantEither {
		t.Errorf("d = unread %v want %q, want a steer kiro-cli never read, still steerable", e.unread, e.want)
	}
	w.l.steerFates([]entryFate{{id: "u", offer: w.entry(t, "u").offer, fate: fateRead}})
	w.advance()
	w.wantOrder(t, "after the user's steer was read", "m-a:running d:offered")
}

// unreadUser stands id on w's list as a user steer a turn end returned unread.
func (w *tableWorld) unreadUser(id string) {
	w.l.entries = append(w.l.entries, &runEntry{
		id: id, kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, state: marotte.RunPending,
		want: marotte.RunWantPrompt, unread: true, row: &marotte.QueuedPrompt{ID: id, Text: id},
	})
}

// The user's unread steers ride one prompt; a stop before that prompt opens gives every one of them back, in order,
// and a life ending before it opens keeps every one for the next life.
func TestRunList_AJoinedPromptNotOpenedKeepsEveryRow(t *testing.T) {
	t.Run("withdrawn by a stop", func(t *testing.T) {
		w := newTableWorld()
		w.unreadUser("u1")
		w.unreadUser("u2")
		w.advance()
		w.wantOrder(t, "taken", "u1:starting")
		w.l.stopHead("u1", 7)
		w.wantOrder(t, "withdrawn", "u1:paused u2:paused")
	})
	t.Run("the life ends", func(t *testing.T) {
		w := newTableWorld()
		w.unreadUser("u1")
		w.unreadUser("u2")
		w.advance()
		var ids []string
		next, _ := w.l.endLife()
		for _, e := range next {
			ids = append(ids, e.ID)
		}
		if got := strings.Join(ids, " "); got != "u1 u2" {
			t.Errorf("the next life's list = %q, want u1 u2", got)
		}
	})
}

// failLaunch gives back id's take and refuses its launch before the turn opened, as a kiro-cli session/new error
// does, then lets the list deliver.
func (w *tableWorld) failLaunch(t *testing.T, id string) {
	t.Helper()
	token := w.entry(t, id).res.Token
	w.held = false
	w.l.reservationReleased(token)
	w.l.launched(id, token, "", launchFailed, errors.New("Authentication required"))
	w.advance()
}

// A launch that keeps failing is never retried in a loop, and no sender's message is lost to it: the row is paused,
// whoever sent it, and a notify caller's next send replaces the paused row's text.
func TestRunList_AnEntryWhoseLaunchFailsIsPausedNotTakenAgain(t *testing.T) {
	t.Run("a door entry", func(t *testing.T) {
		w := newTableWorld()
		w.door("d", "1 new")
		w.wantOrder(t, "taken", "d:starting")
		w.l.takeEffects()
		w.failLaunch(t, "d")
		w.wantOrder(t, "after the failed launch", "d:paused")
		if fx := w.l.takeEffects(); len(fx.launch) != 0 {
			t.Errorf("after the failed launch the list launched %d entries, want none", len(fx.launch))
		}
		if at := w.door("d2", "2 new"); at != doorReplaced {
			t.Errorf("the caller's next send placed %d, want doorReplaced", at)
		}
		w.wantOrder(t, "the caller's next send", "d:paused")
		if got := w.entry(t, "d").row.Text; got != "2 new" {
			t.Errorf("paused row's text = %q, want the next send's", got)
		}
	})
	t.Run("a door entry's failed launch is no Stop: the next turn's clean close lets it go", func(t *testing.T) {
		w := newTableWorld()
		w.door("d", "1 new")
		w.l.takeEffects()
		w.failLaunch(t, "d")
		if w.l.stopPaused(w.entry(t, "d")) {
			t.Error("the failed launch's row waits for Resume, want it to wait for the next turn")
		}
		w.user("u", marotte.RunWantPrompt)
		w.wantOrder(t, "the user's send", "u:starting d:paused")
		w.running("u")
		w.prompted("u")
		w.l.takeEffects()
		w.closed("u", listClose{clean: true})
		w.wantOrder(t, "after the user's clean turn", "d:starting")
	})
	t.Run("a user row", func(t *testing.T) {
		w := newTableWorld()
		w.user("u", marotte.RunWantPrompt)
		w.wantOrder(t, "taken", "u:starting")
		w.l.takeEffects()
		w.failLaunch(t, "u")
		w.wantOrder(t, "after the failed launch", "u:paused")
		if fx := w.l.takeEffects(); len(fx.launch) != 0 {
			t.Errorf("after the failed launch the list launched %d entries, want none", len(fx.launch))
		}
	})
	t.Run("an API caller's row", func(t *testing.T) {
		w := newTableWorld()
		w.l.submit(&runEntry{
			id: "a", kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, want: marotte.RunWantPrompt,
			row: &marotte.QueuedPrompt{ID: "a", Text: "nightly report", Caller: &marotte.Caller{Label: "bot"}},
		}, w.facts(), 0, false)
		w.wantOrder(t, "taken", "a:starting")
		w.l.takeEffects()
		w.failLaunch(t, "a")
		w.wantOrder(t, "after the failed launch", "a:paused")
		if fx := w.l.takeEffects(); len(fx.launch) != 0 {
			t.Errorf("after the failed launch the list launched %d entries, want none", len(fx.launch))
		}
	})
}

// withdrawStop stops id before its turn opened, as a press of Stop does: the take is withdrawn, its reservation
// given back and its launch answered withdrawn, then the list delivers.
func (w *tableWorld) withdrawStop(t *testing.T, id string, token uint64) {
	t.Helper()
	take := w.entry(t, id).res.Token
	if plan := w.l.stopHead(id, token); plan.arm != stopWithdraw {
		t.Fatalf("stop on %s: arm %d, want stopWithdraw", id, plan.arm)
	}
	w.held = false
	w.l.reservationReleased(take)
	w.l.launched(id, take, "", launchWithdrawn, command.ErrTakeWithdrawn)
	w.advance()
}
