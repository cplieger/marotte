package agent

// A steer KAS holds when a prompt's execution ends: KAS 2.37.1 continues the same session/prompt with a new
// execution while unread steering remains, so marotte settles the row only once KAS reports the session idle.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
)

// turnEndedBesideASteer is a bound prompt turn KAS still runs (its session/prompt hold reports the session busy)
// whose turn_end folds with steer-a unread in KAS's buffer: the shape a steer sent during a turn's Stop hook leaves.
// KAS answers a clear with every id it holds. Run inside a synctest bubble.
func turnEndedBesideASteer(t *testing.T) (*steerHarness, *testChatStore, *fakeBridge) {
	t.Helper()
	s, cs, br := steerOpsHub(t)
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply(s.kasHolds()), nil, true
	}
	id, _ := s.h.stagePromptTurn(t, "c1")
	send(t, s.h, newPromptTurnStartMsg(id), newActivityMsg(true))
	s.steer("steer-a", "keep building")
	send(t, s.h, newTurnEndMsg("end_turn"))
	s.h.coord.ReleaseTurn("c1", id)
	synctest.Wait()
	return s, cs, br
}

// carriedPrompts is every opened prompt that carries a steer row as its words.
func carriedPrompts(t *testing.T, cs *testChatStore) []*marotte.EntryPrompt {
	t.Helper()
	var out []*marotte.EntryPrompt
	for _, o := range opensOf(t, logOf(t, cs, "c1")) {
		if o.Prompt != nil && len(o.Prompt.Resends) > 0 {
			out = append(out, o.Prompt)
		}
	}
	return out
}

// KAS continues the prompt for the unread steer: the continuation's turn_start opens a wire turn, whose
// mid-turn read delivers the row. A clear in that window would empty the buffer under the decided
// continuation, which then runs on KAS's hidden "Additional input arrived" prompt alone.
func TestSteerTurnEnd_KASContinuationDeliversTheRowWithNoClear(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, cs, br := turnEndedBesideASteer(t)

		send(t, s.h, newTurnStartMsg())
		synctest.Wait()
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Fatalf("_session/steer/clear calls = %d before the continuation read the row, want 0", n)
		}
		send(t, s.h, injectedMsg("steer-a", "keep building"))
		// The continuation ends; KAS's own boundary clear names the read row too, then the prompt's hold ends.
		send(t, s.h, newTurnEndMsg("end_turn"), clearedMsg("steer-a"), newActivityMsg(false))
		synctest.Wait()

		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d, want 0: KAS's continuation was reading the row", n)
		}
		if _, live := s.state("steer-a"); live {
			t.Error("steer-a is still in the dock, want it retired by the continuation's read")
		}
		if got := steerEntries(t, cs, "c1")["steer-a"]; len(got) != 1 || got[0].State != marotte.SteerStateRead {
			t.Errorf("steer entries for steer-a = %+v, want one read entry", got)
		}
		if got := carriedPrompts(t, cs); len(got) != 0 {
			t.Errorf("prompts carrying a steer = %+v, want none: the agent already read it", got)
		}
		if n := countCalls(br, marotte.MethodPrompt); n != 0 {
			t.Errorf("session/prompt calls = %d, want 0", n)
		}
	})
}

// KAS stops instead (its continuation cap, or no unread past its cursor): its own boundary clear names the row
// before the prompt's hold ends. marotte sends no clear of its own and carries the row as the next prompt, once.
func TestSteerTurnEnd_KASStoppingCarriesTheRowAsTheNextPromptOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, cs, br := turnEndedBesideASteer(t)
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Fatalf("_session/steer/clear calls = %d while KAS still ran the prompt, want 0", n)
		}

		send(t, s.h, clearedMsg("steer-a"), newActivityMsg(false))
		synctest.Wait()

		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d, want 0: KAS's own boundary already dropped the row", n)
		}
		got := carriedPrompts(t, cs)
		if len(got) != 1 || got[0].Text != "keep building" || !slices.Equal(got[0].Resends, []string{"steer-a"}) {
			t.Errorf("prompts carrying a steer = %+v, want one carrying steer-a's words", got)
		}
	})
}

// A row KAS still holds when it reports the session idle (it arrived after KAS's own boundary clear) is proved
// gone by marotte's clear, then carried; the clear waits for the idle report, never racing the run KAS still
// reports.
func TestSteerTurnEnd_TheStrayClearWaitsForKASIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, cs, br := turnEndedBesideASteer(t)

		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d while KAS still ran the prompt, want 0", n)
		}
		s.mustState("steer-a", rowQueued)

		send(t, s.h, newActivityMsg(false))
		synctest.Wait()

		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 1 {
			t.Errorf("_session/steer/clear calls = %d once KAS went idle, want 1", n)
		}
		got := carriedPrompts(t, cs)
		if len(got) != 1 || !slices.Equal(got[0].Resends, []string{"steer-a"}) {
			t.Errorf("prompts carrying a steer = %+v, want one carrying steer-a", got)
		}
	})
}

// The delivering state is a fact of the record, not of the row: the turn end frames it and a reconnect replays it,
// and KAS going idle frames the row out of it.
func TestSteerTurnEnd_TheDeliveringStateIsFramedAndReplayed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, _ := turnEndedBesideASteer(t)

		if f := s.lastFrame("steer-a"); f.State != marotte.SteerRowDelivering {
			t.Errorf("steer-a's frame after the turn end = %q, want delivering", f.State)
		}
		if f := s.replayed("steer-a"); f.State != marotte.SteerRowDelivering {
			t.Errorf("steer-a's replayed frame = %q, want delivering", f.State)
		}

		// KAS took a later steer after the end folded, then stopped with that one unread past its boundary.
		s.steer("steer-b", "and this")
		if f := s.lastFrame("steer-b"); f.State != marotte.SteerRowDelivering {
			t.Errorf("steer-b's frame in the turn-end window = %q, want delivering", f.State)
		}
		send(t, s.h, clearedMsg("steer-a"), newActivityMsg(false))
		synctest.Wait()
		if f := s.lastFrame("steer-b"); f.State == marotte.SteerRowDelivering {
			t.Errorf("steer-b's frame once KAS went idle = %q, want it out of delivering", f.State)
		}
	})
}

// KAS has no per-steer removal, so any clear in the turn-end window empties the buffer a continuation reads: a delete
// of a row KAS took after the turn end folded is refused like the end's own rows.
func TestSteerTurnEnd_ARowSentAfterTheEndIsNotDeletedUnderTheContinuation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, br := turnEndedBesideASteer(t)
		s.steer("steer-b", "and this")

		rec := postCmd(t, s.h, removeCmd("steer-b"))

		if rec.Code != http.StatusConflict {
			t.Errorf("steer_remove = %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d, want 0", n)
		}
		s.mustState("steer-b", rowQueued)
	})
}

// Discard all is one clear of the whole buffer, so in the turn-end window it is refused until KAS settles the end.
func TestSteerTurnEnd_DiscardAllWaitsOutTheTurnEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, br := turnEndedBesideASteer(t)

		rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdSteerClear, ChatID: "c1"})

		if rec.Code != http.StatusConflict {
			t.Errorf("steer_clear = %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d, want 0", n)
		}
		s.mustState("steer-a", rowQueued)
	})
}

// Once a continuation's turn_start binds, the end's unread row is still the continuation's to read: a delete of
// another row would clear it too, so it is refused until that read.
func TestSteerTurnEnd_AContinuationKeepsTheEndsRowUntilItReadsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, br := turnEndedBesideASteer(t)
		send(t, s.h, newTurnStartMsg())
		synctest.Wait()
		s.steer("steer-b", "and this")

		if rec := postCmd(t, s.h, removeCmd("steer-b")); rec.Code != http.StatusConflict {
			t.Errorf("steer_remove before the read = %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d before the read, want 0", n)
		}

		if f := s.lastFrame("steer-b"); f.State != marotte.SteerRowDelivering {
			t.Errorf("steer-b's frame before the read = %q, want delivering: its delete is refused", f.State)
		}

		send(t, s.h, injectedMsg("steer-a", "keep building"))
		synctest.Wait()
		if f := s.lastFrame("steer-b"); f.State != marotte.SteerRowQueued {
			t.Errorf("steer-b's frame after the read = %q, want queued: its delete goes through", f.State)
		}
		if rec := postCmd(t, s.h, removeCmd("steer-b")); rec.Code != http.StatusOK {
			t.Errorf("steer_remove after the read = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}

// Most continuations carry only a workflow notice. After the continuation's turn_start binds, the notice is still
// the continuation's to read, so Discard all is refused until that read: its clear would leave KAS's hidden
// "Additional input arrived" prompt with nothing after it.
func TestSteerTurnEnd_ANoticeOnlyContinuationKeepsItsNoticeUntilItReadsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, br := steerOpsHub(t)
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method != marotte.MethodSessionSteerClear {
				return nil, nil, false
			}
			return clearReply(s.kasHolds()), nil, true
		}
		discard := marotte.ClientCommand{Type: marotte.CmdSteerClear, ChatID: "c1"}
		id, _ := s.h.stagePromptTurn(t, "c1")
		send(t, s.h, newPromptTurnStartMsg(id), newActivityMsg(true))
		s.recs.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "notify-1", Text: "a run finished", Origin: marotte.SteerOriginAgent})
		send(t, s.h, newTurnEndMsg("end_turn"))
		s.h.coord.ReleaseTurn("c1", id)
		synctest.Wait()
		send(t, s.h, newTurnStartMsg())
		synctest.Wait()

		if f := s.lastFrame("notify-1"); f.State != marotte.SteerRowDelivering {
			t.Errorf("notify-1's frame after the continuation bound = %q, want delivering", f.State)
		}
		if rec := postCmd(t, s.h, discard); rec.Code != http.StatusConflict {
			t.Errorf("steer_clear before the read = %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Fatalf("_session/steer/clear calls = %d before the continuation read the notice, want 0", n)
		}

		send(t, s.h, injectedMsg("notify-1", "a run finished"))
		s.recs.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "notify-2", Text: "another run finished", Origin: marotte.SteerOriginAgent})
		synctest.Wait()
		if rec := postCmd(t, s.h, discard); rec.Code != http.StatusOK {
			t.Errorf("steer_clear after the read = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 1 {
			t.Errorf("_session/steer/clear calls = %d after the read, want 1", n)
		}
	})
}

// turnEndedWithNothingUnread is a bound prompt turn KAS still runs whose turn_end folds with KAS's buffer empty, so
// the end owns no row. KAS answers a clear with every id it holds. Run inside a synctest bubble.
func turnEndedWithNothingUnread(t *testing.T) (*steerHarness, *fakeBridge) {
	t.Helper()
	s, _, br := steerOpsHub(t)
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply(s.kasHolds()), nil, true
	}
	id, _ := s.h.stagePromptTurn(t, "c1")
	send(t, s.h, newPromptTurnStartMsg(id), newActivityMsg(true))
	send(t, s.h, newTurnEndMsg("end_turn"))
	s.h.coord.ReleaseTurn("c1", id)
	synctest.Wait()
	return s, br
}

// KAS can take a steer after marotte folded the turn_end and still continue for it, so the continuation that binds
// next may run for that row alone: a delete of it before the continuation reads it would leave KAS's hidden
// "Additional input arrived" prompt with nothing after it.
func TestSteerTurnEnd_ARowKASTookAfterTheEndStaysDeliveringPastTheBind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, br := turnEndedWithNothingUnread(t)
		s.steer("steer-b", "late")
		send(t, s.h, newTurnStartMsg())
		synctest.Wait()

		if f := s.lastFrame("steer-b"); f.State != marotte.SteerRowDelivering {
			t.Errorf("steer-b's frame after the continuation bound = %q, want delivering", f.State)
		}
		if rec := postCmd(t, s.h, removeCmd("steer-b")); rec.Code != http.StatusConflict {
			t.Errorf("steer_remove after the bind, before the read = %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Fatalf("_session/steer/clear calls = %d before the continuation read steer-b, want 0", n)
		}

		send(t, s.h, injectedMsg("steer-b", "late"))
		synctest.Wait()
		s.steer("steer-c", "later")
		if f := s.lastFrame("steer-c"); f.State != marotte.SteerRowQueued {
			t.Errorf("steer-c's frame after the read = %q, want queued: its delete goes through", f.State)
		}
		if rec := postCmd(t, s.h, removeCmd("steer-c")); rec.Code != http.StatusOK {
			t.Errorf("steer_remove after the read = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}

// The same holds for a workflow notice KAS took after the turn_end folded: Discard all stays refused past the
// continuation's bind until the continuation reads it.
func TestSteerTurnEnd_ANoticeKASTookAfterTheEndKeepsDiscardRefusedPastTheBind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, br := turnEndedWithNothingUnread(t)
		discard := marotte.ClientCommand{Type: marotte.CmdSteerClear, ChatID: "c1"}
		in, _ := s.recs.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "notify-1", Text: "a run finished", Origin: marotte.SteerOriginAgent})
		if in.State != marotte.SteerRowDelivering {
			t.Errorf("notify-1's frame in the turn-end window = %q, want delivering", in.State)
		}
		send(t, s.h, newTurnStartMsg())
		synctest.Wait()

		if f := s.replayed("notify-1"); f.State != marotte.SteerRowDelivering {
			t.Errorf("notify-1's state after the continuation bound = %q, want delivering", f.State)
		}
		if rec := postCmd(t, s.h, discard); rec.Code != http.StatusConflict {
			t.Errorf("steer_clear before the read = %d %s, want 409", rec.Code, rec.Body.String())
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Fatalf("_session/steer/clear calls = %d before the continuation read the notice, want 0", n)
		}

		send(t, s.h, injectedMsg("notify-1", "a run finished"))
		synctest.Wait()
		later, _ := s.recs.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "notify-2", Text: "another run finished", Origin: marotte.SteerOriginAgent})
		if later.State != marotte.SteerRowQueued {
			t.Errorf("notify-2's frame after the read = %q, want queued", later.State)
		}
		if rec := postCmd(t, s.h, discard); rec.Code != http.StatusOK {
			t.Errorf("steer_clear after the read = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}
