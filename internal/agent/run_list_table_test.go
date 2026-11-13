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
	stops      int
	stopSeq    uint64
	nextSeq    uint64
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
		newMessageID: func() string { return "m-unread" },
		nextSeq:      w.nextSeq, stopSeq: w.stopSeq, stops: w.stops,
		admit: !w.kasRunning, sessionCall: !w.kasRunning,
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

// compacting takes the compaction id once nothing runs ahead of it and writes its call.
func (w *tableWorld) compacting(id string) {
	w.advance()
	w.l.compactionStart(id, w.facts())
}

func (w *tableWorld) wireOpen(turnID string, bracketed bool) {
	w.nextSeq++
	w.l.turnOpened(openFacts{turnID: turnID, wire: true, bracketed: bracketed, seq: w.nextSeq})
}

// One row per head shape and event, each stating the verb a send gets and the list order after the event, from the
// design's tables (3.4 operations, 3.6 VerbFor, 3.8 stops).
func TestRunList_Table(t *testing.T) {
	type row struct {
		run       func(t *testing.T, w *tableWorld) string
		name      string
		wantOrder string
		wantVerb  string
	}
	clean := listClose{clean: true}
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
			name: "a running compaction: the send queues behind it and is taken once it ends",
			run: func(_ *testing.T, w *tableWorld) string {
				c, _ := w.l.requestCompaction(marotte.RunFromRequest, "")
				w.compacting(c.id)
				verb := w.send("m-q", marotte.SendPlain, "")
				if w.order() != c.id+":running m-q:pending" {
					return "under the compaction: " + w.order()
				}
				w.l.compactionEnded(c.id, true, nil)
				w.advance()
				return string(verb)
			},
			wantVerb: "queue", wantOrder: "m-q:starting",
		},
		{
			name: "a cancel with no target during a compaction stops nothing",
			run: func(_ *testing.T, w *tableWorld) string {
				c, _ := w.l.requestCompaction(marotte.RunFromRequest, "")
				w.compacting(c.id)
				plan := w.l.stopHead("", 9)
				stale := w.l.stopHead("t-gone", 10)
				if plan.arm != stopNothing || stale.arm != stopNothing {
					return "stopped something"
				}
				return "nothing"
			},
			wantVerb: "nothing", wantOrder: "compact-1:running",
		},
		{
			name: "a clean turn end at the point lists its compaction ahead of the queued rows",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				w.send("m-q1", marotte.SendPlain, marotte.InterruptQueue)
				c := clean
				c.compact = marotte.RunFromTurnEnd
				w.closed("m-a", c)
				return ""
			},
			wantOrder: "compact-1:starting m-q1:pending",
		},
		{
			name: "a context-limit turn end pauses the rows it left; a send during its compaction goes first",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				w.send("m-old", marotte.SendPlain, marotte.InterruptQueue)
				w.closed("m-a", listClose{compact: marotte.RunFromContextLimit})
				w.l.compactionStart("compact-1", w.facts())
				return string(w.send("m-new", marotte.SendPlain, ""))
			},
			wantVerb: "queue", wantOrder: "compact-1:running m-new:pending m-old:paused",
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
			name: "send-now during a stopped turn's compaction stops nothing and the row goes first after it",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				w.send("m-q0", marotte.SendPlain, marotte.InterruptQueue)
				w.l.stopHead("m-a", 7)
				w.closed("m-a", listClose{compact: marotte.RunFromTurnEnd})
				w.l.compactionStart("compact-1", w.facts())
				h, err := w.l.sendNow("m-q0", "", false)
				if h != nil || err != nil {
					return "stopped " + h.id
				}
				return "nothing"
			},
			wantVerb: "nothing", wantOrder: "compact-1:running m-q0:pending",
		},
		{
			name: "send-now on a held row is refused",
			run: func(_ *testing.T, w *tableWorld) string {
				w.l.holdCarried(&marotte.QueuedPrompt{ID: "m-held", Text: "x", Resends: []string{"s-1"}})
				if _, err := w.l.sendNow("m-held", "", false); err != errRowHeld {
					return "not refused"
				}
				return "refused"
			},
			wantVerb: "refused", wantOrder: "m-held:held",
		},
		{
			name: "a wire turn opening under a running compaction goes beside it, and a refusal requeues the compaction behind it",
			run: func(_ *testing.T, w *tableWorld) string {
				c, _ := w.l.requestCompaction(marotte.RunFromRequest, "")
				w.compacting(c.id)
				w.wireOpen("t-w", true)
				if w.order() != c.id+":running t-w:running" {
					return "a wire turn under a compaction: " + w.order()
				}
				w.l.compactionEnded(c.id, false, nil)
				return string(marotte.VerbFor(w.l.project(), "", marotte.SendPlain))
			},
			wantVerb: "steer", wantOrder: "t-w:running compact-1:pending",
		},
		{
			name: "a wire turn opening while a compaction is taken but not written puts it back",
			run: func(_ *testing.T, w *tableWorld) string {
				w.l.requestCompaction(marotte.RunFromRequest, "")
				w.advance()
				w.wireOpen("t-w", true)
				return ""
			},
			wantOrder: "t-w:running compact-1:pending",
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
		{
			name: "a compaction refused with no turn beside it fails, with one toast",
			run: func(_ *testing.T, w *tableWorld) string {
				c, _ := w.l.requestCompaction(marotte.RunFromThreshold, "")
				w.compacting(c.id)
				w.l.compactionEnded(c.id, false, nil)
				return map[bool]string{true: "toast", false: "silent"}[len(w.l.takeEffects().toast) == 1]
			},
			wantVerb: "toast", wantOrder: "",
		},
		{
			name: "a cancelled compaction's turn compacts nothing at its end",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				c, _ := w.l.requestCompaction(marotte.RunFromThreshold, "t-m-a")
				w.l.cancelCompaction(c.id)
				end := clean
				end.compact = marotte.RunFromTurnEnd
				w.closed("m-a", end)
				return ""
			},
			wantOrder: "",
		},
		{
			name: "a row queued as a turn end's compaction returns waits behind the row queued during the turn",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				w.send("m-q1", marotte.SendPlain, marotte.InterruptQueue)
				end := clean
				end.compact = marotte.RunFromTurnEnd
				w.closed("m-a", end)
				w.l.compactionStart("compact-1", w.facts())
				w.l.compactionEnded("compact-1", true, nil)
				verb := w.send("m-q2", marotte.SendQueuePrompt, "")
				w.advance()
				return string(verb)
			},
			wantVerb: "queue", wantOrder: "m-q1:starting m-q2:pending",
		},
		{
			name: "a row queued under a stopped close's hold, then its compaction, reads behind the compaction ahead of the paused row",
			run: func(_ *testing.T, w *tableWorld) string {
				w.send("m-a", marotte.SendPlain, "")
				w.running("m-a")
				w.send("m-q0", marotte.SendPlain, marotte.InterruptQueue)
				w.l.stopHead("m-a", 7)
				_, e := w.l.find("m-a")
				w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq, compact: marotte.RunFromTurnEnd}, w.facts())
				w.send("m-q1", marotte.SendQueuePrompt, "")
				return string(marotte.RunViewOf(w.l.project(), 0).Entries[2].Wait.After)
			},
			wantVerb: "compaction", wantOrder: "m-a:closing compact-1:pending m-q1:pending m-q0:paused",
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
