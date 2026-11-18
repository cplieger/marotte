package ruleengine

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// queuedOnce places the first delivery in a waiting row, as the run order does when a turn opened between the free
// check and the send, and opens every later one's turn at once.
func queuedOnce(n int) (placement, error) {
	if n == 0 {
		return waitsInRow, nil
	}
	return opensTurn, nil
}

// seedQueued runs a Bot whose rule every 5 min last failed, and lets its first prompt queue behind a turn.
func seedQueued(h *harness) {
	h.t.Helper()
	r := armedRule("r1", specOf("Tidy", nil, everyT("t1", 5, "")), h.at(0))
	r.Status.LastFailure = &rule.Mark{
		Trigger: "t1", EventID: "every:t1:1", Summary: "every 5 min", At: h.at(0),
		Reason: "Could not deliver after 10 tries: busy",
	}
	h.seedBot("b1", r)
	h.notes.place = queuedOnce
	h.scan()
	h.start()
	h.advance(5 * time.Minute)
	h.wantSent("the queued send", 1)
}

// A prompt that queued behind a turn is in the chat: the event stays, marked, until that turn opens, is never sent
// again, and counts against the rule only then.
func TestEngine_AQueuedPromptCountsWhenItsTurnOpens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		seedQueued(h)
		event := dueID("t1", h.at(5*time.Minute))
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 1 {
			t.Fatalf("queue %v, want the queued event kept", eventIDs(r.Status.Queue))
		}
		q := r.Status.Queue[0]
		if q.EventID != event || q.Stage != rule.StageFired || !q.Queued || q.MessageID != "m-1" {
			t.Errorf("event %+v, want %s fired, queued, message m-1", q, event)
		}
		if r.Status.LastFailure != nil || len(r.Status.Sent) != 0 {
			t.Errorf("last failure %+v sent %v, want the failure cleared and nothing counted", r.Status.LastFailure, r.Status.Sent)
		}

		h.advance(5 * time.Minute)
		h.wantSent("while the row waits", 1)
		r = h.rule("b1", "r1")
		if len(r.Status.Queue) != 2 || !r.Status.Queue[0].Queued || r.Status.Queue[0].EventID != event || r.Status.Dropped != 0 {
			t.Errorf("queue %v dropped %d, want the queued event kept beside the 10 min one", eventIDs(r.Status.Queue), r.Status.Dropped)
		}

		h.takeQueuedRow("b1", true)
		r = h.rule("b1", "r1")
		if len(r.Status.Queue) != 1 || r.Status.Queue[0].EventID != dueID("t1", h.at(10*time.Minute)) ||
			len(r.Status.Sent) != 1 || r.Status.Sent[0] != h.at(10*time.Minute) {
			t.Errorf("queue %v sent %v, want the queued event gone and counted at %d", eventIDs(r.Status.Queue), r.Status.Sent, h.at(10*time.Minute))
		}

		h.endTurn("b1")
		h.advance(time.Minute)
		got := h.wantSent("the next prompt", 2)
		if eventIDOf(got[1]) != dueID("t1", h.at(10*time.Minute)) {
			t.Errorf("second prompt %q, want the 10 min event", eventIDOf(got[1]))
		}
	})
}

// A queued row that leaves the run list with no turn seen is looked for in the log: a turn that opened with it is a
// delivery, and a row the user discarded is a drop.
func TestEngine_AQueuedRowThatLeftTheListIsSettledByTheLog(t *testing.T) {
	t.Run("its turn opened unseen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			seedQueued(h)
			h.takeQueuedRow("b1", false)
			h.advance(time.Minute)
			h.wantSent("after the take", 1)
			r := h.rule("b1", "r1")
			if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
				t.Errorf("queue %v sent %v dropped %d, want it removed as delivered", eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
			}
		})
	})
	t.Run("the user discarded it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t)
			seedQueued(h)
			h.advance(10 * time.Second)
			h.dropQueuedRow("b1")
			h.advance(time.Minute)
			h.wantSent("after the discard", 1)
			r := h.rule("b1", "r1")
			if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 0 || r.Status.Dropped != 1 {
				t.Errorf("queue %v sent %v dropped %d, want it dropped and counted, nothing sent", eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
			}
		})
	})
}

// The boot takes back the held row of the queued event alone: the rule's next event, waiting beside it, has no row,
// and both are delivered in fired order.
func TestEngine_BootTakesBackOnlyTheQueuedEventsRow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		seedQueued(h)
		h.advance(5 * time.Minute)
		if q := h.rule("b1", "r1").Status.Queue; len(q) != 2 {
			t.Fatalf("queue %v, want the queued event and the 10 min one", eventIDs(q))
		}
		h.restart()
		if got := h.notes.unqueuedRows(); !slices.Equal(got, []string{"m-1"}) {
			t.Errorf("unqueued %v, want the queued event's row m-1 once", got)
		}
		got := h.wantSent("the boot pass", 1)
		if eventIDOf(got[0]) != dueID("t1", h.at(5*time.Minute)) {
			t.Errorf("resent %q, want the 5 min event", eventIDOf(got[0]))
		}
		h.endTurn("b1")
		h.advance(time.Minute)
		got = h.wantSent("after the turn", 2)
		if eventIDOf(got[1]) != dueID("t1", h.at(10*time.Minute)) {
			t.Errorf("then %q, want the 10 min event", eventIDOf(got[1]))
		}
	})
}

// A turn the tap saw open with a queued prompt counts as its delivery even when a write fault held the settle back
// until the turn's turn_open left the log tail the settle reads.
func TestEngine_ATappedTurnCountsThoughItsOpenLeftTheLogTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		seedQueued(h)
		h.fs.failing.Store(true)
		h.takeQueuedRow("b1", true)
		entries, err := h.store.All(t.Context(), "b1")
		if err != nil || len(entries) == 0 || entries[len(entries)-1].Kind != marotte.EntryKindTurnOpen {
			t.Fatalf("log %v, %v: want the row's turn_open last", entries, err)
		}
		turn := entries[len(entries)-1].Turn
		for i := range logTail + 50 {
			raw, err := json.Marshal(marotte.EntryText{Text: "working"})
			if err != nil {
				t.Fatal(err)
			}
			e := &marotte.Entry{ID: "x-" + strconv.Itoa(i), Turn: turn, Kind: marotte.EntryKindText, Payload: raw}
			if err := h.store.Append(t.Context(), "b1", e); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		h.fs.failing.Store(false)
		h.advance(time.Minute)
		r := h.rule("b1", "r1")
		if len(r.Status.Queue) != 0 || len(r.Status.Sent) != 1 || r.Status.Dropped != 0 {
			t.Errorf("queue %v sent %v dropped %d, want it removed as delivered", eventIDs(r.Status.Queue), r.Status.Sent, r.Status.Dropped)
		}
	})
}
