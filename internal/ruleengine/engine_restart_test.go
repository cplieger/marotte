package ruleengine

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/rule"
)

// A crash between any two of a delivery's writes resumes, in the next process, at the stage the record holds: the
// prompt reaches the Bot exactly once and the rule counts it exactly once.
func TestEngine_ACrashResumesAtTheStageOnDisk(t *testing.T) {
	queuedFirst := func(n int) (placement, error) {
		if n == 0 {
			return waitsInRow, nil
		}
		return opensTurn, nil
	}
	for _, c := range []struct {
		arm func(h *harness)
		// then runs once the first process stopped writing, before it ends.
		then func(h *harness)
		name string
		// first is how many sends the first process made before it died.
		first int
		// resent says the next process delivers the prompt; otherwise it only settles the record.
		resent bool
		// unqueued is the last process's rows the next one takes off the run list before it sends.
		unqueued []string
	}{
		{name: "after the fired write, before the send", resent: true, arm: func(h *harness) { h.fs.dieAfter(1) }},
		{name: "after the send, before its removal", first: 1, arm: func(h *harness) {
			h.notes.after = func() { h.fs.dieAfter(0) }
		}},
		{name: "after a queued send, before its mark", first: 1, resent: true, unqueued: []string{"m-1"}, arm: func(h *harness) {
			h.notes.place = queuedFirst
			h.notes.after = func() { h.fs.dieAfter(0) }
		}},
		{name: "after the queued mark, before its turn opened", first: 1, resent: true, unqueued: []string{"m-1"}, arm: func(h *harness) {
			h.notes.place = queuedFirst
			h.notes.after = func() { h.fs.dieAfter(1) }
		}},
		{name: "after the queued row's turn opened, before its removal", first: 1, arm: func(h *harness) {
			h.notes.place = queuedFirst
			h.notes.after = func() { h.fs.dieAfter(1) }
		}, then: func(h *harness) { h.takeQueuedRow("b1", false) }},
		{name: "after the removal", first: 1, arm: func(*harness) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
				h.scan()
				c.arm(h)
				h.start()
				h.advance(5 * time.Minute)
				h.wantSent("the first process", c.first)
				if c.then != nil {
					c.then(h)
				}
				event := dueID("t1", h.at(5*time.Minute))

				h.advance(20 * time.Second)
				restartedAt := time.Now()
				h.restart()
				want := 0
				if c.resent {
					want = 1
				}
				got := h.wantSent("the next process", want)
				if c.resent {
					if *got[0].from != *ruleCaller(event) {
						t.Errorf("resent with caller %+v, want %+v", got[0].from, ruleCaller(event))
					}
					if eventIDOf(got[0]) != event || !got[0].at.Equal(restartedAt) {
						t.Errorf("resent %q at %v, want %q on the boot pass at %v", eventIDOf(got[0]), got[0].at, event, restartedAt)
					}
				}
				if got := h.notes.unqueuedRows(); !slices.Equal(got, c.unqueued) {
					t.Errorf("unqueued %v, want %v", got, c.unqueued)
				}
				r := h.rule("b1", "r1")
				if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
					t.Errorf("queue %v sent %v dropped %d, want the event gone, counted once, nothing dropped",
						eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
				}
				if rows := h.runRows("b1"); len(rows) != 0 {
					t.Errorf("run list holds %+v, want no rule prompt left waiting", rows)
				}
			})
		})
	}
}

// Boot evaluates an overdue time trigger once before it delivers anything, so a Bot whose prompt waited at shutdown
// and whose next due time passed while marotte was down gets one prompt, for the newer due time.
func TestEngine_BootEvaluatesTheOverdueSlotBeforeDelivering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		h.advance(6 * time.Minute)
		if q := h.rule("b1", "r1").Status.Queue; len(q) != 1 || q[0].EventID != dueID("t1", h.at(5*time.Minute)) {
			t.Fatalf("queue %v at shutdown, want the 5 min event waiting", eventIDs(q))
		}

		h.stop()
		h.advance(11 * time.Minute)
		h.restart(func() { h.turns.setLive("b1", true) })
		h.wantSent("while the Bot works", 0)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 1 || r.Status.Queue[0].EventID != dueID("t1", h.at(10*time.Minute)) ||
			r.Status.Dropped != 1 || r.Status.Triggers["t1"].LastAt != h.at(17*time.Minute) {
			t.Errorf("queue %v dropped %d last_at %d, want only the 10 min slot, the 5 min one replaced (1), the evaluation at %d",
				eventIDs(r.Status.Queue), r.Status.Dropped, r.Status.Triggers["t1"].LastAt, h.at(17*time.Minute))
		}

		h.endTurn("b1")
		got := h.wantSent("at the turn's end", 1)
		if id := eventIDOf(got[0]); id != dueID("t1", h.at(10*time.Minute)) {
			t.Errorf("delivered %q, want the overdue 10 min slot's event", id)
		}
		if block := eventBlock(t, got[0].text); block["late_by"] != "7m0s" {
			t.Errorf("late_by %v, want 7m0s", block["late_by"])
		}
		h.endTurn("b1")
		h.advance(4*time.Minute + 59*time.Second)
		h.wantSent("before the next interval", 1)
		h.advance(time.Second)
		h.wantSent("at the next interval", 2)
	})
}

// A skip of the overdue evaluation replaces nothing: the event that waited is evaluated again before delivery.
func TestEngine_ASkippedOverdueSlotLeavesTheWaitingEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		notWaiting := leafCond(rule.Leaf{Kind: rule.LeafBotWaiting, Not: true})
		h.seedBot("b1", armedRule("r1", specOf("Tidy", notWaiting, everyT("t1", 5, "")), h.at(0)))
		h.turns.setLive("b1", true)
		h.scan()
		h.start()
		h.advance(6 * time.Minute)
		h.stop()
		h.advance(11 * time.Minute)
		h.restart(func() { h.turns.setWaiting("b1", true) })
		h.wantSent("after the restart", 0)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || r.Status.Dropped != 0 || r.Status.LastSkip == nil ||
			r.Status.LastSkip.EventID != dueID("t1", h.at(5*time.Minute)) ||
			r.Status.LastSkip.Reason != "Skipped after waiting: this Bot is waiting on you" {
			t.Errorf("queue %v dropped %d last skip %+v, want the 5 min event kept past the skip and skipped after waiting",
				eventIDs(r.Status.Queue), r.Status.Dropped, r.Status.LastSkip)
		}
	})
}

// A prompt that queued behind a turn and whose row left the run list before the restart, with no turn of it in the
// log, was discarded: the next process drops it and counts nothing as sent.
func TestEngine_BootDropsAQueuedPromptTheUserDiscarded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		seedQueued(h)
		h.stop()
		h.dropQueuedRow("b1")
		h.restart()
		h.advance(time.Minute)
		h.wantSent("the next process", 0)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 0 || r.Status.Dropped != 1 {
			t.Errorf("queue %v sent %v dropped %d, want it dropped and counted, nothing sent",
				eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	})
}
