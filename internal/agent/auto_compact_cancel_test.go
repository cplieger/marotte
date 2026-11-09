package agent

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// hookQueue runs a step just before each queue section begins and just after it ends, so another caller's whole
// section falls between this caller's section and its next step.
type hookQueue struct {
	compactQueue
	before func()
	after  func()
}

func (q hookQueue) section(ctx context.Context, chatID marotte.ChatID, fn func(t queueTurn, row func(compactRow) error) error) error {
	if q.before != nil {
		q.before()
	}
	err := q.compactQueue.section(ctx, chatID, fn)
	if q.after != nil {
		q.after()
	}
	return err
}

// cancelOnce is a hook step that cancels the chat's queued compaction the first time it runs; the cancel's own
// section passes it by.
func cancelOnce(t *testing.T, cancel func(context.Context, marotte.ChatID) error) func() {
	t.Helper()
	var fired atomic.Bool
	return func() {
		if fired.Swap(true) {
			return
		}
		if err := cancel(t.Context(), "c1"); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}
}

// The reader's row already shows when the turn first crosses the point, and their cancel lands as the crossing goes to
// write it: the cancel holds, so the row stays gone and the turn's end compacts nothing.
func TestAutoCompact_ACancelAsTheCrossingWritesHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t)
	if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactQueued {
		t.Fatalf("the reader's press during the turn = (%v, %v), want queued", out, err)
	}
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, before: cancelOnce(t, r.ac.cancel)}
	r.reading(92)
	r.ac.noteUsage(t.Context(), "c1", 92)
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 0 {
		t.Errorf("a cancel as the crossing wrote: queued %v, turn-end calls %d; want neither", r.isQueued(), r.count())
	}
}

// Through the record and the real close, the same interleaving: the cancel holds.
func TestAutoCompact_ACancelAsTheCrossingWritesHoldsOnTheRecord(t *testing.T) {
	h, cs, _ := newTestHub()
	r := newCompactRig(90)
	r.onHub(h)
	drained := make(chan struct{})
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) { close(drained) }
	id, _ := h.stagePromptTurn(t, "c1")
	if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactQueued {
		t.Fatalf("the reader's press during the turn = (%v, %v), want queued", out, err)
	}
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, before: cancelOnce(t, h.CancelCompact)}

	r.reading(92)
	h.coord.ObserveContextUsage(t.Context(), "c1", 92)
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	<-drained
	if c, _ := cs.Get(t.Context(), "c1"); r.count() != 0 || c.CompactPending != nil {
		t.Errorf("a cancel as the crossing wrote: turn-end calls %d, queued %v; want neither",
			r.count(), c.CompactPending != nil)
	}
}

// A press after a cancel is the reader asking again: the turn's later crossing keeps it and the turn's end runs it once.
func TestAutoCompact_APressAfterTheCancelRunsAtTheTurnEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	r := newCompactRig(90)
	r.onHub(h)
	drained := make(chan struct{})
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) { close(drained) }
	id, _ := h.stagePromptTurn(t, "c1")

	r.reading(92)
	h.coord.ObserveContextUsage(t.Context(), "c1", 92)
	if err := h.CancelCompact(t.Context(), "c1"); err != nil {
		t.Fatalf("CancelCompact: %v", err)
	}
	if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactQueued {
		t.Fatalf("the press after the cancel = (%v, %v), want queued", out, err)
	}
	r.reading(95)
	h.coord.ObserveContextUsage(t.Context(), "c1", 95)
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	<-drained
	if c, _ := cs.Get(t.Context(), "c1"); r.count() != 1 || c.CompactPending != nil {
		t.Errorf("a press after the cancel: turn-end calls %d, queued %v; want 1 call and the row gone",
			r.count(), c.CompactPending != nil)
	}
}

// The turn end's compaction is refused because KAS opened its own next turn, and the reader cancels while it runs:
// the refusal does not queue the cancelled compaction again behind the new turn.
func TestAutoCompact_ACancelDuringARefusedTurnEndRunHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.setRefuse(true)
	cancel := cancelOnce(t, r.ac.cancel)
	r.onCompact = func() {
		r.kasOpensTheNextTurn()
		cancel()
	}
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 1 || len(r.failures) != 0 {
		t.Errorf("a cancel during the refused run: queued %v, calls %d, failures %v; want not queued, 1 call, no failure",
			r.isQueued(), r.count(), r.failures)
	}
}

// The reader cancels while the turn end's compaction runs, and only then KAS opens its own next turn and refuses the
// call: the cancel still holds, so the new turn's end below the point compacts nothing.
func TestAutoCompact_ACancelBeforeKASOpensTheNextTurnDuringARefusedRunHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.setRefuse(true)
	cancel := cancelOnce(t, r.ac.cancel)
	r.onCompact = func() {
		cancel()
		r.kasOpensTheNextTurn()
	}
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 1 || len(r.failures) != 0 {
		t.Errorf("a cancel before KAS opened its turn: queued %v, calls %d, failures %v; want not queued, 1 call, no failure",
			r.isQueued(), r.count(), r.failures)
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.reading(40)
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 1 {
		t.Errorf("the new turn's end at 40%%: queued %v, calls %d; want no row and no further call", r.isQueued(), r.count())
	}
}

// KAS opens and finishes a turn of its own during the refused run, the reader cancels, and KAS opens another: the
// cancel binds to the run it lands during, so neither later turn runs the cancelled compaction.
func TestAutoCompact_ACancelBetweenTwoKASTurnsDuringARefusedRunHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.setRefuse(true)
	cancel := cancelOnce(t, r.ac.cancel)
	r.onCompact = func() {
		r.kasOpensTheNextTurn()
		r.finishTurn()
		cancel()
		r.setLive(true)
	}
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 1 || len(r.failures) != 0 {
		t.Errorf("a cancel between KAS's turns: queued %v, calls %d, failures %v; want not queued, 1 call, no failure",
			r.isQueued(), r.count(), r.failures)
	}
}

// The reader's press runs at once on the idle chat; KAS opens a turn during the call, that turn crosses the point and
// the reader cancels its row, and KAS refuses the call: the press is not queued again behind that turn.
func TestAutoCompact_ACancelDuringARefusedPressHolds(t *testing.T) {
	r := newCompactRig(90)
	r.setRefuse(true)
	r.onCompact = func() {
		r.setLive(true)
		r.reading(92)
		r.ac.noteUsage(t.Context(), "c1", 92)
		if err := r.ac.cancel(t.Context(), "c1"); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}
	if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactRefused || r.isQueued() {
		t.Errorf("a press refused after the cancel = (%v, %v), queued %v; want refused and no row", out, err, r.isQueued())
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.closeTurn(t, false)
	if r.count() != 1 {
		t.Errorf("the declined turn's end: calls %d; want only the refused press", r.count())
	}
}

// A cancel in one turn binds to the runs it lands during: the next turn's context-limit end compaction, refused under
// KAS's own turn with no row behind it, is still queued for that turn's end and runs there.
func TestAutoCompact_AnEarlierCancelLeavesALaterRefusedRunQueued(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	if err := r.ac.cancel(t.Context(), "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	r.closeTurn(t, false)
	r.turn(t, 50)
	r.setRefuse(true)
	r.onCompact = r.kasOpensTheNextTurn
	r.closeTurn(t, true)
	if !r.isQueued() || r.count() != 1 || len(r.failures) != 0 {
		t.Fatalf("the next turn's refused end: queued %v, calls %d, failures %v; want queued again, 1 call, no failure",
			r.isQueued(), r.count(), r.failures)
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 2 {
		t.Errorf("KAS's turn's end: queued %v, calls %d; want the row gone and 2 calls", r.isQueued(), r.count())
	}
}

// A press after the cancel, during the refused run, is the reader asking again: it waits for the new turn's end.
func TestAutoCompact_APressDuringARefusedTurnEndRunAfterTheCancelWaitsForTheNewTurn(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.setRefuse(true)
	var once sync.Once
	r.onCompact = func() {
		once.Do(func() {
			r.kasOpensTheNextTurn()
			if err := r.ac.cancel(t.Context(), "c1"); err != nil {
				t.Errorf("cancel: %v", err)
			}
			if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactQueued {
				t.Errorf("the press after the cancel = (%v, %v), want queued", out, err)
			}
		})
	}
	r.closeTurn(t, false)
	if !r.isQueued() || len(r.failures) != 0 {
		t.Fatalf("after the refused run: queued %v, failures %v; want the press kept, no failure", r.isQueued(), r.failures)
	}
	r.setRefuse(false)
	r.closeTurn(t, false)
	if r.count() != 2 || r.isQueued() {
		t.Errorf("the new turn's end: calls %d, queued %v; want 2 calls and the row gone", r.count(), r.isQueued())
	}
}

// The reader cancels the turn end's running compaction and presses again under the close's hold, and KAS refuses the
// call with no turn live: the re-press is a new request the refused call did not satisfy, so it runs at the release.
func TestAutoCompact_ARepressAfterACancelDuringARefusedTurnEndRunRunsAtTheRelease(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	r.setRefuse(true)
	var once sync.Once
	r.onCompact = func() {
		once.Do(func() {
			if err := r.ac.cancel(t.Context(), "c1"); err != nil {
				t.Errorf("cancel: %v", err)
			}
			r.pressUnderHold(t)
		})
	}
	accepted, _ := r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), false)
	if accepted || !r.isQueued() {
		t.Fatalf("after the refused turn end: accepted %v, queued %v; want not accepted and the re-press kept", accepted, r.isQueued())
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.ac.afterRelease(t.Context(), "c1", accepted)
	if r.count() != 2 || r.isQueued() || len(r.failures) != 0 {
		t.Errorf("the re-press at the release: calls %d, queued %v, failures %v; want 2 calls, the row gone, no failure",
			r.count(), r.isQueued(), r.failures)
	}
}

// The record queue's section names the open turn, then the finished one, with whether a turn is live; its row writer
// moves the record's row through its stages, keeping when it was queued, and is idempotent; a chat with no lifecycle
// gets no turn and a plain writer.
func TestRecordCompactQueue_SectionNamesTheRequestsTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	q := recordCompactQueue{bc: h.coord}
	pending := func() *marotte.PendingCompaction {
		c, _ := cs.Get(t.Context(), "c1")
		return c.CompactPending
	}
	row := func() bool { return pending() != nil }
	in := func(chatID marotte.ChatID, set ...compactRow) queueTurn {
		var got queueTurn
		err := q.section(t.Context(), chatID, func(turn queueTurn, write func(compactRow) error) error {
			got = turn
			for _, want := range set {
				if err := write(want); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("section(%s): %v", chatID, err)
		}
		return got
	}
	id, _ := h.stagePromptTurn(t, "c1")

	if got := in("c1", compactRowQueued, compactRowQueued); got.id != id || !got.live || !got.open || !got.queueable() || !row() || pending().Running {
		t.Errorf("during the turn: %+v, row %+v; want turn %q, live, open, queueable, and the row queued", got, pending(), id)
	}
	queuedAt := pending().RequestedAt
	if in("c1", compactRowRunning); !row() || !pending().Running || pending().RequestedAt != queuedAt {
		t.Errorf("running row = %+v; want running, still requested at %d", pending(), queuedAt)
	}
	if in("c1", compactRowOff); row() {
		t.Error("the writer's clear left the row on")
	}
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return !h.coord.turns.live("c1") })
	if got := in("c1"); got.id != id || got.live {
		t.Errorf("after the close: %+v; want the finished turn %q, not live", got, id)
	}
	if got := in("c-none", compactRowQueued, compactRowOff); got != (queueTurn{}) {
		t.Errorf("with no lifecycle: %+v; want no turn", got)
	}
}

// A cancel landing as the turn end reads the queue still stops the turn end's compaction at the point.
func TestAutoCompact_ACancelAsTheTurnEndReadsTheQueueHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, before: cancelOnce(t, r.ac.cancel)}
	r.turnEnd(t, false)
	if r.isQueued() || r.count() != 0 {
		t.Errorf("a cancel as the turn end read the queue: queued %v, calls %d; want neither", r.isQueued(), r.count())
	}
}

// The reader cancels just after the turn end's section, before its call goes out, and KAS opens its next turn and
// refuses the call: the cancelled compaction is not queued behind that turn, whose end at 40% compacts nothing.
func TestAutoCompact_ACancelArrivingAsTheTurnEndClaimsHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, after: cancelOnce(t, r.ac.cancel)}
	r.setRefuse(true)
	r.onCompact = r.kasOpensTheNextTurn
	r.turnEnd(t, false)
	if r.isQueued() || r.count() != 1 || len(r.failures) != 0 {
		t.Errorf("a cancel as the turn end claimed: queued %v, calls %d, failures %v; want not queued, 1 call, no failure",
			r.isQueued(), r.count(), r.failures)
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.reading(40)
	r.closeTurn(t, false)
	if r.count() != 1 {
		t.Errorf("KAS's turn's end at 40%%: calls %d; want only the refused run", r.count())
	}
}

// The same on the release: a press under the close's hold, cancelled just after the release's section.
func TestAutoCompact_ACancelArrivingAsTheReleaseClaimsHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t)
	r.finishTurn()
	r.pressUnderHold(t)
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, after: cancelOnce(t, r.ac.cancel)}
	r.setRefuse(true)
	r.onCompact = r.kasOpensTheNextTurn
	r.ac.afterRelease(t.Context(), "c1", false)
	if r.isQueued() || r.count() != 1 || len(r.failures) != 0 {
		t.Errorf("a cancel as the release claimed: queued %v, calls %d, failures %v; want not queued, 1 call, no failure",
			r.isQueued(), r.count(), r.failures)
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.reading(40)
	r.closeTurn(t, false)
	if r.count() != 1 {
		t.Errorf("KAS's turn's end at 40%%: calls %d; want only the refused run", r.count())
	}
}

// The turn end runs just after the reader's cancel's section, before the cancel returns: it runs nothing.
func TestAutoCompact_ATurnEndArrivingAsTheCancelWritesRunsNothing(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	var fired atomic.Bool
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, after: func() {
		if !fired.Swap(true) {
			r.turnEnd(t, false)
		}
	}}
	if err := r.ac.cancel(t.Context(), "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if r.isQueued() || r.count() != 0 {
		t.Errorf("a turn end as the cancel wrote: queued %v, calls %d; want neither", r.isQueued(), r.count())
	}
}

// A call is settled once: settling the refused call again, after KAS's turn closed and its end claimed the queued
// run once more, leaves that run's call alone, toasts nothing, and the run's own end settles it.
func TestAutoCompact_ASecondSettleOfOneCallDoesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newCompactRig(90)
		r.turn(t, 92)
		r.setRefuse(true)
		r.onCompact = r.kasOpensTheNextTurn
		r.closeTurn(t, false)
		if !r.isQueued() || len(r.failures) != 0 {
			t.Fatalf("the refused run under KAS's turn: queued %v, failures %v; want queued again", r.isQueued(), r.failures)
		}
		r.ac.mu.Lock()
		refused := r.ac.chats["c1"].queued.call
		r.ac.mu.Unlock()
		r.setRefuse(false)
		r.onCompact = nil
		r.block = make(chan struct{})
		r.finishTurn()
		go r.turnEnd(t, false)
		synctest.Wait()
		r.ac.settle(t.Context(), "c1", refused, true)
		if !r.isQueued() || len(r.failures) != 0 {
			t.Errorf("a second settle of the refused call: queued %v, failures %v; want the running row kept and no toast",
				r.isQueued(), r.failures)
		}
		close(r.block)
		synctest.Wait()
		if r.isQueued() || r.count() != 2 {
			t.Errorf("KAS's turn's end: queued %v, calls %d; want the row gone and 2 calls", r.isQueued(), r.count())
		}
	})
}

// A cancel after the refused run was queued behind KAS's turn removes it: that turn's end compacts nothing.
func TestAutoCompact_ACancelAfterARefusalHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.setRefuse(true)
	r.onCompact = r.kasOpensTheNextTurn
	r.closeTurn(t, false)
	if !r.isQueued() {
		t.Fatal("the refused run under KAS's turn was not queued again")
	}
	if err := r.ac.cancel(t.Context(), "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.reading(92)
	r.closeTurn(t, false)
	if r.isQueued() || r.count() != 1 {
		t.Errorf("KAS's turn's end after the cancel: queued %v, calls %d; want no row and only the refused call",
			r.isQueued(), r.count())
	}
}

// A cancel that lands after the turn end claimed the queued compaction does not stop the call, so the row stays
// running until the call ends, and then goes off with no toast whatever KAS answered.
func TestAutoCompact_ACancelAfterTheClaimKeepsTheRowRunningUntilTheCallEnds(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "refused"}[refuse], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newCompactRig(90)
				r.turn(t, 92)
				r.finishTurn()
				r.setRefuse(refuse)
				r.block = make(chan struct{})
				ended := make(chan struct{})
				go func() {
					r.turnEnd(t, false)
					close(ended)
				}()
				synctest.Wait()
				if err := r.ac.cancel(t.Context(), "c1"); err != nil {
					t.Fatalf("cancel: %v", err)
				}
				want := []compactRow{compactRowQueued, compactRowRunning}
				if got := r.rowLog(); !slices.Equal(got, want) {
					t.Errorf("rows after a cancel mid-call = %v, want %v: the call still runs", got, want)
				}
				close(r.block)
				<-ended
				want = append(want, compactRowOff)
				if got := r.rowLog(); !slices.Equal(got, want) || len(r.failures) != 0 || r.count() != 1 {
					t.Errorf("after the call: rows %v, failures %v, calls %d; want rows %v, no failure, 1 call",
						got, r.failures, r.count(), want)
				}
			})
		})
	}
}
