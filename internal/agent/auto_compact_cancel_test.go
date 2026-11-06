package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// hookQueue runs a step just before a queue write or a queue read, so another caller's step finishes before that
// call's section begins.
type hookQueue struct {
	compactQueue
	beforeMark    func()
	beforePending func()
}

func (q hookQueue) markIfLive(ctx context.Context, chatID marotte.ChatID, admit func(turn string) bool) (bool, error) {
	if q.beforeMark != nil {
		q.beforeMark()
	}
	return q.compactQueue.markIfLive(ctx, chatID, admit)
}

func (q hookQueue) pending(ctx context.Context, chatID marotte.ChatID) bool {
	if q.beforePending != nil {
		q.beforePending()
	}
	return q.compactQueue.pending(ctx, chatID)
}

// cancelOnce is a hook step that cancels the chat's queued compaction the first time it runs.
func cancelOnce(t *testing.T, cancel func(context.Context, marotte.ChatID) error) func() {
	t.Helper()
	var once sync.Once
	return func() {
		once.Do(func() {
			if err := cancel(t.Context(), "c1"); err != nil {
				t.Errorf("cancel: %v", err)
			}
		})
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
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, beforeMark: cancelOnce(t, r.ac.cancel)}
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
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, beforeMark: cancelOnce(t, h.CancelCompact)}

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

// The record queue decides with the turn the request belongs to: admit refuses only a new request, and withdraw hands
// decline the open turn, then the finished one, and on a chat with no lifecycle only clears.
func TestRecordCompactQueue_DecidesWithTheRequestsTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	q := recordCompactQueue{bc: h.coord}
	row := func() bool {
		c, _ := cs.Get(t.Context(), "c1")
		return c.CompactPending != nil
	}
	var seen string
	refuse := func(turn string) bool {
		seen = turn
		return false
	}
	id, _ := h.stagePromptTurn(t, "c1")

	if queued, err := q.markIfLive(t.Context(), "c1", refuse); err != nil || queued || row() || seen != id {
		t.Errorf("a refused new request: (%v, %v), row %v, admit saw %q; want (false, nil), no row, %q", queued, err, row(), seen, id)
	}
	if queued, err := q.markIfLive(t.Context(), "c1", nil); err != nil || !queued || !row() {
		t.Fatalf("the reader's press: (%v, %v), row %v; want queued", queued, err, row())
	}
	if queued, err := q.markIfLive(t.Context(), "c1", refuse); err != nil || !queued || !row() {
		t.Errorf("a refusing admit over a request already there: (%v, %v), row %v; want it kept", queued, err, row())
	}
	var declined string
	decline := func(turn string) { declined = turn }
	if err := q.withdraw(t.Context(), "c1", decline); err != nil || declined != id || row() {
		t.Errorf("withdraw during the turn: %v, declined %q, row %v; want %q and the row gone", err, declined, row(), id)
	}
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return !h.coord.turns.live("c1") })
	declined = ""
	if err := q.withdraw(t.Context(), "c1", decline); err != nil || declined != id {
		t.Errorf("withdraw after the close: %v, declined %q; want the finished turn %q", err, declined, id)
	}
	called := false
	if err := q.withdraw(t.Context(), "c-none", func(string) { called = true }); err != nil || called {
		t.Errorf("withdraw with no lifecycle: %v, decline called %v; want a plain clear", err, called)
	}
}

// A cancel landing as the turn end reads the queue still stops the turn end's compaction at the point.
func TestAutoCompact_ACancelAsTheTurnEndReadsTheQueueHolds(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	r.ac.queue = hookQueue{compactQueue: r.ac.queue, beforePending: cancelOnce(t, r.ac.cancel)}
	r.turnEnd(t, false)
	if r.isQueued() || r.count() != 0 {
		t.Errorf("a cancel as the turn end read the queue: queued %v, calls %d; want neither", r.isQueued(), r.count())
	}
}
