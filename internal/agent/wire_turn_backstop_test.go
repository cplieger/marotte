package agent

// What closes a turn KAS started when its turn_end never comes: the cancel grace, and KAS reporting its session idle.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// idleWakeHub is wakeTurnHub on kasSteerHub.
func idleWakeHub(t *testing.T, d time.Duration) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := kasSteerHub(t, d)
	send(t, h, newTurnStartMsg())
	if _, open := ownID(h, "c1"); !open {
		t.Fatal("setup: a turn_start with nothing pending opened no wire turn")
	}
	return h, cs, br
}

// kasSteerHub is an idle chat with the idle-end grace at d and the bridge's forward loop running; KAS echoes each
// steer as steering_queued and answers a clear with every id it holds.
func kasSteerHub(t *testing.T, d time.Duration) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	s, cs, br := steerOpsHub(t)
	s.h.coord.idleEndGrace = d
	go s.h.coord.Forward("c1", br)
	br.onCall = func(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch method {
		case marotte.MethodSessionSteer:
			wireID, _ := p["messageId"].(string)
			id := marotte.SteerIDPrefix + wireID
			text, _ := p["message"].(string)
			raw, _ := json.Marshal(map[string]any{"queued": true, "messageId": id})
			return raw, []*marotte.RPCResponse{newSessionInfoMsg(map[string]any{
				"kind": "steering_queued", "messageId": id, "content": text,
			})}, true
		case marotte.MethodSessionSteerClear:
			return clearReply(s.kasHolds()), nil, true
		}
		return nil, nil, false
	}
	return s.h, cs, br
}

// newActivityMsg is KAS's focus_update carrying its own count of running turns.
func newActivityMsg(turnActive bool) *marotte.RPCResponse {
	status := "idle"
	if turnActive {
		status = "in_progress"
	}
	return newSessionInfoMsg(map[string]any{
		"kind": "focus_update", "focus": map[string]any{"status": status}, "status": status,
		"activity": map[string]any{"turnActive": turnActive, "runningWorkflows": 0, "pausedWorkflows": 0},
	})
}

func promptTexts(t *testing.T, cs *testChatStore) []string {
	t.Helper()
	var texts []string
	for _, o := range opensOf(t, logOf(t, cs, "c1")) {
		if o.Prompt != nil {
			texts = append(texts, o.Prompt.Text)
		}
	}
	return texts
}

func send(t *testing.T, h *Runtime, msgs ...*marotte.RPCResponse) {
	t.Helper()
	for _, m := range msgs {
		h.translateACPEvent("c1", h.originOf("c1"), m)
	}
}

// A cancel of an agent-initiated turn whose turn_end never comes closes it at the grace, so prompts admit again
// rather than steering into an idle session until the bridge dies.
func TestCmdCancel_AWireTurnWithNoTurnEndClosesAtTheGrace(t *testing.T) {
	setCancelGrace(t, 20*time.Millisecond)
	h, cs, br := wakeTurnHub(t)

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if len(br.notified(marotte.MethodCancel)) == 0 {
		t.Fatal("no session/cancel reached the bridge")
	}
	waitFor(t, func() bool { _, open := ownID(h, "c1"); return !open })

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close = %+v, want one cancelled close", closes)
	}
	got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", 60*time.Millisecond)
	if got == command.AdmissionAcquired {
		h.coord.ReleaseTurnReservation("c1")
	}
	if got != command.AdmissionAcquired {
		t.Errorf("ReserveTurnForPrompt after the grace = %v, want acquired", got)
	}
}

// The turn_end KAS sends for the cancel closes the turn first; the grace then leaves the next turn KAS starts open.
func TestCmdCancel_AWireTurnEndedInsideTheGraceLeavesTheNextTurnOpen(t *testing.T) {
	setCancelGrace(t, 20*time.Millisecond)
	h, cs, _ := wakeTurnHub(t)

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	send(t, h, newTurnEndMsg("cancelled"), newTurnStartMsg())
	next, open := ownID(h, "c1")
	if !open {
		t.Fatal("setup: the second turn_start opened no wire turn")
	}
	time.Sleep(60 * time.Millisecond) // The grace runs on a real timer.

	if id, open := ownID(h, "c1"); !open || id != next {
		t.Errorf("own turn = %q (open %t), want the next turn %q left open", id, open, next)
	}
	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 1 {
		t.Errorf("turn_close entries = %d, want 1: the wire's own", len(closes))
	}
}

// A frame with no turn open opens a wire turn no turn_end is owed for; Cancel beside it closes it at the grace, so
// a Steer sent beside it reaches KAS as the next prompt, once, rather than waiting in a buffer nothing reads.
func TestCmdCancel_AFoldOpenedWireTurnClosesAtTheGrace(t *testing.T) {
	setCancelGrace(t, 20*time.Millisecond)
	h, cs, br := kasSteerHub(t, time.Hour)
	send(t, h, newSessionChunkMsg("", "a stray frame"))
	if _, open := ownID(h, "c1"); !open {
		t.Fatal("setup: the frame opened no wire turn")
	}
	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSteer, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"also check the logs","message_id":"steer-a"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("steer = %d %s, want 200", rec.Code, rec.Body.String())
	}

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return slices.Contains(promptTexts(t, cs), "also check the logs") })
	joinInflight(t, h)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) == 0 || closes[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close = %+v, want the fold-opened turn closed cancelled first", closes)
	}
	if n := countCalls(br, marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d, want 1", n)
	}
}

// A turn KAS wrote into and then went idle without ending closes at the grace, saying why, and prompts admit again.
func TestKASIdle_ClosesAWireTurnWhoseEndNeverCame(t *testing.T) {
	h, cs, _ := idleWakeHub(t, 20*time.Millisecond)
	send(t, h, newSessionChunkMsg("", "reading the notification"), newActivityMsg(false))

	waitFor(t, func() bool { _, open := ownID(h, "c1"); return !open })

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeUnknown || closes[0].FailureReason != kasIdleReason {
		t.Errorf("turn_close = %+v, want one unknown close giving the idle reason", closes)
	}
	got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", 60*time.Millisecond)
	if got == command.AdmissionAcquired {
		h.coord.ReleaseTurnReservation("c1")
	}
	if got != command.AdmissionAcquired {
		t.Errorf("ReserveTurnForPrompt after the close = %v, want acquired", got)
	}
}

// A wake turn KAS still runs stays open past the grace however little it files: a retried model call and an
// auto-compaction send frames that file nothing. A Steer beside it reaches KAS's buffer and never a prompt.
func TestKASIdle_LeavesATurnKASStillRuns(t *testing.T) {
	h, cs, br := idleWakeHub(t, 20*time.Millisecond)
	send(t, h,
		newSessionInfoMsg(map[string]any{"kind": "display_error", "displayError": map[string]any{
			"message": "The model is busy. Retrying.", "errorType": "throttled",
		}}),
		newSessionInfoMsg(map[string]any{"kind": "summarization_started", "summarization": map[string]any{"status": "running"}}),
		newActivityMsg(true),
	)
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSteer, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"also check the logs","message_id":"steer-a"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("steer = %d %s, want 200", rec.Code, rec.Body.String())
	}

	time.Sleep(80 * time.Millisecond) // The idle close would run on a real timer.
	joinInflight(t, h)

	if _, open := ownID(h, "c1"); !open {
		t.Error("the turn closed while KAS still ran it, want it left open")
	}
	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 0 {
		t.Errorf("turn_close = %+v, want none", closes)
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 1 {
		t.Errorf("_session/steer calls = %d, want 1", n)
	}
	if n := countCalls(br, marotte.MethodPrompt); n != 0 {
		t.Errorf("session/prompt calls = %d, want 0: a prompt aborts the running turn", n)
	}
}

// KAS reporting a turn running again inside the grace stands the idle close down.
func TestKASIdle_ARunningReportInsideTheGraceKeepsTheTurn(t *testing.T) {
	h, cs, _ := idleWakeHub(t, 20*time.Millisecond)
	send(t, h, newActivityMsg(false), newActivityMsg(true))

	time.Sleep(60 * time.Millisecond) // The idle close runs on a real timer.

	if _, open := ownID(h, "c1"); !open {
		t.Error("the turn closed after KAS reported it running, want it left open")
	}
	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 0 {
		t.Errorf("turn_close = %+v, want none", closes)
	}
}

// The ordinary end: KAS reports idle, then sends the turn_end; the grace then leaves the next turn KAS starts open.
func TestKASIdle_ATurnEndInsideTheGraceLeavesTheNextTurnOpen(t *testing.T) {
	h, cs, _ := idleWakeHub(t, 20*time.Millisecond)
	send(t, h, newActivityMsg(false), newTurnEndMsg("end_turn"), newTurnStartMsg())
	next, open := ownID(h, "c1")
	if !open {
		t.Fatal("setup: the second turn_start opened no wire turn")
	}

	time.Sleep(60 * time.Millisecond) // The idle close runs on a real timer.

	if id, open := ownID(h, "c1"); !open || id != next {
		t.Errorf("own turn = %q (open %t), want the next turn %q left open", id, open, next)
	}
	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].StopReasonRaw != string(marotte.StopReasonEndTurn) {
		t.Errorf("turn_close = %+v, want the wire's own end_turn close only", closes)
	}
}

// A Steer-mode send beside a wake turn whose end is lost after KAS wrote into it reaches KAS's buffer, which nothing
// reads once KAS is idle; the idle close clears it and sends the words as the next prompt, once.
func TestKASIdle_SendsASteerSentBesideALostEndAsThePrompt(t *testing.T) {
	h, cs, br := idleWakeHub(t, 50*time.Millisecond)
	send(t, h, newSessionChunkMsg("", "reading the notification"))

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSteer, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"also check the logs","message_id":"steer-a"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("steer = %d %s, want 200", rec.Code, rec.Body.String())
	}
	send(t, h, newActivityMsg(false))
	waitFor(t, func() bool { return slices.Contains(promptTexts(t, cs), "also check the logs") })
	joinInflight(t, h)

	if got := promptTexts(t, cs); !slices.Equal(got, []string{"also check the logs"}) {
		t.Errorf("prompts opened = %q, want the steer's words once", got)
	}
	if n := countCalls(br, marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d, want 1", n)
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 1 {
		t.Errorf("_session/steer calls = %d, want 1", n)
	}
	// KAS merges whatever its buffer still holds into the next prompt, so the resend must follow a clear.
	calls := br.callLog()
	if c, p := slices.Index(calls, marotte.MethodSessionSteerClear), slices.Index(calls, marotte.MethodPrompt); c < 0 || c > p {
		t.Errorf("bridge calls = %v, want _session/steer/clear before the session/prompt", calls)
	}
}

// A Queue-mode send beside a wake turn whose end is lost after KAS wrote into it is queued behind it; the idle close
// sends it, once.
func TestKASIdle_SendsARowQueuedBesideALostEnd(t *testing.T) {
	h, cs, br := idleWakeHub(t, 50*time.Millisecond)
	send(t, h, newSessionChunkMsg("", "reading the notification"))

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdQueuePrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"then run the tests","message_id":"m-q1"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("queue_prompt = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 {
		t.Fatalf("queue = %+v, want the row held behind the open turn", rows)
	}
	send(t, h, newActivityMsg(false))
	waitFor(t, func() bool { return len(queuedRows(t, cs, "c1")) == 0 })
	joinInflight(t, h)

	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-q1"}) {
		t.Errorf("prompts opened = %v, want the queued row once", ids)
	}
	if n := countCalls(br, marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d, want 1", n)
	}
}

// lateEndStore delivers the idle-closed wake's turn_end once, in the window between the drained prompt's open and
// its session/prompt: the only window where a stale end can meet a prompt KAS has not yet read.
type lateEndStore struct {
	bridgeChatRecords
	h     *Runtime
	fired atomic.Bool
}

func (s *lateEndStore) WriteCounters(ctx context.Context, chatID marotte.ChatID) error {
	err := s.bridgeChatRecords.WriteCounters(ctx, chatID)
	if unboundPrompt(s.h, chatID) && s.fired.CompareAndSwap(false, true) {
		s.h.translateACPEvent(chatID, s.h.originOf(chatID), newTurnEndMsg("end_turn"))
	}
	return err
}

func unboundPrompt(h *Runtime, chatID marotte.ChatID) bool {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.pending != nil && !lc.pending.acked && lc.pending.Source.PromptClass()
}

// The lost end of a wake arriving after the idle close, while the Steer or Queue send it left behind is opening as
// the next prompt, closes nothing: the words reach KAS once and their turn ends on its own bracket.
func TestKASIdle_ALateEndLeavesTheDrainedPromptToReachKAS(t *testing.T) {
	cases := []struct {
		name string
		cmd  marotte.ClientCommand
		want string
	}{
		{"steer", marotte.ClientCommand{
			Type: marotte.CmdSteer, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"also check the logs","message_id":"steer-a"}`),
		}, "also check the logs"},
		{"queue", marotte.ClientCommand{
			Type: marotte.CmdQueuePrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"then run the tests","message_id":"m-q1"}`),
		}, "then run the tests"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := idleWakeHub(t, 50*time.Millisecond)
			late := &lateEndStore{bridgeChatRecords: h.coord.chatStore, h: h}
			h.coord.chatStore = late
			steer := br.onCall
			br.onCall = func(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
				if method != marotte.MethodPrompt {
					return steer(method, p)
				}
				return json.RawMessage(`{"stopReason":"end_turn"}`), []*marotte.RPCResponse{
					newTurnStartMsg(), newSessionChunkMsg("", "on it"), newTurnEndMsg("end_turn"),
				}, true
			}
			send(t, h, newSessionChunkMsg("", "reading the notification"))
			if rec := postCmd(t, h, tc.cmd); rec.Code != http.StatusOK {
				t.Fatalf("%s = %d %s, want 200", tc.cmd.Type, rec.Code, rec.Body.String())
			}

			send(t, h, newActivityMsg(false))
			waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, "c1"))) == 2 })
			joinInflight(t, h)

			if !late.fired.Load() {
				t.Fatal("setup: the late turn_end never met the drained prompt")
			}
			if n := countCalls(br, marotte.MethodPrompt); n != 1 {
				t.Errorf("session/prompt calls = %d, want 1: the words reach KAS once", n)
			}
			if got := promptTexts(t, cs); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("prompts opened = %q, want %q once", got, tc.want)
			}
			closes := closesOf(t, logOf(t, cs, "c1"))
			if closes[0].FailureReason != kasIdleReason || closes[1].StopReasonRaw != string(marotte.StopReasonEndTurn) {
				t.Errorf("turn_close = %+v, want the wake's idle close then the prompt's own end_turn", closes)
			}
			if rows := queuedRows(t, cs, "c1"); len(rows) != 0 {
				t.Errorf("queue = %+v, want empty", rows)
			}
		})
	}
}
