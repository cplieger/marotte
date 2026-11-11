package agent

import (
	"context"
	"slices"
	"strconv"
	"sync"
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
		out, res := rt.coord.reservePromptSlot(t.Context(), "c1", time.Second, rt.coord.LifeStamp(), "m-1")
		if out != admissionAcquired {
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
	t.Run("a page's prompt taken with no turn yet names its prompt and admission", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		release := holdOpens(rt)
		defer release()
		answered := postAsync(rt, promptCmd("m-2", "hi"))
		got := waitTurns(t, rt, 1)
		if got[0].ID != "" || got[0].MessageID != "m-2" || got[0].Admission == "" {
			t.Errorf("turn = %+v, want m-2 by its prompt and admission, no turn id", got[0])
		}
		release()
		<-answered
	})
	t.Run("a taken row whose submitter was answered queued is named by its row, not as a turn", func(t *testing.T) {
		wait := command.AdmissionWait
		command.AdmissionWait = 50 * time.Millisecond
		t.Cleanup(func() { command.AdmissionWait = wait })
		rt, cs, _ := newTestHub()
		// Registered after the restore, so it runs first: the close tail reads AdmissionWait until shutdown joins it.
		t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
		seedChat(t, cs, "c1")
		release := holdOpens(rt)
		defer release()
		wantQueuedAck(t, <-postAsync(rt, promptCmd("m-3", "hi")), "m-3")
		work := rt.CloseImpact(t.Context(), []marotte.ChatID{"c1"})["c1"]
		if len(work.Turns) != 0 || !slices.Equal(work.Queued, []string{"m-3"}) {
			t.Errorf("close work = turns %+v queued %v, want no turn and m-3 queued", work.Turns, work.Queued)
		}
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

// holdOpens parks every turn open at its seam until release, which may be called more than once.
func holdOpens(rt *Runtime) (release func()) {
	gate := make(chan struct{})
	var once sync.Once
	rt.coord.turns.openHook = func() { <-gate }
	return func() { once.Do(func() { close(gate) }) }
}

// waitTurns waits for the close work of c1 to name n turns.
func waitTurns(t *testing.T, rt *Runtime, n int) []command.CloseTurn {
	t.Helper()
	var got []command.CloseTurn
	if !eventually(func() bool { got = turnsOf(rt, "c1"); return len(got) == n }) {
		t.Fatalf("turns = %+v, want %d", got, n)
	}
	return got
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
		c.Run = []marotte.RunEntry{
			{ID: "q-1", Kind: marotte.RunKindRow, State: marotte.RunStarting, Turn: "t-q1", Row: &marotte.QueuedPrompt{ID: "q-1", Text: "a"}},
			{ID: "q-2", Kind: marotte.RunKindRow, State: marotte.RunHeld, Row: &marotte.QueuedPrompt{ID: "q-2", Text: "b"}},
		}
		return true
	}); err != nil {
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
