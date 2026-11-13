package agent

import (
	"errors"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// fire is a reading at the failsafe point during id's turn: the failsafe's stop under token, its resume m-resume.
func (w *tableWorld) fire(id string, token uint64) stopPlan {
	turn := ""
	if _, e := w.l.find(id); e != nil {
		turn = e.turnID
	}
	return w.l.failsafe(failsafeFacts{turnID: turn, resumeID: "m-resume", token: token})
}

// firedOn is chat m-a's running turn with m-q1 queued behind it, stopped by the failsafe under token 1.
func firedOn(t *testing.T) *tableWorld {
	t.Helper()
	w := newTableWorld()
	w.send("m-a", marotte.SendPlain, "")
	w.running("m-a")
	w.send("m-q1", marotte.SendPlain, marotte.InterruptQueue)
	if plan := w.fire("m-a", 1); plan.arm != stopRunning || plan.entry == nil || plan.entry.id != "m-a" {
		t.Fatalf("Setup: a reading at 95%% during m-a's turn stopped %+v, want m-a's running turn; list %s", plan, w.order())
	}
	return w
}

// compacted closes the stopped m-a cancelled, then runs the compaction its close lets the list take, accepted.
func (w *tableWorld) compacted(t *testing.T) {
	t.Helper()
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	if w.order() != "compact-1:running m-resume:pending m-q1:paused" {
		t.Fatalf("Setup: list under the failsafe compaction = %q", w.order())
	}
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
}

func entryOrigin(w *tableWorld, id string) marotte.RunEntryOrigin {
	if _, e := w.l.find(id); e != nil {
		return e.origin
	}
	return ""
}

// The fire stops the turn as the reader's Cancel does (the rows behind it paused) and lists, at the front of what
// waits, the compaction and then the resume that continues the stopped work.
func TestFailsafe_AReadingAt95StopsTheTurnAndListsTheCompactionThenTheResume(t *testing.T) {
	w := firedOn(t)

	if got, want := w.order(), "m-a:stopping compact-1:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
	if got := entryOrigin(w, "compact-1"); got != marotte.RunFromFailsafe {
		t.Errorf("compaction origin = %q, want %q", got, marotte.RunFromFailsafe)
	}
	_, r := w.l.find("m-resume")
	if r == nil {
		t.Fatal("no resume entry listed")
	}
	if r.kind != marotte.RunKindResume || r.origin != marotte.RunFromFailsafe || r.row == nil || r.row.Text != resumeText {
		t.Errorf("resume = kind %q origin %q row %+v, want a %q entry from %q carrying the resume text",
			r.kind, r.origin, r.row, marotte.RunKindResume, marotte.RunFromFailsafe)
	}
}

// One fire per turn: a second reading while the turn is stopping, and a reading after an undone stop put it back
// running, stop nothing and list nothing more.
func TestFailsafe_FiresOncePerTurn(t *testing.T) {
	t.Run("a second reading while the turn stops", func(t *testing.T) {
		w := firedOn(t)
		if plan := w.fire("m-a", 2); plan.arm != stopNothing {
			t.Errorf("second fire = %+v, want nothing", plan)
		}
		if got, want := w.order(), "m-a:stopping compact-1:pending m-resume:pending m-q1:paused"; got != want {
			t.Errorf("list = %q, want %q", got, want)
		}
	})
	t.Run("a reading after an undone stop", func(t *testing.T) {
		w := firedOn(t)
		w.l.stopUndone(1, func(string) bool { return true })
		if plan := w.fire("m-a", 2); plan.arm != stopNothing {
			t.Errorf("fire after the undo = %+v, want nothing", plan)
		}
		if got, want := w.order(), "m-a:running compact-1:pending m-q1:pending"; got != want {
			t.Errorf("list = %q, want %q", got, want)
		}
	})
}

// A compaction already listed for the turn (the point's crossing, a press) is the one the failsafe runs: one
// compaction, one resume.
func TestFailsafe_TakesOverTheQueuedCompaction(t *testing.T) {
	for _, origin := range []marotte.RunEntryOrigin{marotte.RunFromThreshold, marotte.RunFromRequest} {
		t.Run(string(origin), func(t *testing.T) {
			w := newTableWorld()
			w.send("m-a", marotte.SendPlain, "")
			w.running("m-a")
			w.send("m-q1", marotte.SendPlain, marotte.InterruptQueue)
			w.l.requestCompaction(origin, "t-m-a")
			if plan := w.fire("m-a", 1); plan.arm != stopRunning {
				t.Fatalf("fire = %+v, want m-a stopped", plan)
			}
			if got, want := w.order(), "m-a:stopping compact-1:pending m-resume:pending m-q1:paused"; got != want {
				t.Errorf("list = %q, want %q", got, want)
			}
			if got := entryOrigin(w, "compact-1"); got != marotte.RunFromFailsafe {
				t.Errorf("compaction origin = %q, want %q", got, marotte.RunFromFailsafe)
			}
		})
	}
}

// A turn whose compaction the reader cancelled compacts nothing unasked, so the failsafe does not fire in it.
func TestFailsafe_ADeclinedTurnDoesNotFire(t *testing.T) {
	w := newTableWorld()
	w.send("m-a", marotte.SendPlain, "")
	w.running("m-a")
	c, _ := w.l.requestCompaction(marotte.RunFromThreshold, "t-m-a")
	w.l.cancelCompaction(c.id)

	if plan := w.fire("m-a", 1); plan.arm != stopNothing {
		t.Errorf("fire in a declined turn = %+v, want nothing", plan)
	}
	if got := w.order(); got != "m-a:running" {
		t.Errorf("list = %q, want m-a running alone", got)
	}
}

// Only a lone running turn with a turn to resume fires: nothing else on the list is stopped or gets a resume.
func TestFailsafe_OnlyALoneRunningTurnFires(t *testing.T) {
	type row struct {
		setup func(w *tableWorld) (target string)
		name  string
		fires bool
	}
	rows := []row{
		{name: "a lone running prompt turn", fires: true, setup: func(w *tableWorld) string {
			w.send("m-a", marotte.SendPlain, "")
			w.running("m-a")
			return "m-a"
		}},
		{name: "a lone bracketed wire turn", fires: true, setup: func(w *tableWorld) string {
			w.wireOpen("t-w", true)
			return "t-w"
		}},
		{name: "a lone fold-opened wire turn, which its compaction closes", setup: func(w *tableWorld) string {
			w.wireOpen("t-f", false)
			return "t-f"
		}},
		{name: "kiro-cli's run with no turn here", setup: func(w *tableWorld) string {
			w.kasRunning = true
			w.advance()
			return "run-1"
		}},
		{name: "a take whose turn has not opened", setup: func(w *tableWorld) string {
			w.send("m-a", marotte.SendPlain, "")
			return "m-a"
		}},
		{name: "a turn the reader stopped", setup: func(w *tableWorld) string {
			w.send("m-a", marotte.SendPlain, "")
			w.running("m-a")
			w.l.stopHead("m-a", 9)
			return "m-a"
		}},
		{name: "a closing turn", setup: func(w *tableWorld) string {
			w.send("m-a", marotte.SendPlain, "")
			w.running("m-a")
			_, e := w.l.find("m-a")
			w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq, clean: true}, w.facts())
			return "m-a"
		}},
		{name: "a running compaction", setup: func(w *tableWorld) string {
			c, _ := w.l.requestCompaction(marotte.RunFromRequest, "")
			w.compacting(c.id)
			return c.id
		}},
		{name: "a prompt taken beside a fold-opened wire turn", setup: func(w *tableWorld) string {
			w.wireOpen("t-f", false)
			w.advance()
			w.send("m-p", marotte.SendPlain, "")
			w.running("m-p")
			return "m-p"
		}},
		{name: "kiro-cli's bracketed turn ahead of a prompt running beside it", setup: func(w *tableWorld) string {
			w.send("m-a", marotte.SendPlain, "")
			w.running("m-a")
			w.wireOpen("t-w", true)
			return "t-w"
		}},
		{name: "a reading naming another turn", setup: func(w *tableWorld) string {
			w.send("m-a", marotte.SendPlain, "")
			w.running("m-a")
			return "t-other"
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			w := newTableWorld()
			target := r.setup(w)
			turn := target
			if _, e := w.l.find(target); e != nil {
				turn = e.turnID
			}
			before := w.order()
			plan := w.l.failsafe(failsafeFacts{turnID: turn, resumeID: "m-resume", token: 1})
			_, resume := w.l.find("m-resume")
			if fired := plan.arm == stopRunning && resume != nil; fired != r.fires {
				t.Errorf("fired = %v (plan %+v, list %s), want %v", fired, plan, w.order(), r.fires)
			}
			if !r.fires && w.order() != before {
				t.Errorf("list = %q after a fire that stopped nothing, want it unchanged %q", w.order(), before)
			}
		})
	}
}

// The whole sequence on the list: the stopped turn's close, the compaction, the resume, then the rows the stop
// paused, in that order.
func TestFailsafe_TheResumeRunsAfterTheCompactionAndBeforeTheQueuedRows(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{})
	if got, want := w.order(), "compact-1:starting m-resume:pending m-q1:paused"; got != want {
		t.Fatalf("list after the stopped close = %q, want %q", got, want)
	}
	w.l.compactionStart("compact-1", w.facts())
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
	if got, want := w.order(), "m-resume:starting m-q1:paused"; got != want {
		t.Fatalf("list after the compaction = %q, want %q", got, want)
	}
	w.running("m-resume")
	w.closed("m-resume", listClose{clean: true})
	if got, want := w.order(), "m-q1:starting"; got != want {
		t.Errorf("list after the resume's clean end = %q, want %q", got, want)
	}
}

// A compaction that failed or that kiro-cli refused with no turn of its own beside it continues nothing: the resume
// goes, the rows stay paused and the reader is told.
func TestFailsafe_AFailedOrRefusedCompactionSendsNoResume(t *testing.T) {
	for _, tc := range []struct {
		err  error
		name string
	}{{name: "a refusal", err: nil}, {name: "a failure", err: errors.New("the summary failed")}} {
		t.Run(tc.name, func(t *testing.T) {
			w := firedOn(t)
			w.closed("m-a", listClose{})
			w.l.compactionStart("compact-1", w.facts())
			w.l.takeEffects()
			w.l.compactionEnded("compact-1", false, tc.err)
			fx := w.l.takeEffects()
			w.advance()
			if got := w.order(); got != "m-q1:paused" {
				t.Errorf("list = %q, want only m-q1 paused", got)
			}
			if len(fx.toast) != 1 {
				t.Errorf("toasts = %d, want 1", len(fx.toast))
			}
		})
	}
}

// kiro-cli refusing the compaction because its own turn ran beside it requeues the compaction behind that turn; the
// resume stays behind the compaction.
func TestFailsafe_ARefusalKiroCLICausedKeepsTheResumeBehindTheRequeue(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.wireOpen("t-w", true)
	w.l.compactionEnded("compact-1", false, nil)
	if got, want := w.order(), "t-w:running compact-1:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// The reader's cancel of the failsafe compaction while it waits withdraws the resume, and holds for the turn: its
// end compacts nothing, even refused for the context's size.
func TestFailsafe_CancellingThePendingCompactionSendsNoResumeAndHoldsForTheTurn(t *testing.T) {
	w := firedOn(t)
	w.l.cancelCompaction("compact-1")
	if got, want := w.order(), "m-a:stopping m-q1:paused"; got != want {
		t.Errorf("list after the cancel = %q, want %q", got, want)
	}
	w.closed("m-a", listClose{compact: marotte.RunFromContextLimit})
	if got := w.order(); got != "m-q1:paused" {
		t.Errorf("list after the turn's end = %q, want no compaction listed", got)
	}
}

// A running compaction cannot be stopped, but the reader's cancel of it means the stopped work is not continued.
func TestFailsafe_CancellingTheRunningCompactionSendsNoResume(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.l.cancelCompaction("compact-1")
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
	if got := w.order(); got != "m-q1:paused" {
		t.Errorf("list = %q, want only m-q1 paused", got)
	}
}

// A failsafe compaction that did not bring the context under the point gets no second failsafe until a reading
// under the point shows the context freed.
func TestFailsafe_NoSecondFailsafeUntilAReadingUnderThePoint(t *testing.T) {
	w := firedOn(t)
	w.compacted(t)
	w.running("m-resume")
	if plan := w.fire("m-resume", 2); plan.arm != stopNothing {
		t.Fatalf("fire in the resume before any reading under the point = %+v, want nothing", plan)
	}
	w.l.usageUnderThePoint()
	if plan := w.fire("m-resume", 3); plan.arm != stopRunning {
		t.Errorf("fire after a reading under the point = %+v, want the resume stopped", plan)
	}
}

// The send-now arrow puts its row next behind the compaction and drops the resume: the reader chose what runs after.
func TestFailsafe_ASendNowPressGoesAfterTheCompactionAndDropsTheResume(t *testing.T) {
	w := firedOn(t)
	h, err := w.l.sendNow("m-q1", "", false)
	if h != nil || err != nil {
		t.Errorf("send-now = (%v, %v), want nothing stopped", h, err)
	}
	if got, want := w.order(), "m-a:stopping compact-1:pending m-q1:pending"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// A message sent during the sequence waits behind the resume and ahead of the rows the stop paused.
func TestFailsafe_AMessageSentDuringTheSequenceRunsAfterTheResume(t *testing.T) {
	t.Run("while the turn stops", func(t *testing.T) {
		w := firedOn(t)
		w.send("m-new", marotte.SendPlain, "")
		if got, want := w.order(), "m-a:stopping compact-1:pending m-resume:pending m-new:pending m-q1:paused"; got != want {
			t.Errorf("list = %q, want %q", got, want)
		}
	})
	t.Run("while the compaction runs", func(t *testing.T) {
		w := firedOn(t)
		w.closed("m-a", listClose{})
		w.l.compactionStart("compact-1", w.facts())
		w.send("m-new", marotte.SendPlain, "")
		if got, want := w.order(), "compact-1:running m-resume:pending m-new:pending m-q1:paused"; got != want {
			t.Errorf("list = %q, want %q", got, want)
		}
		w.l.compactionEnded("compact-1", true, nil)
		w.advance()
		w.running("m-resume")
		w.closed("m-resume", listClose{clean: true})
		if got, want := w.order(), "m-new:starting m-q1:pending"; got != want {
			t.Errorf("list after the resume = %q, want %q", got, want)
		}
	})
}

// A press of Compact during the sequence joins the failsafe's compaction.
func TestFailsafe_ACompactPressJoinsTheFailsafeCompaction(t *testing.T) {
	w := firedOn(t)
	c, joined := w.l.requestCompaction(marotte.RunFromRequest, "t-m-a")
	if !joined || c.id != "compact-1" || c.origin != marotte.RunFromFailsafe {
		t.Errorf("press = (%s %s, joined %v), want the failsafe's compact-1 joined", c.id, c.origin, joined)
	}
	if got, want := w.order(), "m-a:stopping compact-1:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// Steers the stopped turn left unread were written for the task it ran: they are sent after the compaction and
// before the resume.
func TestFailsafe_UnreadSteersGoAfterTheCompactionAndBeforeTheResume(t *testing.T) {
	w := firedOn(t)
	_, e := w.l.find("m-a")
	w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
	w.l.endsChanged(1, 0, []string{"s-1"}, false, true, w.facts())
	w.l.closeEndsResolved("m-a", false, true, w.facts())
	if got, want := w.order(), "m-a:closing compact-1:pending m-unread:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// A turn whose reply finished before the cancel landed has nothing to continue: the compaction still runs first,
// as the point's again (no stopped task to speak of), with no resume, and the rows go after it as a clean end lets
// them.
func TestFailsafe_ATurnThatFinishedBeforeTheCancelGetsNoResume(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{clean: true, completed: true})
	if got, want := w.order(), "compact-1:starting m-q1:pending"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
	if got := entryOrigin(w, "compact-1"); got != marotte.RunFromThreshold {
		t.Errorf("compaction origin = %q, want %q back", got, marotte.RunFromThreshold)
	}
}

// A close graded clean only because nothing watched it end (KAS's idle report after a lost turn_end) is no finished
// reply: the stopped turn is still continued and the rows still wait for the continued turn.
func TestFailsafe_AnUnattendedCloseOfTheStoppedTurnKeepsTheResume(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{clean: true})
	if got, want := w.order(), "compact-1:starting m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// A failsafe compaction that fails while a /compact waits on it still toasts, since the toast is the only word that
// the stopped turn was not continued, and the resume goes with it.
func TestFailsafe_AFailedCompactionAJoinedWaiterWaitsOnStillToasts(t *testing.T) {
	w := firedOn(t)
	c, joined := w.l.requestCompaction(marotte.RunFromRequest, "t-m-a")
	if !joined {
		t.Fatalf("Setup: the press did not join %s", c.id)
	}
	c.waiter = true
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.l.takeEffects()
	w.l.compactionEnded("compact-1", false, errors.New("the summary failed"))
	fx := w.l.takeEffects()
	w.advance()
	if len(fx.toast) != 1 {
		t.Errorf("toasts = %d, want 1 with a waiter joined", len(fx.toast))
	}
	if got := w.order(); got != "m-q1:paused" {
		t.Errorf("list = %q, want only m-q1 paused", got)
	}
}

// A failsafe stop whose session/cancel was not delivered leaves the turn running: the resume goes and the compaction
// it took over is the point's again, for the turn's end.
func TestFailsafe_AnUndeliveredCancelWithdrawsTheResume(t *testing.T) {
	w := newTableWorld()
	w.send("m-a", marotte.SendPlain, "")
	w.running("m-a")
	w.send("m-q1", marotte.SendPlain, marotte.InterruptQueue)
	w.l.requestCompaction(marotte.RunFromThreshold, "t-m-a")
	if plan := w.fire("m-a", 1); plan.arm != stopRunning {
		t.Fatalf("fire = %+v, want m-a stopped", plan)
	}
	w.l.stopUndone(1, func(string) bool { return true })
	if got, want := w.order(), "m-a:running compact-1:pending m-q1:pending"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
	if got := entryOrigin(w, "compact-1"); got != marotte.RunFromThreshold {
		t.Errorf("compaction origin = %q, want %q back", got, marotte.RunFromThreshold)
	}
}

// The reader's Cancel during the sequence: drawn against the turn the failsafe is stopping, it stops nothing more,
// the compaction still runs and the task is not continued, and the rows stay paused even when the reply finished
// before the cancel landed, as after any stop of the reader's; during the resume it stops the resume, whose rows
// then wait for the next clean end.
func TestFailsafe_AReaderCancelDuringTheSequence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close listClose
	}{
		{name: "drawn against the turn the failsafe stops"},
		{name: "drawn against the turn the failsafe stops, whose reply then finishes", close: listClose{clean: true, completed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := firedOn(t)
			if plan := w.l.readerStop("m-a", 2); plan.arm != stopNothing {
				t.Errorf("Cancel = %+v, want nothing more stopped", plan)
			}
			if got, want := w.order(), "m-a:stopping compact-1:pending m-q1:paused"; got != want {
				t.Errorf("list = %q, want %q", got, want)
			}
			w.closed("m-a", tc.close)
			w.l.compactionStart("compact-1", w.facts())
			w.l.compactionEnded("compact-1", true, nil)
			w.advance()
			if got := w.order(); got != "m-q1:paused" {
				t.Errorf("list after the compaction = %q, want only m-q1 paused", got)
			}
		})
	}
	t.Run("during the resume", func(t *testing.T) {
		w := firedOn(t)
		w.compacted(t)
		w.running("m-resume")
		if plan := w.l.readerStop("", 2); plan.arm != stopRunning || plan.entry.id != "m-resume" {
			t.Fatalf("Cancel = %+v, want the resume stopped", plan)
		}
		w.closed("m-resume", listClose{})
		if got := w.order(); got != "m-q1:paused" {
			t.Errorf("list = %q, want m-q1 paused", got)
		}
	})
}

// toRest runs the list until nothing is live: a taken turn opens, a live turn closes cancelled, a compaction is
// written and accepted, a close tail ends and its reservation is given back. It returns the kinds taken on the way.
func (w *tableWorld) toRest(t *testing.T) (taken []string) {
	t.Helper()
	w.held = false
	note := func() {
		for _, e := range w.l.takeEffects().launch {
			taken = append(taken, string(e.kind))
		}
	}
	for range 30 {
		note()
		w.advance()
		note()
		if w.l.liveLen() == 0 {
			return taken
		}
		e := w.l.entries[0]
		switch {
		case e.kind == marotte.RunKindCompaction && e.state == marotte.RunStarting:
			w.l.compactionStart(e.id, w.facts())
		case e.kind == marotte.RunKindCompaction:
			w.l.compactionEnded(e.id, true, nil)
		case e.state == marotte.RunStarting && e.turnID == "":
			w.running(e.id)
		case e.state == marotte.RunClosing:
			w.l.tailEnded(e.id, false)
			w.held = false
			w.l.reservationReleased(e.res.Token)
		default:
			w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
		}
	}
	t.Fatalf("the list never came to rest: %s", w.order())
	return nil
}

// Any stop the reader makes after the failsafe's stop and before the resume starts asks for the work to stop,
// whatever runs then: the resume is withdrawn and never starts, the compaction still runs, and the rows wait for the
// reader. Each window is a turn that can run there (the stopped turn, kiro-cli's re-prompt after the cancel, the
// stopped turn's unread steers, kiro-cli's turn under the compaction) or none; each stop is one the reader can make.
func TestFailsafe_AReaderStopBeforeTheResumeStartsWithdrawsIt(t *testing.T) {
	windows := []struct {
		setup func(t *testing.T, w *tableWorld)
		name  string
		// drawn is the entry a Cancel the page drew in that window names.
		drawn string
	}{
		{name: "the stopped turn stopping", drawn: "m-a", setup: func(*testing.T, *tableWorld) {}},
		{name: "the stopped turn closing", drawn: "m-a", setup: func(_ *testing.T, w *tableWorld) {
			_, e := w.l.find("m-a")
			w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
		}},
		{name: "kiro-cli's re-prompt ahead of the compaction", drawn: "t-w", setup: func(_ *testing.T, w *tableWorld) {
			_, e := w.l.find("m-a")
			w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
			w.wireOpen("t-w", true)
		}},
		{name: "the compaction running", drawn: "m-a", setup: func(_ *testing.T, w *tableWorld) {
			w.closed("m-a", listClose{})
			w.l.compactionStart("compact-1", w.facts())
		}},
		{name: "kiro-cli's turn under the compaction", drawn: "t-w", setup: func(_ *testing.T, w *tableWorld) {
			w.closed("m-a", listClose{})
			w.l.compactionStart("compact-1", w.facts())
			w.wireOpen("t-w", true)
		}},
		{name: "the stopped turn's unread steers", drawn: "m-unread", setup: func(t *testing.T, w *tableWorld) {
			_, e := w.l.find("m-a")
			token := e.res.Token
			w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
			w.l.endsChanged(1, 0, []string{"s-1"}, false, true, w.facts())
			w.l.closeEndsResolved("m-a", false, true, w.facts())
			w.l.tailEnded("m-a", false)
			w.held = false
			w.l.reservationReleased(token)
			w.advance()
			w.l.compactionStart("compact-1", w.facts())
			w.l.compactionEnded("compact-1", true, nil)
			w.advance()
			w.running("m-unread")
			if got, want := w.order(), "m-unread:running m-resume:pending m-q1:paused"; got != want {
				t.Fatalf("Setup: list = %q, want %q", got, want)
			}
		}},
		{name: "nothing live, the slot held", drawn: "m-a", setup: func(t *testing.T, w *tableWorld) {
			w.closed("m-a", listClose{})
			w.l.compactionStart("compact-1", w.facts())
			w.held = true
			w.l.compactionEnded("compact-1", true, nil)
			w.advance()
			if got, want := w.order(), "m-resume:pending m-q1:paused"; got != want {
				t.Fatalf("Setup: list = %q, want %q", got, want)
			}
		}},
	}
	stops := []struct {
		stop func(w *tableWorld, drawn string)
		name string
		// rest is the list once nothing runs: the reader's rows paused, or gone after the row a press sent.
		rest string
	}{
		{name: "Cancel drawn against the page", rest: "m-q1:paused", stop: func(w *tableWorld, drawn string) {
			w.l.readerStop(drawn, 2)
		}},
		{name: "Cancel of what the dock names", rest: "m-q1:paused", stop: func(w *tableWorld, _ string) {
			w.l.readerStop("", 2)
		}},
		{name: "Cancel drawn against an earlier turn", rest: "m-q1:paused", stop: func(w *tableWorld, _ string) {
			w.l.readerStop("m-gone", 2)
		}},
		{name: "a send-now press", rest: "", stop: func(w *tableWorld, _ string) {
			if h, err := w.l.sendNow("m-q1", "", false); err == nil && h != nil {
				w.l.readerStop(h.id, 2)
			}
		}},
	}
	for _, win := range windows {
		for _, s := range stops {
			t.Run(win.name+", "+s.name, func(t *testing.T) {
				w := firedOn(t)
				win.setup(t, w)
				if w.l.resume() == nil {
					t.Fatalf("Setup: no resume waiting; list %s", w.order())
				}
				s.stop(w, win.drawn)
				if r := w.l.resume(); r != nil {
					t.Errorf("resume %s still waits after the reader's stop; list %s", r.id, w.order())
				}
				taken := w.toRest(t)
				if slices.Contains(taken, string(marotte.RunKindResume)) {
					t.Errorf("taken = %v, want no resume after the reader's stop", taken)
				}
				if got := w.order(); got != s.rest {
					t.Errorf("list at rest = %q, want %q", got, s.rest)
				}
			})
		}
	}
}

// Closing the chat ends its list's life: an ended list takes nothing, so a waiting resume never starts, and the list
// the next life starts from keeps only the reader's rows.
func TestFailsafe_ClosingTheChatDropsAWaitingResume(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.held = true
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
	if got, want := w.order(), "m-resume:pending m-q1:paused"; got != want {
		t.Fatalf("Setup: list = %q, want %q", got, want)
	}

	next := w.l.endLife()
	w.held = false
	w.l.takeEffects()
	w.advance()

	if fx := w.l.takeEffects(); len(fx.launch) != 0 {
		t.Errorf("an ended list took %v", fx.launch)
	}
	ids := make([]string, 0, len(next))
	for _, e := range next {
		ids = append(ids, e.ID)
	}
	if !slices.Equal(ids, []string{"m-q1"}) {
		t.Errorf("next life's list = %v, want only m-q1", ids)
	}
}

// A wire turn kiro-cli opens after the cancel (its re-prompt of what the cancel cleared) runs first; a reading at
// 95% in it stops it too and joins the listed compaction and resume.
func TestFailsafe_AWireTurnOpenedAfterTheCancelRunsFirst(t *testing.T) {
	w := firedOn(t)
	_, e := w.l.find("m-a")
	w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
	w.wireOpen("t-w", true)
	if got, want := w.order(), "t-w:running compact-1:pending m-resume:pending m-q1:paused"; got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
	if plan := w.l.failsafe(failsafeFacts{turnID: "t-w", resumeID: "m-resume-2", token: 2}); plan.arm != stopRunning {
		t.Errorf("fire in the wire turn = %+v, want it stopped", plan)
	}
	if got, want := w.order(), "t-w:stopping compact-1:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// The reader can take the resume off the dock; the compaction still runs.
func TestFailsafe_RemovingTheResumeKeepsTheCompaction(t *testing.T) {
	w := firedOn(t)
	if err := w.l.unqueue("m-resume"); err != nil {
		t.Errorf("unqueue the resume = %v", err)
	}
	if got, want := w.order(), "m-a:stopping compact-1:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// A rewind's revert taken while the resume waits removes the work the resume would continue, so the resume goes.
func TestFailsafe_ARewindDropsTheResume(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.held = true
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
	if got, want := w.order(), "m-resume:pending m-q1:paused"; got != want {
		t.Fatalf("Setup: list with the slot held = %q, want %q", got, want)
	}
	w.held = false
	revert := &runEntry{id: "rewind-1", kind: marotte.RunKindRevert, origin: marotte.RunFromRewind}
	if a := w.l.takeIdle(revert, w.facts(), 0); a != idleGranted {
		t.Fatalf("revert take = %v, want granted", a)
	}
	if got, want := w.order(), "rewind-1:running m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// A close graded clean only because nothing watched it end is clean for the rows behind it, but only a completed
// outcome says a stopped turn's reply finished before its cancel landed.
func TestListCloseOf_CompletedIsTheOutcomesOwnGrade(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, tc := range []struct {
		name             string
		outcome          marotte.TurnOutcome
		closer           turnCloser
		clean, completed bool
	}{
		{name: "a completed reply", outcome: marotte.TurnOutcomeCompleted, closer: closerPromptResponse, clean: true, completed: true},
		{name: "KAS's idle report", outcome: marotte.TurnOutcomeUnknown, closer: closerKASIdle, clean: true},
		{name: "a cancelled turn", outcome: marotte.TurnOutcomeCancelled, closer: closerPromptResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := &activeTurn{Chat: "c1", ID: "t-1", lc: h.coord.turns.testLifecycle("c1")}
			cf := h.coord.closeFactsOf(turn, tc.closer, tc.outcome)
			c := h.coord.listCloseOf(t.Context(), turn, tc.closer, &cf, marotte.TurnResult{}, false)
			if c.clean != tc.clean || c.completed != tc.completed {
				t.Errorf("close = clean %v completed %v, want clean %v completed %v", c.clean, c.completed, tc.clean, tc.completed)
			}
		})
	}
}

// resumeRunning is chat m-a stopped by the failsafe under token 1 and compacted, with its resume's turn running and
// nothing queued.
func resumeRunning(t *testing.T) *tableWorld {
	t.Helper()
	w := newTableWorld()
	w.send("m-a", marotte.SendPlain, "")
	w.running("m-a")
	if plan := w.fire("m-a", 1); plan.arm != stopRunning {
		t.Fatalf("Setup: fire = %+v, want m-a stopped", plan)
	}
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
	w.running("m-resume")
	w.l.usageUnderThePoint()
	if got := w.order(); got != "m-resume:running" {
		t.Fatalf("Setup: list = %q, want the resume running alone", got)
	}
	w.l.takeEffects()
	return w
}

// marotte continues a stopped task on its own once per turn the reader sent: a resume that fills the context again
// is stopped and compacted (stopping at 95% still beats running out), but nothing continues it, the rows stay
// paused and the reader is told to send a message.
func TestFailsafe_AResumeThatFillsTheContextAgainIsStoppedAndCompactedButNotResumed(t *testing.T) {
	w := firedOn(t)
	w.compacted(t)
	w.running("m-resume")
	w.l.usageUnderThePoint()
	w.l.takeEffects()

	if plan := w.l.failsafe(failsafeFacts{turnID: "t-m-resume", resumeID: "m-resume-2", token: 2}); plan.arm != stopRunning {
		t.Fatalf("fire in the resume = %+v, want it stopped", plan)
	}
	if got, want := w.order(), "m-resume:stopping compact-2:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
	if fx := w.l.takeEffects(); len(fx.notices) != 0 {
		t.Errorf("notices at the fire = %q, want none before the stop holds", fx.notices)
	}
	w.closed("m-resume", listClose{})
	if fx := w.l.takeEffects(); !slices.Equal(fx.notices, []string{resumeLimitNotice}) {
		t.Errorf("notices at the stopped close = %q, want %q", fx.notices, resumeLimitNotice)
	}
	w.l.compactionStart("compact-2", w.facts())
	w.l.compactionEnded("compact-2", true, nil)
	w.advance()
	if got := w.order(); got != "m-q1:paused" {
		t.Errorf("list after the compaction = %q, want only m-q1 paused", got)
	}
}

// The limit's notice says marotte stopped the turn, so a stop that did not hold says nothing: a reply that finished
// before the cancel landed lets the rows run, a cancel not delivered leaves the turn running, and a stop of the
// reader's own makes the stop theirs.
func TestFailsafe_TheResumeLimitsNoticeWaitsForTheStopToHold(t *testing.T) {
	for _, tc := range []struct {
		after func(w *tableWorld)
		name  string
		order string
	}{
		{name: "the reply finished first", after: func(w *tableWorld) {
			w.closed("m-resume", listClose{clean: true, completed: true})
		}},
		{name: "the cancel was not delivered", after: func(w *tableWorld) {
			w.l.stopUndone(2, func(string) bool { return true })
			w.closed("m-resume", listClose{})
		}},
		{name: "the reader stopped it too", after: func(w *tableWorld) {
			w.l.readerStop("m-resume", 3)
			w.closed("m-resume", listClose{})
		}},
		{name: "the reader pressed send-now", order: "compact-2:starting m-q1:pending", after: func(w *tableWorld) {
			w.send("m-q1", marotte.SendPlain, marotte.InterruptQueue)
			if _, err := w.l.sendNow("m-q1", "", false); err != nil {
				t.Fatalf("Setup: send-now = %v", err)
			}
			w.closed("m-resume", listClose{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := resumeRunning(t)
			if plan := w.l.failsafe(failsafeFacts{turnID: "t-m-resume", resumeID: "m-resume-2", token: 2}); plan.arm != stopRunning {
				t.Fatalf("Setup: fire in the resume = %+v, want it stopped", plan)
			}
			w.l.takeEffects()
			tc.after(w)
			if fx := w.l.takeEffects(); len(fx.notices) != 0 {
				t.Errorf("notices = %q, want none", fx.notices)
			}
			if got := w.order(); tc.order != "" && got != tc.order {
				t.Errorf("list = %q, want %q", got, tc.order)
			}
		})
	}
}

// A turn the reader sent (a message, or the steers the reader sent that the turn left unread) allows the next
// resume again; kiro-cli's own turn does not.
func TestFailsafe_ATurnTheReaderSentAllowsTheNextResume(t *testing.T) {
	for _, tc := range []struct {
		turn    func(w *tableWorld) (entry string)
		name    string
		resumes bool
	}{
		{name: "a message the reader sent", resumes: true, turn: func(w *tableWorld) string {
			w.closed("m-resume", listClose{clean: true, completed: true})
			w.send("m-b", marotte.SendPlain, "")
			w.running("m-b")
			return "m-b"
		}},
		{name: "the steers the resume left unread", resumes: true, turn: func(w *tableWorld) string {
			_, r := w.l.find("m-resume")
			token := r.res.Token
			w.l.turnClosed(listClose{turnID: r.turnID, seq: w.nextSeq, clean: true, completed: true}, w.facts())
			w.l.endsChanged(1, 0, []string{"s-1"}, false, true, w.facts())
			w.l.closeEndsResolved("m-resume", false, true, w.facts())
			w.l.tailEnded("m-resume", false)
			w.held = false
			w.l.reservationReleased(token)
			w.advance()
			w.running("m-unread")
			return "m-unread"
		}},
		{name: "kiro-cli's own wire turn", turn: func(w *tableWorld) string {
			w.closed("m-resume", listClose{clean: true, completed: true})
			w.wireOpen("t-w", true)
			return "t-w"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := resumeRunning(t)
			id := tc.turn(w)
			_, e := w.l.find(id)
			if e == nil || e.state != marotte.RunRunning || w.l.liveLen() != 1 {
				t.Fatalf("Setup: list = %q, want %s running alone", w.order(), id)
			}
			w.l.usageUnderThePoint()
			w.l.takeEffects()
			if plan := w.l.failsafe(failsafeFacts{turnID: e.turnID, resumeID: "m-resume-2", token: 2}); plan.arm != stopRunning {
				t.Fatalf("fire in %s = %+v, want it stopped", id, plan)
			}
			_, r := w.l.find("m-resume-2")
			if got := r != nil; got != tc.resumes {
				t.Errorf("resume listed = %v, want %v; list %q", got, tc.resumes, w.order())
			}
			w.l.turnClosed(listClose{turnID: e.turnID, seq: w.nextSeq}, w.facts())
			fx := w.l.takeEffects()
			if got := len(fx.notices) == 1; got == tc.resumes {
				t.Errorf("notices = %q with resume %v, want the limit notice only when no resume is listed", fx.notices, tc.resumes)
			}
		})
	}
}

// A turn that runs ahead of a waiting resume (kiro-cli's own, or the stopped turn's unread steers) and reaches 95%
// lists its compaction ahead of that resume, which waits on behind it: one resume, never two. Undoing the new stop
// leaves the resume, which belongs to the first stop.
func TestFailsafe_AFireAheadOfAWaitingResumeKeepsOneResume(t *testing.T) {
	w := firedOn(t)
	w.closed("m-a", listClose{})
	w.l.compactionStart("compact-1", w.facts())
	w.held = true
	w.l.compactionEnded("compact-1", true, nil)
	w.advance()
	w.wireOpen("t-w", true)
	if got, want := w.order(), "t-w:running m-resume:pending m-q1:paused"; got != want {
		t.Fatalf("Setup: list = %q, want %q", got, want)
	}
	w.l.usageUnderThePoint()

	if plan := w.l.failsafe(failsafeFacts{turnID: "t-w", resumeID: "m-resume-2", token: 2}); plan.arm != stopRunning {
		t.Fatalf("fire in the wire turn = %+v, want it stopped", plan)
	}
	if got, want := w.order(), "t-w:stopping compact-2:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
	w.l.stopUndone(2, func(string) bool { return true })
	if got, want := w.order(), "t-w:running compact-2:pending m-resume:pending m-q1:paused"; got != want {
		t.Errorf("list after the undo = %q, want %q", got, want)
	}
	if got := entryOrigin(w, "compact-2"); got != marotte.RunFromThreshold {
		t.Errorf("compaction origin after the undo = %q, want %q", got, marotte.RunFromThreshold)
	}
}

// A resume whose launch is refused continues nothing: it goes from the dock, the rows stay paused and the reader is
// told, unless a stop withdrew it or the chat's life ended.
func TestFailsafe_ARefusedResumeLaunchTellsTheReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    []string
		refusal launchRefusal
		ended   bool
	}{
		{name: "refused", refusal: launchClosed, want: []string{resumeRefusedNotice}},
		{name: "withdrawn by a stop", refusal: launchWithdrawn},
		{name: "on an ended life", refusal: launchClosed, ended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := firedOn(t)
			w.compacted(t)
			_, r := w.l.find("m-resume")
			if r == nil || r.state != marotte.RunStarting {
				t.Fatalf("Setup: list = %q, want the resume starting", w.order())
			}
			if tc.ended {
				w.l.endLife()
			}
			w.l.takeEffects()
			w.l.launched("m-resume", r.res.Token, "", tc.refusal, errors.New("the bridge went away"))
			fx := w.l.takeEffects()
			if got := w.order(); got != "m-q1:paused" {
				t.Errorf("list = %q, want only m-q1 paused", got)
			}
			if !slices.Equal(fx.notices, tc.want) {
				t.Errorf("notices = %q, want %q", fx.notices, tc.want)
			}
		})
	}
}
