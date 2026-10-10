package agent

// The steer commands against the real record and turn registry, the fake holding an RPC open while another event lands.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

func removeCmd(key string) marotte.ClientCommand {
	payload, _ := json.Marshal(marotte.SteerRemoveCommand{SteerID: key})
	return marotte.ClientCommand{Type: marotte.CmdSteerRemove, ChatID: "c1", Payload: payload}
}

func removedBody(t *testing.T, code int, body []byte) map[string]any {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("steer_remove = %d %s, want 200", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func injectedMsg(id, text string) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{"kind": "steering_injected", "messageId": id, "content": text})
}

func clearedMsg(ids ...string) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{"kind": "steering_cleared", "messageIds": ids})
}

// steerOpsHub is a runtime whose chat c1 has a live bridge the steer commands reach.
func steerOpsHub(t *testing.T) (*steerHarness, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteer: json.RawMessage(`{"queued":true}`)}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	return harnessOn(h, "c1", t.Fatalf), cs, br
}

func clearReply(ids []string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"cleared": true, "messageIds": ids})
	return raw
}

// awaitLockWaiters polls until n holders and waiters share the chat's steer lock.
func awaitLockWaiters(t *testing.T, q steerQueue, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.locks.mu.Lock()
		l := q.locks.m["c1"]
		refs := 0
		if l != nil {
			refs = l.refs
		}
		q.locks.mu.Unlock()
		if refs >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("steer lock refs = %d, want %d", refs, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A read emitted before the clear's reply that never folds leaves the clear unsettled: nothing is deleted,
// and the late read is the row's one entry.
func TestCmdSteerRemove_AReadThatCannotFoldBeforeTheBarrierDeletesNothing(t *testing.T) {
	s, _, br := steerOpsHub(t)
	gen := s.h.coord.turns.attachForward("c1")
	// No goroutine stands behind this generation, so close its exit as forward would.
	t.Cleanup(s.h.coord.turns.exitFor("c1", gen))
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		s.h.coord.turns.sealPosition("c1", gen)
		return clearReply([]string{"steer-a", "steer-b"}), []*marotte.RPCResponse{injectedMsg("steer-a", "first")}, true
	}

	rec := postCmd(t, s.h, removeCmd("steer-a"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("steer_remove = %d %s, want 502: the clear's outcome is unknown", rec.Code, rec.Body.String())
	}
	if n := s.spy.note("steer-a"); n != nil {
		t.Errorf("the target got entry %+v before its read folded", n)
	}
	s.mustState("steer-a", rowQueued)
	s.h.translateACPEvent("c1", injectedMsg("steer-a", "first"))
	if _, live := s.state("steer-a"); live {
		t.Error("the stranded read, once folded, did not retire the row")
	}
}

// Two deletes take the lock in turn; the second re-reads, finds its target in the first's probe, clears again
// and resends the rest under a fresh probe.
func TestCmdSteerRemove_TwoDeletesInFlightTakeTurns(t *testing.T) {
	s, _, br := steerOpsHub(t)
	s.bind()
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	s.steer("steer-c", "c")
	gate := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodSessionSteerClear: gate}
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply(s.kasHolds()), nil, true
	}

	type answer struct {
		body []byte
		code int
	}
	answers := make([]answer, 2)
	var wg sync.WaitGroup
	for i, key := range []string{"steer-a", "steer-b"} {
		wg.Go(func() {
			rec := postCmd(t, s.h, removeCmd(key))
			answers[i] = answer{code: rec.Code, body: rec.Body.Bytes()}
		})
		if i == 0 {
			waitForCall(t, br, marotte.MethodSessionSteerClear)
		}
	}
	awaitLockWaiters(t, s.h.steerQueue, 2)
	close(gate)
	wg.Wait()

	for i, key := range []string{"steer-a", "steer-b"} {
		if got := removedBody(t, answers[i].code, answers[i].body); got["deleted"] != key {
			t.Errorf("delete %s = %v, want deleted with its kept rows resent", key, got)
		}
		if n := s.spy.note(key); n == nil || n.Reason != marotte.SteerReasonDeleted {
			t.Errorf("entry %s = %+v, want deleted", key, n)
		}
	}
	want := []string{marotte.MethodSessionSteerClear, marotte.MethodSessionSteer, marotte.MethodSessionSteerClear, marotte.MethodSessionSteer}
	var got []string
	for _, m := range br.callLog() {
		if m == marotte.MethodSessionSteerClear || m == marotte.MethodSessionSteer {
			got = append(got, m)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("wire = %v, want each delete's clear and resubmit in turn", got)
	}
	s.mustState("steer-c", rowOutstanding)
	if msg := br.paramsFor(marotte.MethodSessionSteer)["message"]; msg != "c" {
		t.Errorf("the second probe carries %q, want c alone", msg)
	}
}

// The same row deleted twice at once: the second finds nothing waiting.
func TestCmdSteerRemove_TheSameRowTwiceAnswersNotWaiting(t *testing.T) {
	s, _, br := steerOpsHub(t)
	s.bind()
	s.steer("steer-a", "a")
	gate := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodSessionSteerClear: gate}

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { codes[i] = postCmd(t, s.h, removeCmd("steer-a")).Code })
		if i == 0 {
			waitForCall(t, br, marotte.MethodSessionSteerClear)
		}
	}
	awaitLockWaiters(t, s.h.steerQueue, 2)
	close(gate)
	wg.Wait()

	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusNotFound}) {
		t.Errorf("codes = %v, want one delete and one not_waiting", codes)
	}
}

// failingClear is a bridge whose clear fails after a hook runs, the shape a clear
// takes when the bridge dies under it.
type failingClear struct {
	*fakeBridge
	before func()
}

func (b failingClear) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	if method == marotte.MethodSessionSteerClear {
		b.before()
		return nil, 0, errors.New("the bridge exited")
	}
	return b.fakeBridge.CallAt(ctx, method, params)
}

// A turn ending during the delete's clear skips the op's rows; the op deletes its target and the close's
// pipeline resends the kept rows as the next prompt.
func TestCmdSteerRemove_ATurnEndingUnderTheClearResendsTheKeptRows(t *testing.T) {
	deathExit := make(chan struct{})
	close(deathExit)
	for _, tc := range []struct {
		stage func(t *testing.T, s *steerHarness, br *fakeBridge)
		// closed runs the close pipeline a staged end has no closer for.
		closed func(t *testing.T, s *steerHarness)
		name   string
	}{
		{name: "a cancel", stage: wireEndUnderTheClear("cancelled")},
		{name: "a natural end", stage: wireEndUnderTheClear("end_turn")},
		{name: "a bridge death", stage: func(_ *testing.T, s *steerHarness, br *fakeBridge) {
			s.bind()
			s.steer("steer-a", "a")
			s.steer("steer-b", "b")
			var once sync.Once
			s.h.bridge.mgr.remove("c1")
			s.h.bridge.mgr.insert("c1", &sharedBridge{bridge: failingClear{fakeBridge: br, before: func() {
				once.Do(func() {
					s.recs.TurnEnded("c1", command.SteerTurnEnd{
						TurnID: s.turnID(), BridgeDeath: true, Exit: deathExit, Source: marotte.TurnSourcePrompt,
					})
				})
			}}, state: bridgeIdle})
		}, closed: func(t *testing.T, s *steerHarness) {
			s.h.coord.afterTurnClose(t.Context(), "c1", closeFacts{exit: deathExit, outcome: marotte.TurnOutcomeInterrupted})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			tc.stage(t, s, br)

			rec := postCmd(t, s.h, removeCmd("steer-a"))

			if got := removedBody(t, rec.Code, rec.Body.Bytes()); got["deleted"] != "steer-a" {
				t.Errorf("body = %v, want steer-a deleted and the rest left to the turn end", got)
			}
			if n := s.spy.note("steer-a"); n == nil || n.Reason != marotte.SteerReasonDeleted {
				t.Errorf("target entry = %+v, want deleted", n)
			}
			if tc.closed != nil {
				tc.closed(t, s)
			}
			if p := resendPrompt(t, cs); p == nil || p.Text != "b" || !slices.Equal(p.Resends, []string{"steer-b"}) {
				t.Errorf("resend prompt = %+v, want b naming steer-b", p)
			}
			waitFor(t, func() bool {
				_, live := s.state("steer-b")
				return !live
			})
			if n := s.spy.note("steer-b"); n != nil {
				t.Errorf("kept entry = %+v, want none: the resend prompt carries it", n)
			}
		})
	}
}

// wireEndUnderTheClear stages a bound prompt turn whose end folds while the delete's clear is held open.
func wireEndUnderTheClear(stop string) func(t *testing.T, s *steerHarness, br *fakeBridge) {
	return func(t *testing.T, s *steerHarness, br *fakeBridge) {
		id, _ := s.h.stagePromptTurn(t, "c1")
		s.h.translateACPEvent("c1", newTurnStartMsg())
		s.steer("steer-a", "a")
		s.steer("steer-b", "b")
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method != marotte.MethodSessionSteerClear {
				return nil, nil, false
			}
			s.h.translateACPEvent("c1", clearedMsg("steer-a", "steer-b"))
			s.h.translateACPEvent("c1", newTurnEndMsg(stop))
			s.h.coord.ReleaseTurn("c1", id)
			return clearReply(nil), nil, true
		}
	}
}

// resendPrompt polls the log for the resend's turn_open, the one carrying resends.
func resendPrompt(t *testing.T, cs *testChatStore) *marotte.EntryPrompt {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, e := range logOf(t, cs, "c1") {
			if e.Kind != marotte.EntryKindTurnOpen {
				continue
			}
			var open marotte.EntryTurnOpen
			decodePayload(t, e, &open)
			if open.Prompt != nil && len(open.Prompt.Resends) > 0 {
				return open.Prompt
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

// A cancel naming a queued row as its lead ends the turn and opens the next with every unread row as its
// prompt, lead first; the log carries those words only as that prompt, never as a "not read" note.
func TestCmdCancel_TheSendNowRowOpensTheNextTurnWithNoNotReadNote(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.recs.note = func(ctx context.Context, chatID marotte.ChatID, id string, steer *marotte.EntrySteer) {
		s.h.coord.recordSteer(ctx, chatID, id, steer)
	}
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", newTurnStartMsg())
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")

	rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-b"}`)})
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if len(br.notified(marotte.MethodCancel)) == 0 {
		t.Fatal("no session/cancel reached the bridge")
	}
	s.h.translateACPEvent("c1", clearedMsg("steer-a", "steer-b"))
	s.h.translateACPEvent("c1", newTurnEndMsg("cancelled"))
	s.h.coord.ReleaseTurn("c1", id)

	if p := resendPrompt(t, cs); p == nil || p.Text != "second\n\nfirst" || !slices.Equal(p.Resends, []string{"steer-b", "steer-a"}) {
		t.Errorf("next prompt = %+v, want the lead then the other row, naming both", p)
	}
	waitFor(t, func() bool {
		_, liveA := s.state("steer-a")
		_, liveB := s.state("steer-b")
		return !liveA && !liveB
	})
	got := steerEntries(t, cs, "c1")
	for _, k := range []string{"steer-a", "steer-b"} {
		if e := got[k]; len(e) != 0 {
			t.Errorf("steer entries for %s = %+v, want none: the next prompt carries its words", k, e)
		}
	}
}

// A delete resends the kept rows as one steer under a fresh id; read after the ledger has forgotten that id
// (a turn outlives its TTL), the read entry still names both rows, so a later session/load merge inserts no
// "Not read" note for either.
func TestSteerRead_ACombinedResendReadAfterTheLedgerForgotItNamesItsRows(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", newTurnStartMsg())
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	s.steer("steer-c", "c")
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply(s.kasHolds()), nil, true
	}
	rec := postCmd(t, s.h, removeCmd("steer-b"))
	if got := removedBody(t, rec.Code, rec.Body.Bytes()); got["deleted"] != "steer-b" {
		t.Fatalf("steer_remove = %v, want steer-b deleted", got)
	}
	probe := s.probe()
	if probe == "" || s.kasID("steer-a") != probe || s.kasID("steer-c") != probe {
		t.Fatalf("kept rows under %q/%q, want both under the probe %q", s.kasID("steer-a"), s.kasID("steer-c"), probe)
	}
	// ForgetChat stands in for the TTL lapse: the ledger answers nothing about the probe.
	s.h.steerLedger.ForgetChat("c1")

	s.h.translateACPEvent("c1", injectedMsg(probe, "a\n\nc"))

	read := steerEntries(t, cs, "c1")[probe]
	if len(read) != 1 || !slices.Equal(read[0].Resends, []string{"steer-a", "steer-c"}) {
		t.Errorf("read entry for the probe = %+v, want one naming steer-a and steer-c", read)
	}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		steerRow("steer-a", marotte.EntrySteer{Text: "a", Origin: marotte.SteerOriginUser}),
		steerRow("steer-c", marotte.EntrySteer{Text: "c", Origin: marotte.SteerOriginUser}),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}
	merged, _ := MergeEntries(RecordTurnsOf(logOf(t, cs, "c1"), nil), projected, "sid-1")
	for _, turn := range merged {
		for _, e := range turn.Entries {
			if e.ID == "steer-a" || e.ID == "steer-c" {
				t.Errorf("merged turn %s holds steer %s %s, want none: the read resend carried it", e.Turn, e.ID, e.Payload)
			}
		}
	}
}

// A bridge death with a probe outstanding and a steer waiting collects both for one resend, marks the
// outstanding one possibly still in KAS's log, and owes the post-load clear.
func TestSteerRecords_ABridgeDeathCollectsOutstandingAndWaitingRows(t *testing.T) {
	s := newSteerHarness(t)
	probeSent(s)
	s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
	s.mustState("steer-c", rowWaiting)

	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{TurnID: s.turnID(), BridgeDeath: true, Source: marotte.TurnSourcePrompt})

	ends := s.q.Ends(s.chat)
	if len(ends) != 1 || !ends[0].End.BridgeDeath {
		t.Fatalf("ends = %+v, want one death end", ends)
	}
	rows, _ := s.q.JobRows(s.chat, ends[0].Owner)
	if len(rows) != 2 || rows[0].Key != "steer-b" || !rows[0].InKAS || rows[1].Key != "steer-c" || rows[1].InKAS {
		t.Errorf("end rows = %+v, want steer-b in KAS then steer-c not", rows)
	}
	if s.channel() != chanNone || !s.recs.NeedsPostLoadClear(s.chat) {
		t.Errorf("channel = %d post-load %v, want NONE owing the clear", s.channel(), s.recs.NeedsPostLoadClear(s.chat))
	}
}

// A teardown landing while a delete's clear is in flight ends the op as chat_gone,
// and nothing about the chat is noted or broadcast after it.
func TestCmdSteerRemove_ATeardownUnderTheClearWritesNothing(t *testing.T) {
	s, _, br := steerOpsHub(t)
	s.bind()
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		s.h.BeginChatTeardown("c1", false)
		return clearReply([]string{"steer-a", "steer-b"}), nil, true
	}
	frames := len(s.spy.frames)

	rec := postCmd(t, s.h, removeCmd("steer-a"))

	if rec.Code != http.StatusConflict {
		t.Errorf("steer_remove = %d %s, want 409 chat_gone", rec.Code, rec.Body.String())
	}
	if len(s.spy.frames) != frames || s.spy.note("steer-a") != nil {
		t.Errorf("after the teardown: frames %d (was %d), note %+v; want none",
			len(s.spy.frames), frames, s.spy.note("steer-a"))
	}
}

// A pending end resolved after the chat's teardown finds no rows to send.
func TestSteerRecords_AnEndResolvedAfterTeardownFindsTheChatGone(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "a")
	s.endTurn()
	ends := s.q.Ends(s.chat)

	s.recs.BeginTeardown(s.chat, nil)

	if rows, gone := s.q.JobRows(s.chat, ends[0].Owner); !gone || len(rows) != 0 {
		t.Errorf("JobRows after teardown = %+v gone %v, want gone", rows, gone)
	}
}

// While KAS may still hold a dead buffer's copy, the drain does not send the row; only a landed clear releases it.
func TestPromptDrain_ADeadBuffersRowWaitsForThePostLoadClear(t *testing.T) {
	for _, tc := range []struct {
		setup     func(s *steerHarness, br *fakeBridge)
		name      string
		wantClear bool
		wantSteer bool
	}{
		{name: "the clear fails", wantClear: true, setup: func(_ *steerHarness, br *fakeBridge) {
			br.callErrs = map[string]error{marotte.MethodSessionSteerClear: errors.New("pipe closed")}
		}},
		{name: "an agent row blocks the clear", setup: func(s *steerHarness, _ *fakeBridge) {
			s.recs.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "notify-1", Origin: marotte.SteerOriginAgent})
		}},
		{name: "the clear lands", wantClear: true, wantSteer: true, setup: func(*steerHarness, *fakeBridge) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := newTestHub()
			defer shutdownHub(t, h)
			seedChat(t, cs, "c1")
			s := harnessOn(h, "c1", t.Fatalf)
			br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteer: json.RawMessage(`{"queued":true}`)}
			s.steer("steer-a", "first")
			s.recs.BridgeGone("c1")
			tc.setup(s, br)

			postCmd(t, h, marotte.ClientCommand{
				Type: marotte.CmdPrompt, ChatID: "c1",
				Payload: json.RawMessage(`{"text":"next","message_id":"m-1"}`),
			})
			waitForCall(t, br, marotte.MethodPrompt)

			calls := br.callLog()
			if got := slices.Contains(calls, marotte.MethodSessionSteerClear); got != tc.wantClear {
				t.Errorf("clear issued = %v, want %v (calls %v)", got, tc.wantClear, calls)
			}
			if got := slices.Contains(calls, marotte.MethodSessionSteer); got != tc.wantSteer {
				t.Errorf("steer sent = %v, want %v (calls %v)", got, tc.wantSteer, calls)
			}
			if tc.wantSteer {
				if msg := br.paramsFor(marotte.MethodSessionSteer)["message"]; msg != "first" {
					t.Errorf("steer message = %q, want the dead buffer's row", msg)
				}
				return
			}
			s.mustState("steer-a", rowUnsent)
		})
	}
}

// A turn StartTurn began during a NONE-state delete cannot read the buffer until the delete ends; from
// StartTurn on, a new delete is refused, read under the same lock.
func TestCmdSteerRemove_ATurnStartingUnderADeleteWaitsAndLaterDeletesAreRefused(t *testing.T) {
	s, _, br := steerOpsHub(t)
	for _, key := range []string{"steer-a", "steer-b"} {
		sends, _ := s.q.RouteSteer("c1", key, key, promptHolder)
		s.kas(sends)
	}
	gate := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodSessionSteerClear: gate}
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply([]string{"steer-a", "steer-b"}), nil, true
	}
	var wg sync.WaitGroup
	var code int
	var body []byte
	wg.Go(func() {
		rec := postCmd(t, s.h, removeCmd("steer-a"))
		code, body = rec.Code, rec.Body.Bytes()
	})
	waitForCall(t, br, marotte.MethodSessionSteerClear)

	postCmd(t, s.h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"next","message_id":"m-1"}`),
	})
	awaitLockWaiters(t, s.h.steerQueue, 2)

	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Fatal("the prompt reached KAS while the delete's clear was in flight")
	}
	late, _ := s.q.RouteSteer("c1", "steer-c", "c", promptHolder)
	s.kas(late)
	if _, refuse := s.q.BeginRemove("c1", "steer-c", "op-late"); refuse != command.SteerRefuseStarting {
		t.Errorf("a delete after StartTurn = %q, want starting", refuse)
	}

	close(gate)
	wg.Wait()
	waitForCall(t, br, marotte.MethodPrompt)

	if got := removedBody(t, code, body); got["deleted"] != "steer-a" {
		t.Errorf("the in-flight delete = %v, want steer-a deleted", got)
	}
	// Up to the prompt only.
	var order []string
	for _, m := range br.callLog() {
		switch m {
		case marotte.MethodSessionSteerClear, marotte.MethodSessionSteer, marotte.MethodPrompt:
			order = append(order, m)
		}
		if m == marotte.MethodPrompt {
			break
		}
	}
	want := []string{marotte.MethodSessionSteerClear, marotte.MethodSessionSteer, marotte.MethodPrompt}
	if !slices.Equal(order, want) {
		t.Errorf("wire = %v, want the delete's clear and resubmit before the prompt", order)
	}
}

// A resubmit failing after the clear still answers deleted; every kept row stays outstanding under the probe KAS may hold.
func TestCmdSteerRemove_AFailedResubmitKeepsEveryKeptRowInCustody(t *testing.T) {
	s, _, br := steerOpsHub(t)
	s.bind()
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	s.steer("steer-c", "c")
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply(s.kasHolds()), nil, true
	}
	br.setCallErr(marotte.MethodSessionSteer, errors.New("the bridge exited"))

	rec := postCmd(t, s.h, removeCmd("steer-a"))

	if got := removedBody(t, rec.Code, rec.Body.Bytes()); got["deleted"] != "steer-a" {
		t.Errorf("body = %v, want steer-a deleted", got)
	}
	if n := s.spy.note("steer-a"); n == nil || n.Reason != marotte.SteerReasonDeleted {
		t.Errorf("target entry = %+v, want deleted", n)
	}
	probe := s.probe()
	for _, key := range []string{"steer-b", "steer-c"} {
		s.mustState(key, rowOutstanding)
		if id := s.kasID(key); id != probe || probe == "" {
			t.Errorf("%s is held under %q, want the outstanding probe %q", key, id, probe)
		}
		if n := s.spy.note(key); n != nil {
			t.Errorf("kept row %s got entry %+v, want none: its words were not dropped", key, n)
		}
	}
	var listed []string
	for _, e := range s.recs.List("c1") {
		if p := e.Payload.(marotte.SteerQueuedPayload); len(p.Replaces) == 0 {
			listed = append(listed, p.SteerID)
		}
	}
	if !slices.Equal(listed, []string{"steer-b", "steer-c"}) {
		t.Errorf("a reconnecting dock lists %v, want steer-b and steer-c", listed)
	}
}
