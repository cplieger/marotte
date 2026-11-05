package agent

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

func turnsOf(rt *Runtime, chatID marotte.ChatID) []command.CloseTurn {
	return rt.CloseImpact(context.Background(), []marotte.ChatID{chatID})[chatID].Turns
}

func oneTurn(t *testing.T, got []command.CloseTurn, want command.CloseTurn) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("turns = %+v, want exactly %+v", got, want)
	}
	g := got[0]
	if g.ID != want.ID || g.MessageID != want.MessageID || g.Admission != want.Admission || !slices.Equal(g.Resends, want.Resends) {
		t.Errorf("turn = %+v, want %+v", g, want)
	}
}

// Each kind of turn a close stops is named by the ids a page can know it by.
func TestCloseImpact_NamesEachTurnByItsIds(t *testing.T) {
	t.Run("an open prompt turn names its turn, prompt, admission and resends", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		out, res := rt.coord.ReserveTurnForPrompt(t.Context(), "c1", time.Second, rt.coord.LifeStamp(), "m-1")
		if out != command.AdmissionAcquired {
			t.Fatalf("reserve = %v", out)
		}
		id, err := rt.OpenTurn(t.Context(), "c1", command.TurnOpen{
			Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hi", Resends: []string{"s-1"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		oneTurn(t, turnsOf(rt, "c1"), command.CloseTurn{ID: id, MessageID: "m-1", Admission: command.AdmissionID(res.Token), Resends: []string{"s-1"}})
	})
	t.Run("a prompt admitted with no turn yet names its prompt and admission", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		_, res := rt.coord.ReserveTurnForPrompt(t.Context(), "c1", time.Second, rt.coord.LifeStamp(), "m-2")
		oneTurn(t, turnsOf(rt, "c1"), command.CloseTurn{MessageID: "m-2", Admission: command.AdmissionID(res.Token)})
	})
	t.Run("a reservation with no prompt id is named by its admission", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		res, ok := rt.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, rt.coord.LifeStamp())
		if !ok {
			t.Fatal("reserve refused")
		}
		oneTurn(t, turnsOf(rt, "c1"), command.CloseTurn{Admission: command.AdmissionID(res.Token)})
	})
	t.Run("a released reservation stops nothing", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		res, _ := rt.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, rt.coord.LifeStamp())
		rt.coord.ReleaseTurnReservation(res)
		if got := turnsOf(rt, "c1"); len(got) != 0 {
			t.Errorf("turns = %+v, want none", got)
		}
	})
	t.Run("a drain's reservation is named by its row, not as a turn", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		if !rt.coord.tryReserveFencedNow("c1", marotte.TurnSourcePrompt, fenceNow(rt, "c1")) {
			t.Fatal("the drain's reserve refused")
		}
		if got := turnsOf(rt, "c1"); len(got) != 0 {
			t.Errorf("turns = %+v, want none before the drain's turn opens", got)
		}
	})
	t.Run("a wire turn is named by its turn id alone", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		rt.stageWireTurn(t, "c1")
		got := turnsOf(rt, "c1")
		if len(got) != 1 || got[0].ID == "" || got[0].Admission != "" || got[0].MessageID != "" {
			t.Errorf("turns = %+v, want one wire turn by id", got)
		}
	})
	t.Run("a finalizing turn stops nothing", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		id, _ := rt.stagePromptTurn(t, "c1")
		lc, _ := rt.coord.turns.lookup("c1")
		lc.mu.Lock()
		claimed := lc.claimIfLocked(lc.liveLocked(id), nil)
		lc.mu.Unlock()
		if !claimed {
			t.Fatal("could not claim the turn")
		}
		if got := turnsOf(rt, "c1"); len(got) != 0 {
			t.Errorf("turns = %+v, want none for a finalizing turn", got)
		}
	})
	t.Run("a chat with no life stops no turn", func(t *testing.T) {
		rt, _, _ := newTestHub()
		if got := turnsOf(rt, "c-none"); len(got) != 0 {
			t.Errorf("turns = %+v, want none", got)
		}
	})
}

func TestCloseImpact_NamesTheChatsAsksRunsAndQueuedRows(t *testing.T) {
	rt, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	first := rt.bus.pendingPerms.add(10, permNeeded("c1", 0), nil)
	rt.bus.pendingPerms.add(11, permNeeded("c2", 0), nil)
	second := rt.bus.pendingPerms.add(12, permNeeded("c1", 0), nil)
	for _, l := range []runlease.Lease{{WorkflowID: "wf-1", ChatID: "c1", Origin: runlease.OriginAgent}, {WorkflowID: "wf-2", ChatID: "c2", Origin: runlease.OriginAgent}, {WorkflowID: "wf-3", Origin: runlease.OriginManual}} {
		if err := rt.runs.leaseStore().Put(t.Context(), &l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.QueuedPrompts = []marotte.QueuedPrompt{{ID: "q-1", Text: "a"}, {ID: "q-2", Text: "b", Held: true}}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := cs.Dequeue(t.Context(), "c1", "q-1"); err != nil {
		t.Fatal(err)
	}

	w := rt.CloseImpact(t.Context(), []marotte.ChatID{"c1"})["c1"]

	wantAsks := []string{askIDString(t, first), askIDString(t, second)}
	if !slices.Equal(w.Asks, wantAsks) {
		t.Errorf("asks = %v, want %v (the chat's own, in ask order)", w.Asks, wantAsks)
	}
	if !slices.Equal(w.Runs, []string{"wf-1"}) {
		t.Errorf("runs = %v, want the run the chat launched", w.Runs)
	}
	if !slices.Equal(w.Queued, []string{"q-2"}) {
		t.Errorf("queued = %v, want the row whose turn has not opened", w.Queued)
	}
}

func askIDString(t *testing.T, evt marotte.ServerEvent) string {
	t.Helper()
	p, ok := evt.Payload.(marotte.PermissionNeededPayload)
	if !ok {
		t.Fatalf("payload = %T", evt.Payload)
	}
	return strconv.FormatInt(p.RequestID, 10)
}

// The steers a close names are the rows its teardown drops.
func TestSteerRecords_UnreadKeysAreWhatTheTeardownDrops(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")

	keys := s.recs.unreadKeys(s.chat)
	unread := s.recs.beginTeardown(s.chat, nil)

	var dropped []string
	for _, p := range unread {
		dropped = append(dropped, p.SteerID)
	}
	if !slices.Equal(keys, dropped) || len(keys) != 2 {
		t.Errorf("unreadKeys = %v, the teardown dropped %v; want the same two rows", keys, dropped)
	}
	if got := s.recs.unreadKeys(s.chat); len(got) != 0 {
		t.Errorf("unreadKeys after the teardown = %v, want none", got)
	}
}
