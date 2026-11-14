package agent

// What a chat's close and a process restart leave of the user's steers and the notify door's wakes, on the chat's
// real run list and steer queue, kiro-cli 3.2.0's steering faked at the bridge.

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// steerEntriesOf is c1's steer entries by id.
func steerEntriesOf(t *testing.T, cs *testChatStore) map[string][]marotte.EntrySteer {
	t.Helper()
	out := map[string][]marotte.EntrySteer{}
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var steer marotte.EntrySteer
		decodePayload(t, e, &steer)
		out[e.ID] = append(out[e.ID], steer)
	}
	return out
}

func callerLabel(c *marotte.Caller) string {
	if c == nil {
		return ""
	}
	return c.Label
}

// rowsAfterRestart is c1's run list as the next process's first open of the chat leaves it.
func rowsAfterRestart(t *testing.T, cs *testChatStore) []marotte.RunEntry {
	t.Helper()
	next, err := chat.NewStore(cs.Dir())
	if err != nil {
		t.Fatalf("the next process's store: %v", err)
	}
	if _, err := next.All(t.Context(), "c1"); err != nil {
		t.Fatalf("the next process's first open: %v", err)
	}
	c, ok := next.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("the next process finds no chat")
	}
	return c.Run
}

// A restart keeps the user's queued words, held, and never the words of a steer kiro-cli held or of a wake: kiro-cli's
// own log decides a held steer, and grelot sends again while its wake stays unread. Each steer and each wake the
// restart cut gets its restart entry, a waiting wake's carrying its words.
func TestHoldUnread_CarriesOnlyTheUsersWords(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	kas320On(s.h, "c1", br)
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
	if got := ackAs(t, awaitAnswer(t, sendAsUser(s.h, "c1", "m-0", "start"))); got != "started" {
		t.Fatalf("Setup: the first prompt answered %q, want started", got)
	}
	waitForCall(t, br, marotte.MethodPrompt)
	id, _ := ownID(s.h, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	placeWakeFrom(t, s.h, "c1", "n-ci", "ci")
	if !reachesState(s.h, "c1", "n-ci", marotte.RunInKAS) {
		t.Fatal("Setup: the first wake was never steered into the running turn")
	}
	queueTextDuring(t, s.h, "m-q", "later")
	placeWake(t, s.h, "c1", "n-1")
	if got := ackAs(t, awaitAnswer(t, sendAsUser(s.h, "c1", "m-u1", "first"))); got != "steered" {
		t.Fatalf("Setup: the user's steer answered %q, want steered", got)
	}
	if got := stateOf(s.h, "c1", "n-1"); got != marotte.RunPending {
		t.Fatalf("Setup: the second wake is %q, want waiting behind the user's queued row", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}

	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want the cut turn's", len(closes))
	}
	steers := steerEntriesOf(t, cs)
	for _, want := range []struct {
		key, text, caller string
	}{
		{key: "steer-n-ci", caller: "ci"},
		{key: "steer-n-1", text: grelotWake, caller: "grelot"},
		{key: "steer-m-u1"},
	} {
		got := steers[want.key]
		if len(got) != 1 || got[0].State != marotte.SteerStateDropped || got[0].Reason != marotte.SteerReasonRestart ||
			got[0].Text != want.text || callerLabel(got[0].Caller) != want.caller {
			t.Errorf("%s entries = %+v, want one dropped by the restart, text %q, from %q", want.key, got, want.text, want.caller)
		}
	}
	if got := steers["steer-m-q"]; len(got) != 0 {
		t.Errorf("the queued row got steer entries %+v, want none: it is still queued", got)
	}
	var rows []string
	for _, e := range rowsAfterRestart(t, cs) {
		rows = append(rows, e.ID+":"+string(e.State)+":"+e.Row.Text)
	}
	if len(rows) != 1 || rows[0] != "m-q:held:later" {
		t.Errorf("the next process's list = %v, want only the user's queued row, held", rows)
	}
}

// A wake waiting on a chat whose session is not running when the process stops still gets its restart entry, with
// its words.
func TestHoldUnread_AWakeOnAChatWithNoSessionIsRecorded(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	lc := h.coord.turns.testLifecycle("c1")
	lc.mu.Lock()
	lc.run.entries = append(lc.run.entries, &runEntry{
		id: "m-p", kind: marotte.RunKindRow, origin: marotte.RunFromQueue, state: marotte.RunPaused,
		want: marotte.RunWantPrompt, row: &marotte.QueuedPrompt{ID: "m-p", Text: "after the stop"},
	})
	lc.mu.Unlock()
	placeWake(t, h, "c1", "n-1")
	if got := stateOf(h, "c1", "n-1"); got != marotte.RunPending {
		t.Fatalf("Setup: the wake is %q, want waiting behind the user's paused row", got)
	}

	shutdownHub(t, h)

	got := steerEntriesOf(t, cs)["steer-n-1"]
	if len(got) != 1 || got[0].State != marotte.SteerStateDropped || got[0].Reason != marotte.SteerReasonRestart ||
		got[0].Text != grelotWake || callerLabel(got[0].Caller) != "grelot" {
		t.Errorf("steer-n-1 entries = %+v, want one dropped by the restart carrying the wake from grelot", got)
	}
}

// A close notes each steer kiro-cli still held as not read, the user's and a wake's, with its words and its sender;
// a delete notes none. Neither comes back as a queued row in the chat's next life.
func TestBeginChatTeardown_ACloseNotesEverySteerTheAgentHolds(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep bool
	}{{"close", true}, {"delete", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, _, _ := wakeInKAS(t, [2]string{"m-u1", "first"})

			s.h.BeginChatTeardown("c1", tc.keep)

			steers := steerEntriesOf(t, cs)
			for _, want := range []struct {
				key, text, caller string
			}{
				{key: "steer-m-u1", text: "first"},
				{key: "steer-n-1", text: grelotWake, caller: "grelot"},
			} {
				got := steers[want.key]
				switch {
				case !tc.keep && len(got) != 0:
					t.Errorf("%s notes = %+v, want none on a delete", want.key, got)
				case tc.keep && (len(got) != 1 || got[0].Reason != marotte.SteerReasonBoundary || got[0].Text != want.text ||
					callerLabel(got[0].Caller) != want.caller):
					t.Errorf("%s notes = %+v, want one not-read note carrying %q from %q", want.key, got, want.text, want.caller)
				}
			}
			for _, e := range queuedRows(t, cs, "c1") {
				t.Errorf("the next life's list holds %s (%s), want no row for a steer", e.ID, e.State)
			}
		})
	}
}
