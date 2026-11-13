package agent

// The steer commands against the real record and turn registry, the fake holding an RPC open while another event lands.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
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

func steerOpsHub(t *testing.T) (*steerHarness, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteer: json.RawMessage(`{"queued":true}`)}
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	return harnessOn(h, "c1", t.Fatalf), cs, br
}

// userSteer sends the user's steer command for key's message id into c1's running turn, through the run list as the
// page does.
func (s *steerHarness) userSteer(t *testing.T, key, text string) {
	t.Helper()
	if code := postSteer(t, s.h, strings.TrimPrefix(key, marotte.SteerIDPrefix), text); code != http.StatusOK {
		t.Fatalf("Setup: steer %s = %d, want 200", key, code)
	}
}

func clearReply(ids []string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"cleared": true, "messageIds": ids})
	return raw
}

// A read emitted before the clear's reply that never folds leaves the clear unsettled: nothing is deleted,
// and the late read is the row's one entry.
func TestCmdSteerRemove_AReadThatCannotFoldBeforeTheBarrierDeletesNothing(t *testing.T) {
	s, _, br := steerOpsHub(t)
	att := s.h.coord.turns.attachForwardNow("c1")
	gen := att.gen
	// No goroutine stands behind this generation, so close its exit as forward would.
	t.Cleanup(att.exited)
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
	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg("steer-a", "first"))
	if _, live := s.state("steer-a"); live {
		t.Error("the stranded read, once folded, did not retire the row")
	}
}

// Two deletes take the lock in turn; the second re-reads, finds its target in the first's probe, clears again
// and resends the rest under a fresh probe.
func TestCmdSteerRemove_TwoDeletesInFlightTakeTurns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		// Every other goroutine is durably blocked: the second delete is parked on the steer lock.
		synctest.Wait()
		close(gate)
		wg.Wait()

		for i, key := range []string{"steer-a", "steer-b"} {
			if got := removedBody(t, answers[i].code, answers[i].body); got["deleted"] != key {
				t.Errorf("delete %s = %v, want deleted with its kept rows resent", key, got)
			}
			wantGoneUnnoted(t, s, key)
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
	})
}

// The same row deleted twice at once: the second finds nothing waiting.
func TestCmdSteerRemove_TheSameRowTwiceAnswersNotWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		// Every other goroutine is durably blocked: the second delete is parked on the steer lock.
		synctest.Wait()
		close(gate)
		wg.Wait()

		slices.Sort(codes)
		if !slices.Equal(codes, []int{http.StatusOK, http.StatusNotFound}) {
			t.Errorf("codes = %v, want one delete and one not_waiting", codes)
		}
	})
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
		{name: "a bridge death", stage: func(t *testing.T, s *steerHarness, br *fakeBridge) {
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			s.userSteer(t, "steer-a", "a")
			s.userSteer(t, "steer-b", "b")
			var once sync.Once
			s.h.bridge.mgr.remove("c1")
			s.h.insertBridgeNow("c1", testBridge(failingClear{fakeBridge: br, before: func() {
				once.Do(func() {
					s.recs.turnEnded("c1", command.SteerTurnEnd{
						TurnID: id, BridgeDeath: true, Exit: deathExit, Source: marotte.TurnSourcePrompt,
					})
				})
			}}, bridgeReady))
		}, closed: func(t *testing.T, s *steerHarness) {
			id, _ := ownID(s.h, "c1")
			s.h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
				Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: id,
			})
			s.h.coord.releaseTurn("c1", id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			tc.stage(t, s, br)

			rec := postCmd(t, s.h, removeCmd("steer-a"))

			if got := removedBody(t, rec.Code, rec.Body.Bytes()); got["deleted"] != "steer-a" {
				t.Errorf("body = %v, want steer-a deleted and the rest left to the turn end", got)
			}
			wantGoneUnnoted(t, s, "steer-a")
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
		s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
		s.userSteer(t, "steer-a", "a")
		s.userSteer(t, "steer-b", "b")
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method != marotte.MethodSessionSteerClear {
				return nil, nil, false
			}
			s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg("steer-a", "steer-b"))
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg(stop))
			s.h.coord.releaseTurn("c1", id)
			return clearReply(nil), nil, true
		}
	}
}

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
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.userSteer(t, "steer-a", "first")
	s.userSteer(t, "steer-b", "second")

	rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-b"}`)})
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if len(br.notified(marotte.MethodCancel)) == 0 {
		t.Fatal("no session/cancel reached the bridge")
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg("steer-a", "steer-b"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)

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

// A send-now press whose stop stops nothing (the turn it was drawn against is gone) leaves no lead behind: the next
// plain stop pauses both unread steers in the order they were written and opens no turn, as it would with no press.
func TestCmdCancel_ALeadWhoseStopStopsNothingIsNotKept(t *testing.T) {
	s, cs, _ := steerOpsHub(t)
	s.recs.note = func(ctx context.Context, chatID marotte.ChatID, id string, steer *marotte.EntrySteer) {
		s.h.coord.recordSteer(ctx, chatID, id, steer)
	}
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.userSteer(t, "steer-a", "first")
	s.userSteer(t, "steer-b", "second")

	rec := postCmd(t, s.h, marotte.ClientCommand{
		Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-b","target":"t-gone"}`),
	})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"stopped":true`) {
		t.Fatalf("cancel against a gone turn = %d %s, want 200 stopping nothing", rec.Code, rec.Body.String())
	}
	rec = postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d %s, want 200", rec.Code, rec.Body.String())
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg("steer-a", "steer-b"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)

	waitFor(t, func() bool {
		return stateOf(s.h, "c1", "a") == marotte.RunPaused && stateOf(s.h, "c1", "b") == marotte.RunPaused
	})
	joinInflight(t, s.h)
	if got := listedRows(s.h, "c1"); !slices.Equal(got, []string{"a:paused", "b:paused"}) {
		t.Errorf("waiting rows = %v, want both steers paused in the order written", got)
	}
	if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
		t.Errorf("turns opened = %d, want only the stopped one", n)
	}
}

// The reader pressed send now on a steer as its turn closed on its own, and that close already sent the steer as
// the next prompt: the stop arriving now would cancel the very turn the reader asked for.
func TestCmdCancel_ALeadWhoseResendOpenedBeforeTheStopIsNotCancelled(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.userSteer(t, "steer-a", "first")
	s.userSteer(t, "steer-b", "second")
	resendRuns := make(chan struct{})
	t.Cleanup(func() { close(resendRuns) })
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: resendRuns}
	br.mu.Unlock()
	s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg("steer-a", "steer-b"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	if p := resendPrompt(t, cs); p == nil || !slices.Contains(p.Resends, "steer-b") {
		t.Fatalf("Setup: resend prompt = %+v, want the close to send steer-b", p)
	}
	waitFor(t, func() bool {
		_, live := s.state("steer-b")
		return !live
	})

	rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-b"}`)})

	if rec.Code != http.StatusOK {
		t.Errorf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := br.notified(marotte.MethodCancel); len(got) != 0 {
		t.Errorf("session/cancel sent %v, want the resend carrying steer-b left running", got)
	}
	for _, e := range logOf(t, cs, "c1") {
		var open marotte.EntryTurnOpen
		if e.Kind == marotte.EntryKindTurnOpen {
			decodePayload(t, e, &open)
		}
		if open.Prompt != nil && len(open.Prompt.Resends) > 0 && s.h.coord.stopRequestedAfter("c1", e.Turn) {
			t.Errorf("the resend's turn %s is marked stopped, want it free to send and retry", e.Turn)
		}
	}
}

// A delete resends the kept rows as one steer under a fresh id; read after the ledger has forgotten that id
// (a turn outlives its TTL), the read entry still names both rows, so a later session/load merge inserts no
// "Not read" note for either.
func TestSteerRead_ACombinedResendReadAfterTheLedgerForgotItNamesItsRows(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
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

	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg(probe, "a\n\nc"))

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
	merged, _ := mergeEntries(recordTurnsOf(logOf(t, cs, "c1"), nil), projected, "sid-1")
	for _, turn := range merged {
		for _, e := range turn.Entries {
			if e.ID == "steer-a" || e.ID == "steer-c" {
				t.Errorf("merged turn %s holds steer %s %s, want none: the read resend carried it", e.Turn, e.ID, e.Payload)
			}
		}
	}
}

// Between a turn_end and KAS going idle, a row KAS still holds is the continuation's to read: it reads
// delivering, and a delete is refused with a clear-free 409 that says so rather than claiming a resend.
func TestCmdSteerRemove_ARowKASIsDeliveringIsRefusedWithNoClear(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, br := turnEndedBesideASteer(t)

		rec := postCmd(t, s.h, removeCmd("steer-a"))

		if rec.Code != http.StatusConflict {
			t.Fatalf("steer_remove = %d %s, want 409", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "reading or dropping") || strings.Contains(body, "resent") {
			t.Errorf("refusal = %s, want it to say the agent is reading or dropping the message, with no resend claimed", body)
		}
		if n := countCalls(br, marotte.MethodSessionSteerClear); n != 0 {
			t.Errorf("_session/steer/clear calls = %d, want 0", n)
		}
		if f := s.lastFrame("steer-a"); f.State != marotte.SteerRowState("delivering") {
			t.Errorf("steer-a's last frame state = %q, want delivering", f.State)
		}
	})
}

// A bridge death with a probe outstanding and a steer waiting collects both for one resend, marks the
// outstanding one possibly still in KAS's log, and owes the post-load clear.
func TestSteerRecords_ABridgeDeathCollectsOutstandingAndWaitingRows(t *testing.T) {
	s := newSteerHarness(t)
	probeSent(s)
	s.q.RouteSteer(s.chat, "steer-c", "c", nil, promptHolder)
	s.mustState("steer-c", rowWaiting)

	s.recs.turnEnded(s.chat, command.SteerTurnEnd{TurnID: s.turnID(), BridgeDeath: true, Source: marotte.TurnSourcePrompt})

	ends := s.q.Ends(s.chat)
	if len(ends) != 1 || !ends[0].End.BridgeDeath {
		t.Fatalf("ends = %+v, want one death end", ends)
	}
	rows, _ := s.q.JobRows(s.chat, ends[0].Owner)
	if len(rows) != 2 || rows[0].Key != "steer-b" || !rows[0].InKAS || rows[1].Key != "steer-c" || rows[1].InKAS {
		t.Errorf("end rows = %+v, want steer-b in KAS then steer-c not", rows)
	}
	if s.channel() != chanNone || !s.recs.needsPostLoadClear(s.chat) {
		t.Errorf("channel = %d post-load %v, want NONE owing the clear", s.channel(), s.recs.needsPostLoadClear(s.chat))
	}
}

// A teardown landing while a delete's clear is in flight ends the op as chat_gone,
// and nothing about the chat is noted or broadcast after it.
func TestCmdSteerRemove_ATeardownUnderTheClearWritesNothing(t *testing.T) {
	shortenSteerLockWait(t)
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

	s.recs.beginTeardown(s.chat, nil)

	if rows, gone := s.q.JobRows(s.chat, ends[0].Owner); !gone || len(rows) != 0 {
		t.Errorf("JobRows after teardown = %+v gone %v, want gone", rows, gone)
	}
}

// A user's steer kiro-cli held when its bridge died without draining waits for the next prompt: while kiro-cli's log
// may re-inject it, nothing sends it; once that prompt's post-load clear lands, it is steered into that prompt's turn.
func TestPromptDrain_ADeadBuffersRowWaitsForThePostLoadClear(t *testing.T) {
	never := make(chan struct{})
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
			shortenSteerLockWait(t)
			s, _, br := steerOpsHub(t)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			s.userSteer(t, "steer-a", "first")
			if !reachesState(s.h, "c1", "a", marotte.RunInKAS) {
				t.Fatalf("Setup: the steer is %q, want in kiro-cli's buffer", stateOf(s.h, "c1", "a"))
			}
			s.recs.turnEnded("c1", command.SteerTurnEnd{
				TurnID: id, BridgeDeath: true, Exit: never, Source: marotte.TurnSourcePrompt,
			})
			s.h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
				Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: id,
			})
			s.h.coord.releaseTurn("c1", id)
			idleAfter(t, s.h, "c1", id)
			tc.setup(s, br)
			br.forgetCalls()
			gate := make(chan struct{})
			t.Cleanup(func() { close(gate) })
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

			postCmd(t, s.h, marotte.ClientCommand{
				Type: marotte.CmdPrompt, ChatID: "c1",
				Payload: json.RawMessage(`{"text":"next","message_id":"m-1"}`),
			})
			waitForCall(t, br, marotte.MethodPrompt)
			next, _ := ownID(s.h, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(next))
			// A steer goes, if at all, once the list applied the clear's outcome to the prompted turn.
			within := 300 * time.Millisecond
			if tc.wantSteer {
				within = 5 * time.Second
			}
			sent := false
			for deadline := time.Now().Add(within); !sent && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				sent = slices.Contains(br.callLog(), marotte.MethodSessionSteer)
			}
			if sent != tc.wantSteer {
				t.Errorf("steer sent = %v, want %v (calls %v)", sent, tc.wantSteer, br.callLog())
			}

			calls := br.callLog()
			if got := slices.Contains(calls, marotte.MethodSessionSteerClear); got != tc.wantClear {
				t.Errorf("clear issued = %v, want %v (calls %v)", got, tc.wantClear, calls)
			}
			if tc.wantSteer {
				if msg := br.paramsFor(marotte.MethodSessionSteer)["message"]; msg != "first" {
					t.Errorf("steer message = %q, want the dead buffer's row", msg)
				}
				return
			}
			if got := stateOf(s.h, "c1", "a"); got != marotte.RunInKAS {
				t.Errorf("the steer is %q, want still in kiro-cli's custody", got)
			}
		})
	}
}

// A turn StartTurn began during a NONE-state delete cannot read the buffer until the delete ends.
func TestCmdSteerRemove_ATurnStartingUnderADeleteWaitsForIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, br := steerOpsHub(t)
		for _, key := range []string{"steer-a", "steer-b"} {
			sends, _ := s.q.RouteSteer("c1", key, key, nil, promptHolder)
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
		// Every other goroutine is durably blocked: the second delete is parked on the steer lock.
		synctest.Wait()

		if slices.Contains(br.callLog(), marotte.MethodPrompt) {
			t.Fatal("the prompt reached KAS while the delete's clear was in flight")
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
	})
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
	wantGoneUnnoted(t, s, "steer-a")
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
	for _, e := range s.recs.list("c1") {
		if p := e.Payload.(marotte.SteerQueuedPayload); len(p.Replaces) == 0 {
			listed = append(listed, p.SteerID)
		}
	}
	if !slices.Equal(listed, []string{"steer-b", "steer-c"}) {
		t.Errorf("a reconnecting dock lists %v, want steer-b and steer-c", listed)
	}
}

// The recorded delete end to end, with KAS's queued report arriving only after it: deleting the user's steer clears it
// from KAS's buffer and answers deleted instead of "starting to read", and the late report changes nothing.
func TestCmdSteerRemove_ASteerOfAStarterKASHasNotReportedIsDeleted(t *testing.T) {
	s, _, br := steerOpsHub(t)
	s.h.stagePromptTurn(t, "c1")
	sends, _ := s.q.RouteSteer("c1", "steer-late", "?", nil, promptHolder)
	s.kas(sends)
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteerClear {
			return nil, nil, false
		}
		return clearReply([]string{s.kasID("steer-late")}), nil, true
	}
	br.mu.Unlock()

	rec := postCmd(t, s.h, removeCmd("steer-late"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newSessionInfoMsg(map[string]any{
		"kind": "queued", "activeSessionId": "sess-holder",
	}))

	if got := removedBody(t, rec.Code, rec.Body.Bytes()); got["deleted"] != "steer-late" {
		t.Errorf("steer_remove = %v, want the steer deleted", got)
	}
	if !slices.Contains(br.callLog(), marotte.MethodSessionSteerClear) {
		t.Error("the delete issued no clear, so KAS's buffer still holds the steer")
	}
	if st, live := s.state("steer-late"); live {
		t.Errorf("steer-late is still on the dock (%d) after the delete and the late report", st)
	}
}

// The same recorded moment as the reader sees it: a turn whose execution KAS queued shows as waiting, never as a
// silent turn, and its steer as sent, never as read.
func TestQueuedReport_AQueuedStarterShowsItsWaitAndLeavesTheSteerSent(t *testing.T) {
	s, _, _ := steerOpsHub(t)
	s.h.stagePromptTurn(t, "c1")
	sends, _ := s.q.RouteSteer("c1", "steer-late", "?", nil, promptHolder)
	s.kas(sends)

	s.h.translateACPEvent("c1", s.h.originOf("c1"), newSessionInfoMsg(map[string]any{
		"kind": "queued", "activeSessionId": "sess-holder",
	}))

	labels := eventsOfType(s.h, marotte.EventWorkingLabel)
	if len(labels) == 0 || !strings.Contains(string(labels[len(labels)-1].Payload), "Waiting for another kiro-cli session") {
		t.Errorf("working_label events = %+v, want the turn shown waiting for the slot", labels)
	}
	s.mustState("steer-late", rowQueued)
}

// Send now on a queued row stops the turn, and the stop's close opens that row's turn ahead of the unread
// steers and the other queued rows. The steers are not lost: they reach the agent no earlier than that turn.
func TestSendQueuedNow_StopsTheTurnAndItsCloseSendsThatRowFirst(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.recs.note = func(ctx context.Context, chatID marotte.ChatID, id string, steer *marotte.EntrySteer) {
		s.h.coord.recordSteer(ctx, chatID, id, steer)
	}
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.userSteer(t, "steer-a", "use tabs")
	seedQueue(t, cs, "c1",
		marotte.QueuedPrompt{ID: "m-q1", Text: "first"},
		marotte.QueuedPrompt{ID: "m-q2", Text: "second"},
		marotte.QueuedPrompt{ID: "m-q3", Text: "third"},
	)

	rec := postCmd(t, s.h, marotte.ClientCommand{
		Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q3"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send_queued_now = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if len(br.notified(marotte.MethodCancel)) == 0 {
		t.Fatal("no session/cancel reached the bridge")
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg("steer-a"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)

	resend := resendPrompt(t, cs)
	if resend == nil || resend.Text != "use tabs" {
		t.Fatalf("resend prompt = %+v, want the unread steer still sent", resend)
	}
	ids := promptIDs(t, cs, "c1")
	if q3, r := slices.Index(ids, "m-q3"), slices.Index(ids, resend.ID); q3 < 0 || q3 > r {
		t.Errorf("turns opened = %v, want m-q3 before the steer's resend %s", ids, resend.ID)
	}
	waitFor(t, func() bool {
		rows := queuedRows(t, cs, "c1")
		return len(rows) == 2 && rows[0].ID == "m-q1" && rows[1].ID == "m-q2"
	})
}

// The unread steer does not ride into the marked row's turn: kiro-cli gets that row's prompt with no steer ahead of it,
// then the steer's words as the next prompt, so the reader's send-now message is the next thing the agent reads.
func TestSendQueuedNow_TheUnreadSteerWaitsForTheTurnAfterTheMarkedRow(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.recs.note = func(ctx context.Context, chatID marotte.ChatID, id string, steer *marotte.EntrySteer) {
		s.h.coord.recordSteer(ctx, chatID, id, steer)
	}
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.userSteer(t, "steer-a", "use tabs")
	seedQueue(t, cs, "c1",
		marotte.QueuedPrompt{ID: "m-q1", Text: "first"},
		marotte.QueuedPrompt{ID: "m-q3", Text: "third"},
	)
	var mu sync.Mutex
	var prompted []string
	br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodPrompt {
			return nil, nil, false
		}
		s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(promptTagOf(params)))
		s.h.translateACPEvent("c1", s.h.originOf("c1"), newChunkMsg("done"))
		mu.Lock()
		prompted = append(prompted, kasReplayedUserText(t, params))
		mu.Unlock()
		return json.RawMessage(`{"stopReason":"end_turn"}`), nil, true
	}
	before := len(br.callLog())

	rec := postCmd(t, s.h, marotte.ClientCommand{
		Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q3"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send_queued_now = %d %s, want 200", rec.Code, rec.Body.String())
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), clearedMsg("steer-a"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompted) >= 2
	})

	if calls := br.callLog()[before:]; len(calls) < 2 || calls[0] != marotte.MethodPrompt || calls[1] != marotte.MethodPrompt {
		t.Errorf("wire after the press = %v, want m-q3's prompt then the steer's, with no steer between", calls)
	}
	mu.Lock()
	got := slices.Clone(prompted[:2])
	mu.Unlock()
	if !slices.Equal(got, []string{"third", "use tabs"}) {
		t.Errorf("prompts sent = %q, want the send-now row's words, then the unread steer's", got)
	}
}
