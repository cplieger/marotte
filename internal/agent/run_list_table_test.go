package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// tableWorld is a run list with a modelled admission slot: every take is granted unless held, and kiro-cli's state
// admits a take unless kasRunning.
type tableWorld struct {
	l          *runList
	tokens     uint64
	held       bool
	kasRunning bool
	stopSeq    uint64
	nextSeq    uint64
	offers     uint64
	// mode is the interrupt_mode a door send takes (doorSubmitLocked).
	mode marotte.InterruptMode
}

func newTableWorld() *tableWorld { return &tableWorld{l: &runList{}} }

func (w *tableWorld) facts() *orderFacts {
	return &orderFacts{
		reserve: func(e *runEntry, _ uint64) (command.Reservation, takeAnswer) {
			if w.held {
				return command.Reservation{}, takeWait
			}
			w.held = true
			w.tokens++
			return command.Reservation{Chat: "c1", Token: w.tokens}, takeGranted
		},
		newOffer: func() uint64 { w.offers++; return w.offers },
		nextSeq:  w.nextSeq, stopSeq: w.stopSeq, admit: !w.kasRunning,
	}
}

func (w *tableWorld) advance() { w.l.advance(w.facts()) }

// order is the list as "id:state" words.
func (w *tableWorld) order() string {
	words := make([]string, 0, len(w.l.entries))
	for _, e := range w.l.entries {
		words = append(words, e.id+":"+string(e.state))
	}
	return strings.Join(words, " ")
}

func (w *tableWorld) send(id string, kind marotte.SendKind, mode marotte.InterruptMode) marotte.SendVerb {
	verb := marotte.VerbFor(w.l.project(), mode, kind)
	if verb == marotte.SendSteer {
		return verb
	}
	w.l.submit(&runEntry{
		id: id, kind: marotte.RunKindRow, origin: marotte.RunFromPrompt,
		row: &marotte.QueuedPrompt{ID: id, Text: id},
	}, w.facts(), 0, verb == marotte.SendPrompt)
	return verb
}

// running opens a prompt turn for the starting entry id and marks its call written.
func (w *tableWorld) running(id string) {
	_, e := w.l.find(id)
	w.nextSeq++
	w.l.turnOpened(openFacts{turnID: "t-" + id, admission: e.res.Token, seq: w.nextSeq})
	w.l.turnSent("t-" + id)
}

// closed closes id's turn, ends its tail and gives its reservation back.
func (w *tableWorld) closed(id string, c listClose) {
	_, e := w.l.find(id)
	c.turnID, c.seq = e.turnID, w.nextSeq
	token := e.res.Token
	w.l.turnClosed(c, w.facts())
	w.l.tailEnded(id, false)
	w.held = false
	w.l.reservationReleased(token)
	w.advance()
}

func (w *tableWorld) wireOpen(turnID string, bracketed bool) {
	w.nextSeq++
	w.l.turnOpened(openFacts{turnID: turnID, wire: true, bracketed: bracketed, seq: w.nextSeq})
}

// One row per head shape and event, each stating the verb a send gets and the list order after the event.
func TestRunList_Table(t *testing.T) {
	t.Parallel()
	type row struct {
		run       func(t *testing.T, w *tableWorld) string
		name      string
		wantOrder string
		wantVerb  string
	}
	rows := []row{
		{
			name: "no head: a prompt is taken",
			run: func(_ *testing.T, w *tableWorld) string {
				return string(w.send("m-p", marotte.SendPlain, ""))
			},
			wantVerb: "prompt", wantOrder: "m-p:starting",
		},
		{
			name: "a withdrawn take's late refusal leaves the row's next take starting",
			run: func(_ *testing.T, w *tableWorld) string {
				verb := w.send("m-p", marotte.SendPlain, "")
				_, e := w.l.find("m-p")
				first := e.res.Token
				w.l.stopHead("m-p", 1)
				w.held = false
				w.l.reservationReleased(first)
				if _, err := w.l.sendNow("m-p", "", true); err != nil {
					return err.Error()
				}
				w.advance()
				w.l.launched("m-p", first, "", launchWithdrawn, command.ErrTakeWithdrawn)
				return string(verb)
			},
			wantVerb: "prompt", wantOrder: "m-p:starting",
		},
		{
			name: "no head: a queue_prompt has nothing to queue behind",
			run: func(_ *testing.T, w *tableWorld) string {
				return string(marotte.VerbFor(w.l.project(), "", marotte.SendQueuePrompt))
			},
			wantVerb: "prompt", wantOrder: "",
		},
		{
			name: "a running prompt turn in steer mode: a plain send steers, a labelled one queues",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				steer := w.send("m-s", marotte.SendPlain, "")
				return string(steer) + "," + string(w.send("m-l", marotte.SendLabelled, ""))
			},
			wantVerb: "steer,queue", wantOrder: "m-a:running m-l:pending",
		},
		{
			name: "a running prompt turn in queue mode: the send queues",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				return string(w.send("m-q", marotte.SendPlain, marotte.InterruptQueue))
			},
			wantVerb: "queue", wantOrder: "m-a:running m-q:pending",
		},
		{
			name: "a starting turn: the send queues, never steers",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				return string(w.send("m-q", marotte.SendPlain, ""))
			},
			wantVerb: "queue", wantOrder: "m-a:starting m-q:pending",
		},
		{
			name: "kiro-cli runs with no turn here: the send queues behind the run, and goes once it reports idle",
			run: func(_ *testing.T, w *tableWorld) string {
				w.kasRunning = true
				w.advance()
				verb := w.send("m-q", marotte.SendPlain, "")
				w.kasRunning = false
				w.advance()
				return string(verb)
			},
			wantVerb: "queue", wantOrder: "m-q:starting",
		},
		{
			name: "a stopped turn's rows wait; a send after the stop is listed ahead of them",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				w.send("m-q0", marotte.SendPlain, marotte.InterruptQueue)
				if plan := w.l.stopHead("m-a", 7); plan.arm != stopRunning {
					return "no stop"
				}
				return string(w.send("m-new", marotte.SendPlain, ""))
			},
			wantVerb: "queue", wantOrder: "m-a:stopping m-new:pending m-q0:paused",
		},
		{
			name: "send-now on a held row is refused",
			run: func(_ *testing.T, w *tableWorld) string {
				w.l.entries = append(w.l.entries, &runEntry{
					id: "m-held", kind: marotte.RunKindRow, origin: marotte.RunFromCarried, state: marotte.RunHeld,
					row: &marotte.QueuedPrompt{ID: "m-held", Text: "x", Resends: []string{"s-1"}},
				})
				if _, err := w.l.sendNow("m-held", "", false); err != errRowHeld {
					return "not refused"
				}
				return "refused"
			},
			wantVerb: "refused", wantOrder: "m-held:held",
		},
		{
			name: "a lone fold-opened wire turn admits a prompt taken beside it",
			run: func(_ *testing.T, w *tableWorld) string {
				w.wireOpen("t-f", false)
				w.advance()
				return string(w.send("m-p", marotte.SendPlain, ""))
			},
			wantVerb: "prompt", wantOrder: "t-f:running m-p:starting",
		},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			w := newTableWorld()
			got := r.run(t, w)
			if got != r.wantVerb {
				t.Errorf("answer = %q, want %q (list %s)", got, r.wantVerb, w.order())
			}
			if w.order() != r.wantOrder {
				t.Errorf("list = %q, want %q", w.order(), r.wantOrder)
			}
			if ids := entryIDs(w.l); len(slices.Compact(slices.Sorted(slices.Values(ids)))) != len(ids) {
				t.Errorf("list ids repeat: %v", ids)
			}
		})
	}
}
