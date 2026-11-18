package ruleengine

import (
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/testsupport"
)

const writeFailingSeries = "marotte_rule_engine_write_failing"

// A record write that fails stops evaluation and delivery loudly, once, and the first good write resumes them; a
// 3-hour stop under "every 15 min" is one late evaluation on resume, anchored there, never a pass per missed slot.
func TestEngine_AWriteFaultStopsTheLoopUntilAWriteLands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 15, "")), h.at(0)))
		h.scan()
		h.start()
		h.advance(10 * time.Minute)
		h.fs.failing.Store(true)
		h.advance(5 * time.Minute)
		if got := h.logs.lines(slog.LevelError, "rule engine could not write a Bot's record"); got != 1 {
			t.Errorf("%d ERROR lines at the fault, want 1", got)
		}
		if got := testsupport.MetricValue(t, writeFailingSeries); got != 1 {
			t.Errorf("%s = %v during the fault, want 1", writeFailingSeries, got)
		}

		h.advance(3*time.Hour - 30*time.Second)
		h.wantSent("during the fault", 0)
		if got := h.logs.lines(slog.LevelError, "rule engine could not write a Bot's record"); got != 1 {
			t.Errorf("%d ERROR lines after 3 hours of retries, want 1", got)
		}
		r := h.rule("b1", "r1")
		if r.Status.Triggers["t1"].LastAt != 0 || len(r.Status.Queue) != 0 || r.Status.LastFire != nil {
			t.Errorf("status %+v moved during the fault, want nothing written", r.Status)
		}

		h.fs.failing.Store(false)
		h.advance(30 * time.Second)
		resumed := h.at(3*time.Hour + 15*time.Minute)
		got := h.wantSent("on resume", 1)
		if !got[0].at.Equal(time.UnixMilli(resumed)) {
			t.Errorf("delivered at %v, want the first retry after the fault, %v", got[0].at, time.UnixMilli(resumed))
		}
		block := eventBlock(t, got[0].text)
		if want := time.UnixMilli(h.at(15 * time.Minute)).Format(time.RFC3339); block["due_at"] != want || block["late_by"] != "3h0m0s" {
			t.Errorf("due_at %v late_by %v, want %s and 3h0m0s", block["due_at"], block["late_by"], want)
		}
		if lastAt := h.rule("b1", "r1").Status.Triggers["t1"].LastAt; lastAt != resumed {
			t.Errorf("last_at %d, want the resumed evaluation's %d", lastAt, resumed)
		}
		if got := h.logs.lines(slog.LevelInfo, "rule engine resumed"); got != 1 {
			t.Errorf("%d INFO resume lines, want 1", got)
		}
		if got := testsupport.MetricValue(t, writeFailingSeries); got != 0 {
			t.Errorf("%s = %v after the resume, want 0", writeFailingSeries, got)
		}

		h.endTurn("b1")
		h.advance(15*time.Minute - time.Second)
		h.wantSent("inside the next interval", 1)
		h.advance(time.Second)
		h.wantSent("15 min after the resume", 2)
	})
}

// A time trigger keeps one waiting event through a long turn, and a Run now press beside it keeps one more: the turn's
// end delivers the press, which fired first, and then the newest due time, each once.
func TestEngine_ATimeTriggerNeverStacksBesideRunNow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 15, "")), h.at(0)))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		h.advance(30*time.Minute + 30*time.Second)
		for _, op := range []string{"op-1", "op-2"} {
			if ans := h.runNow(op); ans.Outcome != command.RunNowQueued {
				t.Errorf("%s answered %+v, want queued", op, ans)
			}
		}
		h.advance(31*time.Minute + 30*time.Second)
		r := h.rule("b1", "r1")
		got, want := eventIDs(r.Status.Queue), []string{"manual:op-1", dueID("t1", h.at(60*time.Minute))}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("queue %v, want the first press and the 60 min slot", got)
		}
		if r.Status.Dropped != 3 || r.Status.Triggers["t1"].LastAt != h.at(60*time.Minute) {
			t.Errorf("dropped %d last_at %d, want 3 slots replaced and the last evaluation at 60 min",
				r.Status.Dropped, r.Status.Triggers["t1"].LastAt)
		}
		h.wantSent("during the turn", 0)

		h.endTurn("b1")
		h.endTurn("b1")
		h.wantSent("inside the rule's 1-minute gap", 1)
		h.advance(time.Minute)
		sent := h.wantSent("after the turn", 2)
		if eventIDOf(sent[0]) != "manual:op-1" || eventIDOf(sent[1]) != dueID("t1", h.at(60*time.Minute)) {
			t.Errorf("delivered %q then %q, want the press then the 60 min slot", eventIDOf(sent[0]), eventIDOf(sent[1]))
		}
		if !sent[1].at.Equal(sent[0].at.Add(rule.MinGap)) {
			t.Errorf("second prompt at %v, want a gap after the first at %v", sent[1].at, sent[0].at)
		}
	})
}

// A Run now press while another waits fires nothing, logs no fire, and answers queued each time it is retried.
func TestEngine_APressBesideAWaitingOneFiresNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", weekly(h))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		for _, op := range []string{"op-1", "op-2", "op-2"} {
			if ans := h.runNow(op); ans.Outcome != command.RunNowQueued {
				t.Errorf("%s answered %+v, want queued", op, ans)
			}
		}
		if q := h.rule("b1", "r1").Status.Queue; !slices.Equal(eventIDs(q), []string{"manual:op-1"}) {
			t.Errorf("queue %v, want the first press alone", eventIDs(q))
		}
		if got := h.logs.lines(slog.LevelInfo, "rule fired"); got != 1 {
			t.Errorf("%d fired lines, want 1", got)
		}
	})
}

// A replayed Run now press answers what its first press answered while its event still waits for delivery.
func TestEngine_AReplayedPressAnswersItsFirstAnswer(t *testing.T) {
	for _, c := range []struct {
		name string
		live bool
		want command.RunNowOutcome
	}{
		{name: "fired on a free Bot", want: command.RunNowFired},
		{name: "queued on a working Bot", live: true, want: command.RunNowQueued},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", weekly(h))
				h.turns.setLive("b1", c.live)
				h.refused.Store(true)
				h.scan()
				h.start()
				for _, label := range []string{"the press", "its replay"} {
					if ans := h.runNow("op-1"); ans.Outcome != c.want {
						t.Errorf("%s answered %+v, want %s", label, ans, c.want)
					}
				}
				h.wantSent("while the runtime refuses", 0)
			})
		})
	}
}

// A row the settle took back off the run list is never lost to a write fault at the settle's own write: once writes
// land, the event goes again once and nothing is dropped.
func TestEngine_ARowTakenBackSurvivesAFaultAtTheSettlesWrite(t *testing.T) {
	failOnce := func(h *harness) func() {
		var faulted atomic.Bool
		return func() {
			if !faulted.Swap(true) {
				h.fs.failing.Store(true)
			}
		}
	}
	wantDeliveredOnce := func(t *testing.T, h *harness) {
		t.Helper()
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
			t.Errorf("queue %v sent %v dropped %d, want the event delivered once and nothing dropped",
				eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	}
	t.Run("the boot's held row", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			seedQueued(h)
			h.restart(func() { h.notes.afterUnqueue = failOnce(h) })
			if got := h.notes.unqueuedRows(); !slices.Equal(got, []string{"m-1"}) {
				t.Errorf("unqueued %v, want the held row m-1", got)
			}
			h.wantSent("during the fault", 0)
			h.advance(30 * time.Second)
			h.fs.failing.Store(false)
			h.advance(time.Minute)
			got := h.wantSent("after the fault", 1)
			if eventIDOf(got[0]) != dueID("t1", h.at(5*time.Minute)) {
				t.Errorf("resent %q, want the 5 min event", eventIDOf(got[0]))
			}
			wantDeliveredOnce(t, h)
		})
	})
	t.Run("a failed launch's row", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
			h.notes.place = launchFailsFirst
			h.notes.afterUnqueue = failOnce(h)
			h.scan()
			h.start()
			h.advance(5*time.Minute + 30*time.Second)
			if got := h.notes.unqueuedRows(); !slices.Equal(got, []string{"m-1"}) {
				t.Errorf("unqueued %v, want the failed row m-1", got)
			}
			h.fs.failing.Store(false)
			h.advance(15 * time.Second)
			h.wantSent("inside the retry wait", 1)
			h.advance(time.Minute)
			h.wantSent("after the retry wait", 2)
			wantDeliveredOnce(t, h)
			if n := h.logs.lines(slog.LevelWarn, "rule delivery retry"); n != 1 {
				t.Errorf("%d retry lines, want the relaunch logged once its write landed", n)
			}
		})
	})
}

// The write a send owes is kept through a write fault and lands before the loop runs again: a prompt that queued
// while writes failed is marked queued once they land, so the user's discard of its row drops it, never sends it again.
func TestEngine_AWriteOwedBySendLandsAfterTheFault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.notes.place = queuedOnce
		var faulted atomic.Bool
		h.notes.after = func() {
			if !faulted.Swap(true) {
				h.fs.failing.Store(true)
			}
		}
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		h.wantSent("the queued send", 1)
		if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || q[0].Queued {
			t.Fatalf("queue %+v during the fault, want the fired event with no mark yet", q)
		}

		h.advance(30 * time.Second)
		h.fs.failing.Store(false)
		h.advance(time.Minute)
		if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || !q[0].Queued || q[0].MessageID != "m-1" {
			t.Fatalf("queue %+v after the fault, want the event marked queued as m-1", q)
		}

		h.dropQueuedRow("b1")
		h.advance(time.Minute)
		h.wantSent("after the discard", 1)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 0 || r.Status.Dropped != 1 {
			t.Errorf("queue %v sent %v dropped %d, want it dropped and counted, nothing sent", eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	})
}

// A drain that lands between taking the boot's held row back and the settle's write never loses the row: the write
// lands anyway, and the next process sends the event once and drops nothing.
func TestEngine_AnOwedWriteSurvivesADrainAtItsRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		seedQueued(h)
		var faulted, drained atomic.Bool
		var afterFault atomic.Int64
		h.restart(func() {
			h.notes.afterUnqueue = func() {
				if !faulted.Swap(true) {
					h.fs.failing.Store(true)
				}
			}
			// The first write after the fault is the settle's, which faults; the second is its retry.
			h.fs.beforeMutate = func() {
				if faulted.Load() && afterFault.Add(1) == 2 && !drained.Swap(true) {
					h.fs.failing.Store(false)
					h.cancel()
				}
			}
		})
		h.wantSent("during the fault", 0)
		h.advance(time.Minute)
		if !drained.Load() {
			t.Fatal("the owed write was never retried")
		}
		h.wantSent("before the next process", 0)
		h.restart()
		h.advance(time.Minute)
		h.wantSent("after the next process starts", 1)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
			t.Errorf("queue %v sent %v dropped %d, want the event delivered once and nothing dropped",
				eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	})
}

func TestEngine_ARowTakenBackSurvivesADrainBeforeTheSettlesWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		seedQueued(h)
		h.restart(func() { h.notes.afterUnqueue = func() { h.cancel() } })
		if got := h.notes.unqueuedRows(); !slices.Equal(got, []string{"m-1"}) {
			t.Fatalf("unqueued %v, want the held row m-1", got)
		}
		h.wantSent("before the next process", 0)
		h.restart()
		h.advance(time.Minute)
		got := h.wantSent("after the next process starts", 1)
		if eventIDOf(got[0]) != dueID("t1", h.at(5*time.Minute)) {
			t.Errorf("resent %q, want the 5 min event", eventIDOf(got[0]))
		}
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
			t.Errorf("queue %v sent %v dropped %d, want the event delivered once and nothing dropped",
				eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	})
}
