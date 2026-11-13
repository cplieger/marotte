package agent

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// api places an API caller's row (a CmdPrompt with a caller label) whose delivery is want.
func (w *tableWorld) api(id string, want marotte.RunWant) {
	w.l.submit(&runEntry{
		id: id, kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, want: want,
		row: &marotte.QueuedPrompt{ID: id, Text: id, Caller: &marotte.Caller{Label: "bot"}},
	}, w.facts(), 0, false)
}

// sender is one of the three senders a row can have.
type sender struct {
	name  string
	place func(w *tableWorld, id string, want marotte.RunWant)
}

var senders = []sender{
	{"user", func(w *tableWorld, id string, want marotte.RunWant) { w.user(id, want) }},
	{"wake", func(w *tableWorld, id string, _ marotte.RunWant) { w.door(id, "1 new") }},
	{"api", func(w *tableWorld, id string, want marotte.RunWant) { w.api(id, want) }},
}

// stateSetup stands row x from s on a fresh list in one state and returns its list.
type stateSetup func(t *testing.T, s sender) *tableWorld

var stateSetups = map[string]stateSetup{
	// waiting: behind a running turn in queue mode, so nothing is steered.
	"waiting": func(t *testing.T, s sender) *tableWorld {
		w := newTableWorld()
		w.mode = marotte.InterruptQueue
		w.head()
		s.place(w, "x", marotte.RunWantPrompt)
		w.mustOrder(t, "m-a:running x:pending")
		return w
	},
	// opening: taken on an idle list, its turn not opened.
	"opening": func(t *testing.T, s sender) *tableWorld {
		w := newTableWorld()
		s.place(w, "x", marotte.RunWantPrompt)
		w.mustOrder(t, "x:starting")
		return w
	},
	// unsent: taken, its turn opened, the prompt not yet written.
	"unsent": func(t *testing.T, s sender) *tableWorld {
		w := newTableWorld()
		s.place(w, "x", marotte.RunWantPrompt)
		w.openedUnsent("x")
		w.mustOrder(t, "x:starting")
		return w
	},
	// open: its own turn running, the prompt written.
	"open": func(t *testing.T, s sender) *tableWorld {
		w := newTableWorld()
		s.place(w, "x", marotte.RunWantPrompt)
		w.running("x")
		w.prompted("x")
		w.mustOrder(t, "x:running")
		return w
	},
	// steered: in kiro-cli's buffer for the user's running turn m-a.
	"steered": func(t *testing.T, s sender) *tableWorld {
		w := newTableWorld()
		w.head()
		s.place(w, "x", marotte.RunWantEither)
		w.queued("x")
		w.mustOrder(t, "m-a:running x:in_kas")
		return w
	},
	// late: sent while the head the reader stopped is still stopping.
	"late": func(t *testing.T, s sender) *tableWorld {
		w := newTableWorld()
		w.head()
		w.l.readerStop("m-a", 7)
		s.place(w, "x", marotte.RunWantPrompt)
		if s.name == "user" {
			w.mustOrder(t, "m-a:stopping x:pending")
		} else {
			w.mustOrder(t, "m-a:stopping x:paused")
		}
		return w
	},
}

// openedUnsent opens a prompt turn for the starting entry id without writing its call.
func (w *tableWorld) openedUnsent(id string) {
	_, e := w.l.find(id)
	w.nextSeq++
	w.l.turnOpened(openFacts{turnID: "t-" + id, admission: e.res.Token, seq: w.nextSeq})
}

func (w *tableWorld) mustOrder(t *testing.T, want string) {
	t.Helper()
	if got := w.order(); got != want {
		t.Fatalf("setup: order = %q, want %q", got, want)
	}
}

// stop presses Stop on what the state's row waits behind or is: the running head, the take, or its own turn, and
// lets the stopped turn close (with x left unread when kiro-cli held it). A late row's Stop was pressed before it
// was sent: here the stopped head closes.
func (w *tableWorld) stop(t *testing.T, state string) {
	t.Helper()
	switch state {
	case "waiting":
		w.l.readerStop("m-a", 7)
		w.closed("m-a", listClose{})
	case "opening":
		w.withdrawStop(t, "x", 7)
	case "unsent", "open":
		w.l.readerStop("x", 7)
		w.closed("x", listClose{})
	case "steered":
		w.l.readerStop("m-a", 7)
		w.l.turnEnded([]string{"x"})
		w.closed("m-a", listClose{})
	case "late":
		w.closed("m-a", listClose{})
	}
}

// restart is the list a process start keeps (marotte.SanitizeRun on the stored list) and the steers it cuts.
func (w *tableWorld) restart() (next, cut string) {
	all := w.l.project()
	return entryWords(marotte.SanitizeRun(all, true)), entryWords(handedSteers(all))
}

// closeChat is the list the chat's next life starts from and the steers its close notes as not read.
func (w *tableWorld) closeChat() (next, cut string) {
	n, s := w.l.endLife()
	return entryWords(n), entryWords(s)
}

// reopen ends the chat's life and starts its next life in the same process from the list the close left.
func (w *tableWorld) reopen() {
	next, _ := w.l.endLife()
	w.l = &runList{}
	w.l.load(next, next, 0)
}

func entryWords(es []marotte.RunEntry) string {
	words := make([]string, 0, len(es))
	for _, e := range es {
		words = append(words, e.ID+":"+string(e.State))
	}
	return strings.Join(words, " ")
}

// copied reads words with the id of every entry no test placed shown as "copy": the row a stop puts back under a new
// message id.
func copied(words string, placed ...string) string {
	out := strings.Fields(words)
	for i, word := range out {
		id, state, _ := strings.Cut(word, ":")
		known := false
		for _, p := range placed {
			known = known || p == id
		}
		if !known {
			out[i] = "copy:" + state
		}
	}
	return strings.Join(out, " ")
}

// placedIDs are the ids the row-state tests place themselves.
var placedIDs = []string{"m-a", "x", "x2", "x3", "u", "v", "y", "t-w"}

// The row-state table: one case per cell of (sender, state, event), DESIGN section 3b. The events are Stop, a failed
// launch, a second wake from the same caller, a process restart and the chat's close. The rule under test is the
// user's: Stop pauses the chat's whole queue, every sender's rows alike, and nothing launches by itself after it; one
// caller has at most one waiting row.
func TestRunList_RowStates(t *testing.T) {
	type cell struct {
		sender, state, event string
		// want is the list after the event (a row put back under a new id reads "copy"); cut the steers a restart or
		// close hands to the drop record.
		want, cut string
		// launches is the takes the event's own advance made (-1: not read).
		launches int
	}
	cells := []cell{
		// Stop.
		{"user", "waiting", "stop", "x:paused", "", 0},
		{"wake", "waiting", "stop", "x:paused", "", 0},
		{"api", "waiting", "stop", "x:paused", "", 0},
		{"user", "opening", "stop", "x:paused", "", 0},
		{"wake", "opening", "stop", "x:paused", "", 0},
		{"api", "opening", "stop", "x:paused", "", 0},
		{"user", "unsent", "stop", "", "", 0},
		{"wake", "unsent", "stop", "copy:paused", "", 0},
		{"api", "unsent", "stop", "copy:paused", "", 0},
		{"user", "open", "stop", "", "", 0},
		{"wake", "open", "stop", "", "", 0},
		{"api", "open", "stop", "", "", 0},
		{"user", "steered", "stop", "x:paused", "", 0},
		{"wake", "steered", "stop", "x:paused", "", 0},
		{"api", "steered", "stop", "x:paused", "", 0},
		{"user", "late", "stop", "x:starting", "", 1},
		{"wake", "late", "stop", "x:paused", "", 0},
		{"api", "late", "stop", "x:paused", "", 0},
		// Failed launch (only a take whose turn has not opened has one).
		{"user", "opening", "fail", "x:paused", "", 0},
		{"wake", "opening", "fail", "x:paused", "", 0},
		{"api", "opening", "fail", "x:paused", "", 0},
		// A second wake from the same caller (only a wake has one).
		{"wake", "waiting", "wake", "m-a:running x:pending", "", 0},
		{"wake", "opening", "wake", "x:starting x2:pending", "", 0},
		{"wake", "unsent", "wake", "x:starting x2:pending", "", 0},
		{"wake", "open", "wake", "x:running x2:offered", "", 0},
		{"wake", "steered", "wake", "m-a:running x:in_kas", "", 0},
		{"wake", "late", "wake", "m-a:stopping x:paused", "", 0},
		// Restart.
		{"user", "waiting", "restart", "x:held", "", -1},
		{"wake", "waiting", "restart", "", "", -1},
		{"api", "waiting", "restart", "x:held", "", -1},
		{"user", "opening", "restart", "x:held", "", -1},
		{"wake", "opening", "restart", "", "", -1},
		{"api", "opening", "restart", "x:held", "", -1},
		{"user", "unsent", "restart", "", "", -1},
		{"wake", "unsent", "restart", "", "", -1},
		{"api", "unsent", "restart", "", "", -1},
		{"user", "open", "restart", "", "", -1},
		{"wake", "open", "restart", "", "", -1},
		{"api", "open", "restart", "", "", -1},
		{"user", "steered", "restart", "", "x:in_kas", -1},
		{"wake", "steered", "restart", "", "x:in_kas", -1},
		{"api", "steered", "restart", "", "x:in_kas", -1},
		{"user", "late", "restart", "x:held", "", -1},
		{"wake", "late", "restart", "", "", -1},
		{"api", "late", "restart", "x:held", "", -1},
		// The chat's close.
		{"user", "waiting", "close", "x:paused", "", -1},
		{"wake", "waiting", "close", "x:pending", "", -1},
		{"api", "waiting", "close", "x:paused", "", -1},
		{"user", "opening", "close", "x:paused", "", -1},
		{"wake", "opening", "close", "x:pending", "", -1},
		{"api", "opening", "close", "x:paused", "", -1},
		{"user", "unsent", "close", "", "", -1},
		{"wake", "unsent", "close", "", "", -1},
		{"api", "unsent", "close", "", "", -1},
		{"user", "open", "close", "", "", -1},
		{"wake", "open", "close", "", "", -1},
		{"api", "open", "close", "", "", -1},
		{"user", "steered", "close", "", "x:in_kas", -1},
		{"wake", "steered", "close", "", "x:in_kas", -1},
		{"api", "steered", "close", "", "x:in_kas", -1},
		{"user", "late", "close", "x:paused", "", -1},
		{"wake", "late", "close", "x:paused", "", -1},
		{"api", "late", "close", "x:paused", "", -1},
	}
	for _, c := range cells {
		t.Run(c.sender+" "+c.state+" "+c.event, func(t *testing.T) {
			var s sender
			for _, x := range senders {
				if x.name == c.sender {
					s = x
				}
			}
			w := stateSetups[c.state](t, s)
			w.l.takeEffects()
			var got, cut string
			switch c.event {
			case "stop":
				w.stop(t, c.state)
				got = w.order()
			case "fail":
				w.failLaunch(t, "x")
				got = w.order()
			case "wake":
				w.door("x2", "2 new")
				got = w.order()
			case "restart":
				got, cut = w.restart()
			case "close":
				got, cut = w.closeChat()
			}
			if got = copied(got, placedIDs...); got != c.want {
				t.Errorf("list = %q, want %q", got, c.want)
			}
			if cut != c.cut {
				t.Errorf("steers cut = %q, want %q", cut, c.cut)
			}
			if c.launches >= 0 {
				if n := len(w.l.takeEffects().launch); n != c.launches {
					t.Errorf("launches = %d, want %d", n, c.launches)
				}
			}
		})
	}
}

// A row put back by a stop keeps its sender and words, so its paused row says who sent what.
func TestRunList_AStoppedUnsentRowKeepsItsCallerAndWords(t *testing.T) {
	for _, s := range senders[1:] {
		t.Run(s.name, func(t *testing.T) {
			w := stateSetups["unsent"](t, s)
			want := *w.entry(t, "x").row
			w.stop(t, "unsent")
			if len(w.l.entries) != 1 {
				t.Fatalf("list = %q, want the one row put back", w.order())
			}
			e := w.l.entries[0]
			if e.row.Text != want.Text || e.row.Caller == nil || e.row.Caller.Label != want.Caller.Label {
				t.Errorf("put back %q from %v, want %q from %q", e.row.Text, e.row.Caller, want.Text, want.Caller.Label)
			}
			if e.id == "x" || e.row.ID != e.id {
				t.Errorf("put back as %s (message %s), want a new id that is also its message id", e.id, e.row.ID)
			}
		})
	}
}

// The cells that need a second row beside the stopped one: a user row waiting, the caller's second wake, another
// caller's wake, a wire turn the stop names, or the chat's close inside the stopped turn's close window.
func TestRunList_RowStatesWithASecondRow(t *testing.T) {
	cases := []struct {
		name     string
		run      func(t *testing.T, w *tableWorld)
		want     string
		text     map[string]string
		launches int
		// stopPaused is the rows that must wait for the reader's send or Resume (StopPaused), not only paused.
		stopPaused []string
	}{
		{
			name: "a wake opening with a user row waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.user("u", marotte.RunWantPrompt)
				w.l.takeEffects()
				w.withdrawStop(t, "x", 7)
			},
			want: "u:paused x:paused",
		},
		{
			name: "an API row opening with a user row waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.api("x", marotte.RunWantPrompt)
				w.user("u", marotte.RunWantPrompt)
				w.l.takeEffects()
				w.withdrawStop(t, "x", 7)
			},
			want: "u:paused x:paused",
		},
		{
			name: "a user row opening with another waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.user("x", marotte.RunWantPrompt)
				w.user("u", marotte.RunWantPrompt)
				w.l.takeEffects()
				w.withdrawStop(t, "x", 7)
			},
			want: "x:paused u:paused",
		},
		{
			name: "a wake opening, its caller's second wake waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.door("x2", "2 new")
				w.l.takeEffects()
				w.withdrawStop(t, "x", 7)
			},
			want: "x2:paused",
			text: map[string]string{"x2": "2 new"},
		},
		{
			name: "a wake opening, its caller's second wake and a user row waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.door("x2", "2 new")
				w.user("u", marotte.RunWantPrompt)
				w.l.takeEffects()
				w.withdrawStop(t, "x", 7)
			},
			want: "u:paused x2:paused",
		},
		{
			name: "a wake opening whose launch fails, its caller's second wake waiting",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.door("x2", "2 new")
				w.l.takeEffects()
				w.failLaunch(t, "x")
			},
			want:     "x2:starting",
			text:     map[string]string{"x2": "2 new"},
			launches: 1,
		},
		{
			name: "a wake's turn open, its caller's second wake waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.door("x2", "2 new")
				w.mustOrder(t, "x:running x2:pending")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.closed("x", listClose{})
			},
			want: "x2:paused",
		},
		{
			name: "a wake's turn open, its caller's second wake in kiro-cli, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.prompted("x")
				w.door("x2", "2 new")
				w.queued("x2")
				w.mustOrder(t, "x:running x2:in_kas")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.l.turnEnded([]string{"x2"})
				w.closed("x", listClose{})
			},
			want: "x2:paused",
		},
		{
			name: "a wake's turn open, its caller's second wake in kiro-cli, stopped, the wake refused at the boundary",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.prompted("x")
				w.door("x2", "2 new")
				w.queued("x2")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.l.steerBack("x2")
				w.closed("x", listClose{})
			},
			want: "x2:paused",
		},
		{
			name: "a wake's turn open, its caller's second wake waiting, the stop not delivered",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.door("x2", "2 new")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.l.stopUndone(7, func(string) bool { return true })
				w.closed("x", listClose{clean: true})
			},
			want:     "x2:starting",
			launches: 1,
		},
		{
			name: "a wake's turn open, its caller's second wake in kiro-cli, the stop not delivered",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.prompted("x")
				w.door("x2", "2 new")
				w.queued("x2")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.l.stopUndone(7, func(string) bool { return true })
				w.l.turnEnded([]string{"x2"})
				w.closed("x", listClose{clean: true})
			},
			want:     "x2:starting",
			launches: 1,
		},
		{
			name: "a wake's turn opened unsent, its caller's second wake waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.door("x2", "2 new")
				w.openedUnsent("x")
				w.mustOrder(t, "x:starting x2:pending")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.closed("x", listClose{})
			},
			want: "x2:paused",
			text: map[string]string{"x2": "2 new"},
		},
		{
			name: "a wake's turn opened unsent, its caller's second wake and a user row waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.door("x2", "2 new")
				w.user("u", marotte.RunWantPrompt)
				w.openedUnsent("x")
				w.mustOrder(t, "x:starting x2:pending u:pending")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.closed("x", listClose{})
			},
			want: "u:paused x2:paused",
		},
		{
			name: "a wake's turn opened unsent with a user row waiting, stopped",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.user("u", marotte.RunWantPrompt)
				w.openedUnsent("x")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				w.closed("x", listClose{})
			},
			want: "u:paused copy:paused",
		},
		{
			name: "a wake's turn open, its caller's second wake waiting, a third wake sent during the stop",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.door("x2", "2 new")
				w.mustOrder(t, "x:running x2:pending")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				if at := w.door("x3", "3 new"); at != doorReplaced {
					t.Errorf("third wake placed %d, want doorReplaced", at)
				}
				w.closed("x", listClose{})
			},
			want: "x2:paused",
			text: map[string]string{"x2": "3 new"},
		},
		{
			name: "a wake's turn open, its caller's second wake in kiro-cli, a third wake sent during the stop",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.prompted("x")
				w.door("x2", "2 new")
				w.queued("x2")
				w.mustOrder(t, "x:running x2:in_kas")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				if at := w.door("x3", "3 new"); at != doorWithKAS {
					t.Errorf("third wake placed %d, want doorWithKAS", at)
				}
				w.l.turnEnded([]string{"x2"})
				w.closed("x", listClose{})
			},
			want: "x2:paused",
		},
		{
			name: "a plain stop naming kiro-cli's wire turn beside a wake's turn, its caller's second wake waiting",
			run: func(t *testing.T, w *tableWorld) {
				w.wireOpen("t-w", false)
				w.door("x", "1 new")
				w.running("x")
				w.door("x2", "2 new")
				w.mustOrder(t, "t-w:running x:running x2:pending")
				w.l.takeEffects()
				w.l.readerStop("", 7)
				w.mustOrder(t, "t-w:stopping x:stopping x2:paused")
				w.closed("t-w", listClose{})
				w.closed("x", listClose{})
			},
			want: "x2:paused",
		},
		{
			name: "a plain stop naming kiro-cli's wire turn beside a wake's turn, its caller's second wake in kiro-cli",
			run: func(t *testing.T, w *tableWorld) {
				w.wireOpen("t-w", false)
				w.door("x", "1 new")
				w.running("x")
				w.prompted("x")
				w.door("x2", "2 new")
				w.queued("x2")
				w.l.takeEffects()
				w.l.readerStop("", 7)
				w.l.turnEnded([]string{"x2"})
				w.closed("t-w", listClose{})
				w.closed("x", listClose{})
			},
			want: "x2:paused",
		},
		{
			name: "a wake's turn open, its caller's second wake waiting, stopped, the chat closed before the stopped close",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.running("x")
				w.door("x2", "2 new")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				next, _ := w.l.endLife()
				w.l = &runList{}
				w.l.load(next, next, 1)
				w.advance()
			},
			want: "x2:paused",
		},
		{
			name: "a wake's turn opened unsent, stopped, the chat closed before the stopped close",
			run: func(t *testing.T, w *tableWorld) {
				w.door("x", "1 new")
				w.openedUnsent("x")
				w.l.takeEffects()
				w.l.readerStop("x", 7)
				next, _ := w.l.endLife()
				w.l = &runList{}
				w.l.load(next, next, 1)
				w.advance()
			},
			want: "copy:paused",
		},
		{
			name: "another caller's wake joins a paused queue paused, behind it",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.l.takeEffects()
				w.l.doorSend(&runEntry{
					id: "y", kind: marotte.RunKindRow, origin: marotte.RunFromNotify,
					row: &marotte.QueuedPrompt{ID: "y", Text: "build failed", Caller: &marotte.Caller{Label: "ci"}},
				}, w.facts(), 0, false)
			},
			want:       "u:paused y:paused",
			stopPaused: []string{"u", "y"},
		},
		{
			name: "a wake sent once the stopped turn closed stays paused when the user discards their row and an API row comes",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.door("y", "1 new")
				if err := w.l.unqueue("u"); err != nil {
					t.Fatalf("discard u: %v", err)
				}
				w.l.takeEffects()
				w.api("x3", marotte.RunWantPrompt)
			},
			want:       "y:paused x3:paused",
			stopPaused: []string{"y", "x3"},
		},
		{
			name: "a stop of kiro-cli's run with no turn here pauses a user row and a wake; its idle takes nothing",
			run: func(t *testing.T, w *tableWorld) {
				w.kasRunning = true
				w.advance()
				w.user("u", marotte.RunWantPrompt)
				w.door("x", "1 new")
				w.l.takeEffects()
				if plan := w.l.readerStop("", 7); plan.arm != stopRun {
					t.Fatalf("stop arm = %d, want stopRun", plan.arm)
				}
				w.kasRunning = false
				w.advance()
			},
			want:       "u:paused x:paused",
			stopPaused: []string{"u", "x"},
		},
		{
			name: "a wake sent while a stopped run of kiro-cli's ends joins the queue paused",
			run: func(t *testing.T, w *tableWorld) {
				w.kasRunning = true
				w.advance()
				w.l.takeEffects()
				w.l.readerStop("", 7)
				w.door("x", "1 new")
				w.kasRunning = false
				w.advance()
			},
			want:       "x:paused",
			stopPaused: []string{"x"},
		},
		{
			name: "a stop of kiro-cli's run whose cancel is not delivered pauses nothing",
			run: func(t *testing.T, w *tableWorld) {
				w.kasRunning = true
				w.advance()
				w.user("u", marotte.RunWantPrompt)
				w.door("x", "1 new")
				w.l.takeEffects()
				w.l.readerStop("", 7)
				w.l.stopUndone(7, func(string) bool { return true })
				w.kasRunning = false
				w.advance()
			},
			want:     "u:starting x:pending",
			launches: 1,
		},
		{
			name: "a wake sent once the stopped turn closed with nothing paused is taken",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.l.takeEffects()
				w.door("x", "1 new")
			},
			want:     "x:starting",
			launches: 1,
		},
		{
			name: "the user's send after a stop is taken, and its clean close lets the paused rows go in order",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("y", marotte.RunWantPrompt)
				w.door("x", "1 new")
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.mustOrder(t, "y:paused x:paused")
				w.user("u", marotte.RunWantPrompt)
				w.mustOrder(t, "u:starting y:paused x:paused")
				w.running("u")
				w.prompted("u")
				w.l.takeEffects()
				w.closed("u", listClose{clean: true})
			},
			want:     "y:starting x:pending",
			launches: 1,
		},
		{
			name: "Resume lets the paused rows go in order",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("y", marotte.RunWantPrompt)
				w.door("x", "1 new")
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.mustOrder(t, "y:paused x:paused")
				w.l.takeEffects()
				w.l.resumeQueue()
				w.advance()
			},
			want:     "y:starting x:pending",
			launches: 1,
		},
		{
			name: "Resume before the stopped turn ends lets the user's steer kiro-cli returns go at its close",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantEither)
				w.queued("u")
				w.l.readerStop("m-a", 7)
				w.l.takeEffects()
				w.l.resumeQueue()
				w.l.turnEnded([]string{"u"})
				w.closed("m-a", listClose{})
			},
			want:     "u:starting",
			launches: 1,
		},
		{
			name: "a stop after Resume pauses the queue again",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("y", marotte.RunWantPrompt)
				w.door("x", "1 new")
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.l.takeEffects()
				w.l.resumeQueue()
				w.advance()
				w.mustOrder(t, "y:starting x:pending")
				w.running("y")
				w.prompted("y")
				w.user("u", marotte.RunWantPrompt)
				w.l.readerStop("y", 8)
				w.closed("y", listClose{})
			},
			want:     "x:paused u:paused",
			launches: 1,
		},
		{
			name: "send-now on the user's steer takes the user's steers; the wake waiting behind them is paused",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantEither)
				w.queued("u")
				w.user("y", marotte.RunWantEither)
				w.queued("y")
				w.door("x", "1 new")
				w.mustOrder(t, "m-a:running u:in_kas y:in_kas x:pending")
				if !w.l.leadSteer("y") {
					t.Fatal("the user's steer was not marked as the send-now lead")
				}
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.l.turnEnded([]string{"u", "y"})
				w.closed("m-a", listClose{})
				w.mustOrder(t, "y:starting x:paused")
				if got := joinedIDs(w.entry(t, "y").joined); len(got) != 1 || got[0] != "u" {
					t.Errorf("the lead's prompt joins %v, want u", got)
				}
				w.running("y")
				w.prompted("y")
				w.closed("y", listClose{clean: true})
			},
			want:     "x:starting",
			launches: 2,
		},
		{
			name: "a turn kiro-cli opens after the stopped close ends clean",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.door("y", "1 new")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.wireOpen("t-w", false)
				w.mustOrder(t, "t-w:running u:paused y:paused")
				w.closed("t-w", listClose{clean: true})
			},
			want:       "u:paused y:paused",
			stopPaused: []string{"u", "y"},
		},
		{
			name: "a turn kiro-cli opens inside the stopped close ends clean",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.door("y", "1 new")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.wireOpen("t-w", false)
				w.mustOrder(t, "t-w:running m-a:stopping u:paused y:paused")
				w.closed("t-w", listClose{clean: true})
				w.closed("m-a", listClose{})
			},
			want:       "u:paused y:paused",
			stopPaused: []string{"u", "y"},
		},
		{
			name: "a turn kiro-cli opens after the stopped close ends in error",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.door("y", "1 new")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.wireOpen("t-w", false)
				w.closed("t-w", listClose{})
			},
			want:       "u:paused y:paused",
			stopPaused: []string{"u", "y"},
		},
		{
			name: "a row a broken close paused goes after a turn kiro-cli opens ends clean",
			run: func(t *testing.T, w *tableWorld) {
				w.mode = marotte.InterruptQueue
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.l.takeEffects()
				w.closed("m-a", listClose{})
				w.mustOrder(t, "u:paused")
				w.wireOpen("t-w", true)
				w.closed("t-w", listClose{clean: true})
			},
			want:     "u:starting",
			launches: 1,
		},
		{
			name: "the user's send after a stop goes first, then the paused rows after its clean close",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.door("y", "1 new")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
				w.user("v", marotte.RunWantPrompt)
				w.mustOrder(t, "v:starting u:paused y:paused")
				w.running("v")
				w.prompted("v")
				w.l.takeEffects()
				w.closed("v", listClose{clean: true})
			},
			want:     "u:starting y:pending",
			launches: 1,
		},
		{
			name: "the user's send while the stopped turn still ends: kiro-cli's returned wake waits for that reply",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("y", "1 new")
				w.queued("y")
				w.mustOrder(t, "m-a:running y:in_kas")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.user("v", marotte.RunWantPrompt)
				w.l.turnEnded([]string{"y"})
				w.closed("m-a", listClose{})
				w.running("v")
				w.prompted("v")
				w.mustOrder(t, "v:running y:paused")
				if s := w.steered(); s != "" {
					t.Errorf("steered %q into the reply to the user's message, want nothing", s)
				}
				w.l.takeEffects()
				w.closed("v", listClose{clean: true})
			},
			want:     "y:starting",
			launches: 1,
		},
		{
			name: "the user's send while the stopped turn still ends: kiro-cli's returned API steer waits for that reply",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.api("y", marotte.RunWantEither)
				w.queued("y")
				w.mustOrder(t, "m-a:running y:in_kas")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.user("v", marotte.RunWantPrompt)
				w.l.turnEnded([]string{"y"})
				w.closed("m-a", listClose{})
				w.running("v")
				w.prompted("v")
				w.mustOrder(t, "v:running y:paused")
				if s := w.steered(); s != "" {
					t.Errorf("steered %q into the reply to the user's message, want nothing", s)
				}
				w.l.takeEffects()
				w.closed("v", listClose{clean: true})
			},
			want:     "y:starting",
			launches: 1,
		},
		{
			name: "a send-now press on the user's steer: kiro-cli's returned wake waits for the lead's reply",
			run: func(t *testing.T, w *tableWorld) {
				w.head()
				w.door("y", "1 new")
				w.queued("y")
				w.user("s", marotte.RunWantEither)
				w.queued("s")
				w.mustOrder(t, "m-a:running y:in_kas s:in_kas")
				if !w.l.leadSteer("s") {
					t.Fatal("the user's steer was not marked as the send-now lead")
				}
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.l.turnEnded([]string{"y", "s"})
				w.closed("m-a", listClose{})
				w.running("s")
				w.prompted("s")
				w.mustOrder(t, "s:running y:paused")
				if s := w.steered(); s != "" {
					t.Errorf("steered %q into the lead's turn, want nothing", s)
				}
				w.l.takeEffects()
				w.closed("s", listClose{clean: true})
			},
			want:     "y:starting",
			launches: 1,
		},
		{
			name: "a plain stop pauses the row an earlier send-now press left waiting",
			run: func(t *testing.T, w *tableWorld) {
				w.mode = marotte.InterruptQueue
				w.head()
				w.user("u", marotte.RunWantPrompt)
				w.user("v", marotte.RunWantPrompt)
				if h, err := w.l.sendNow("v", "gone", true); h != nil || err != nil {
					t.Fatalf("send-now drawn against a gone turn stopped %v, %v", h, err)
				}
				w.mustOrder(t, "m-a:running v:pending u:pending")
				w.l.takeEffects()
				w.l.readerStop("m-a", 7)
				w.closed("m-a", listClose{})
			},
			want:       "v:paused u:paused",
			stopPaused: []string{"v", "u"},
		},
		{
			name: "a stopped wake waiting, the chat closed and reopened, an API row joins the paused queue",
			run: func(t *testing.T, w *tableWorld) {
				stoppedWakeReopened(t, w)
				w.api("y", marotte.RunWantPrompt)
			},
			want: "x:paused y:paused",
		},
		{
			name: "a stopped wake waiting, the chat closed and reopened, the user's send goes first",
			run: func(t *testing.T, w *tableWorld) {
				stoppedWakeReopened(t, w)
				w.user("u", marotte.RunWantPrompt)
			},
			want:     "u:starting x:paused",
			launches: 1,
		},
		{
			name: "a stopped wake waiting, the chat closed and reopened, Resume sends it",
			run: func(t *testing.T, w *tableWorld) {
				stoppedWakeReopened(t, w)
				w.l.resumeQueue()
				w.advance()
			},
			want:     "x:starting",
			launches: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newTableWorld()
			c.run(t, w)
			if got := copied(w.order(), placedIDs...); got != c.want {
				t.Errorf("list = %q, want %q", got, c.want)
			}
			for id, text := range c.text {
				if got := w.entry(t, id).row.Text; got != text {
					t.Errorf("%s text = %q, want %q", id, got, text)
				}
			}
			if n := len(w.l.takeEffects().launch); n != c.launches {
				t.Errorf("launches = %d, want %d", n, c.launches)
			}
			for _, id := range c.stopPaused {
				if !w.l.stopPaused(w.entry(t, id)) {
					t.Errorf("%s does not wait for the reader's send or Resume (StopPaused false)", id)
				}
			}
		})
	}
}

// stoppedWakeReopened leaves a wake the reader's stop paused on the list the chat's next life loads.
func stoppedWakeReopened(t *testing.T, w *tableWorld) {
	t.Helper()
	w.mode = marotte.InterruptQueue
	w.head()
	w.door("x", "1 new")
	w.l.readerStop("m-a", 7)
	w.closed("m-a", listClose{})
	w.reopen()
	w.mustOrder(t, "x:paused")
	w.l.takeEffects()
}

// No path takes or steers a row while the reader's stop holds the queue, whatever left it pending: a retry listed
// behind the paused rows, or a returned row beside a turn kiro-cli runs.
func TestRunList_NothingIsTakenOrSteeredWhileAStopHoldsTheQueue(t *testing.T) {
	held := func(t *testing.T) *tableWorld {
		t.Helper()
		w := newTableWorld()
		w.head()
		w.user("u", marotte.RunWantPrompt)
		w.l.takeEffects()
		w.l.readerStop("m-a", 7)
		w.closed("m-a", listClose{})
		w.mustOrder(t, "u:paused")
		return w
	}
	t.Run("a pending retry on an idle list", func(t *testing.T) {
		w := held(t)
		w.l.insertPending(&runEntry{
			id: "r", kind: marotte.RunKindRetry, origin: marotte.RunFromRetry, state: marotte.RunPending,
			row: &marotte.QueuedPrompt{ID: "r", Text: "r"},
		})
		w.advance()
		if fx := w.l.takeEffects(); len(fx.launch) != 0 {
			t.Errorf("launched %d entries while the queue is held, want none", len(fx.launch))
		}
	})
	t.Run("a pending row beside a steerable turn kiro-cli runs", func(t *testing.T) {
		w := held(t)
		w.wireOpen("t-w", true)
		w.l.insertPending(&runEntry{
			id: "s", kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, state: marotte.RunPending,
			want: marotte.RunWantEither, row: &marotte.QueuedPrompt{ID: "s", Text: "s"},
		})
		w.advance()
		if fx := w.l.takeEffects(); len(fx.steer) != 0 || len(fx.launch) != 0 {
			t.Errorf("steered %d and launched %d while the queue is held, want none", len(fx.steer), len(fx.launch))
		}
	})
}
