package ruleengine

import (
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// launchFailsFirst fails the first delivery's turn open and opens every later one's at once.
func launchFailsFirst(n int) (placement, error) {
	if n == 0 {
		return launchFails, nil
	}
	return opensTurn, nil
}

// A prompt whose turn failed to open after Notify answered is a refused delivery: its paused row is taken back, the
// event waits out the ladder's first step, then goes again and counts once.
func TestEngine_APromptWhoseTurnFailedToOpenIsSentAgainAfterTheRetryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.notes.place = launchFailsFirst
		h.scan()
		h.start()
		event := dueID("t1", h.at(5*time.Minute))

		h.advance(5*time.Minute + 30*time.Second)
		h.wantSent("before the retry wait", 1)
		if got := h.notes.unqueuedRows(); !slices.Equal(got, []string{"m-1"}) {
			t.Errorf("unqueued %v, want the failed row m-1 taken back", got)
		}
		if rows := h.runRows("b1"); len(rows) != 0 {
			t.Errorf("run rows %+v, want none left", rows)
		}
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 1 {
			t.Fatalf("queue %v, want the event waiting", eventIDs(r.Status.Queue))
		}
		if q := r.Status.Queue[0]; q.EventID != event || q.Stage != rule.StageFired || q.Queued || q.MessageID != "" || q.Tries != 1 {
			t.Errorf("event %+v, want %s fired, not queued, 1 try", q, event)
		}
		if len(r.Status.Sent) != 0 || r.Status.LastFailure != nil {
			t.Errorf("sent %v last failure %+v, want nothing counted and no failure yet", r.Status.Sent, r.Status.LastFailure)
		}

		h.advance(time.Minute)
		got := h.wantSent("after the retry wait", 2)
		if eventIDOf(got[1]) != event || got[1].at.Sub(got[0].at) < time.Minute {
			t.Errorf("second send %q at +%v, want %s again after 1 min", eventIDOf(got[1]), got[1].at.Sub(got[0].at), event)
		}
		r = h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
			t.Errorf("queue %v sent %v dropped %d, want the event delivered and counted once", eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	})
}

// A prompt whose turn fails to open on every try is dropped on the tenth with the rule's failure, and no row of it
// stays on the run list.
func TestEngine_APromptWhoseTurnNeverOpensFailsAfterTenTries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 600, "")), h.at(0)))
		h.notes.place = func(int) (placement, error) { return launchFails, nil }
		h.scan()
		h.start()

		h.advance(600 * time.Minute)
		h.wantSent("the first try", 1)
		h.advance(167 * time.Minute)
		h.wantSent("before the tenth try", 9)
		h.advance(time.Minute + 30*time.Second)
		h.wantSent("the tenth try", 10)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 0 {
			t.Errorf("queue %v sent %v, want the event dropped and nothing counted", eventIDs(r.Status.Queue), r.Status.Sent)
		}
		want := "Could not deliver after 10 tries: the Bot's reply did not start"
		if f := r.Status.LastFailure; f == nil || f.Reason != want || f.EventID != dueID("t1", h.at(600*time.Minute)) {
			t.Errorf("last failure %+v, want %q for the event", f, want)
		}
		if rows := h.runRows("b1"); len(rows) != 0 {
			t.Errorf("run rows %+v, want none left", rows)
		}
		if n := h.logs.lines(slog.LevelWarn, "rule delivery retry"); n != 9 {
			t.Errorf("%d retry lines, want 9", n)
		}
	})
}

// A queued prompt whose row a stop paused is still in the chat: the Bot is not free, so no other prompt goes until
// the row's turn opens.
func TestEngine_APausedQueuedRowKeepsTheBotBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.seedBot("b1", armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0)))
		h.notes.place = queuedOnce
		h.scan()
		h.start()
		h.advance(5 * time.Minute)
		h.wantSent("the queued send", 1)
		h.mutate("b1", func(c *marotte.Chat) {
			for i := range c.Run {
				c.Run[i].State = marotte.RunPaused
			}
		})

		h.advance(10 * time.Minute)
		h.wantSent("while the row is paused", 1)

		h.takeQueuedRow("b1", true)
		h.endTurn("b1")
		h.advance(2 * time.Minute)
		got := h.wantSent("after the row's turn", 2)
		if eventIDOf(got[1]) != dueID("t1", h.at(15*time.Minute)) {
			t.Errorf("then %q, want the 15 min event", eventIDOf(got[1]))
		}
	})
}
