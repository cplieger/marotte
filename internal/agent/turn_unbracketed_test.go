package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

func endTurnResponse() *marotte.RPCResponse {
	return &marotte.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
}

// The recorded shape of an unstreamed prompt turn: KAS ran the prompt (it launched a workflow) but its session adapter owned another
// execution, so no turn_start, chunk or tool call reached marotte before KAS answered the prompt end_turn. The record
// says KAS sent none of the run, never that the agent answered with nothing.
func TestPromptTurn_AnUnbracketedSilentEndIsNotGradedEmpty(t *testing.T) {
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	turnID, _ := h.stagePromptTurn(t, "c1")

	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, endTurnResponse())

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %+v, want one", closes)
	}
	c := closes[0]
	if c.Outcome != marotte.TurnOutcomeUnknown || c.StopReasonRaw != string(marotte.StopReasonEndTurn) ||
		c.FailureReason != marotte.UnobservedRunReason {
		t.Errorf("turn_close = %+v, want outcome unknown, raw end_turn and the unobserved-run reason", c)
	}
}

// The contrast: a bracketed turn KAS ended with nothing is the agent's empty answer.
func TestPromptTurn_ABracketedSilentEndIsGradedEmpty(t *testing.T) {
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{Tag: turnID})

	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, endTurnResponse())

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeEmpty {
		t.Errorf("turn_close entries = %+v, want one graded empty", closes)
	}
}

// countDrains counts the close tails that ended, after which the run list takes what runs next.
func countDrains(h *Runtime) *atomic.Int32 {
	var drained atomic.Int32
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if ended {
			drained.Add(1)
		}
	})
	return &drained
}

// graceClose closes the turn as the cancel grace's expiry does on the prompt-failure path.
func graceClose(t *testing.T, h *Runtime, turnID string) {
	t.Helper()
	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerPromptFailure, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})
}

// gateFirstClose parks the first close's publish until release closes, before its post-close pipeline starts, so
// that pipeline's wait for KAS begins only after the test moved on (its closer then runs on its own goroutine).
func gateFirstClose(h *Runtime) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	closed := h.coord.onTurnClosed
	h.coord.onTurnClosed = func(chatID marotte.ChatID, turnID string) {
		// Not a sync.Once: a second caller parked on its mutex is not durably blocked, so synctest.Wait would hang.
		if blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		if closed != nil {
			closed(chatID, turnID)
		}
	}
	return entered, release
}

// The recorded shape of the next turn's start: the cancel's grace closed the stopped turn while KAS still ran executions for the
// session (its re-wake of the cleared notifications held it busy), and marotte sent the carried steer 6 ms later,
// which KAS queued beside its re-wake and orphaned. The drain waits until KAS reports the session idle.
func TestAfterTurnClose_AGraceClosedPromptsDrainWaitsForKASToGoIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)

		graceClose(t, h, turnID)
		synctest.Wait()
		if n := drained.Load(); n != 0 {
			t.Fatalf("drained %d times while KAS still reported the session busy, want none", n)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times once KAS reported the session idle, want one", n)
		}
	})
}

// The contrast: a grace close while KAS last reported the session idle drains at once.
func TestAfterTurnClose_AGraceClosedPromptDrainsAtOnceWhenKASIsIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		h.coord.WireTurnActivity("c1", false)

		graceClose(t, h, turnID)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times, want one", n)
		}
	})
}

// Elapsed time is no proof KAS ended its executions: past the stuck-hold warning the drain still waits, and so do
// the chat's prompts, until KAS reports the session idle.
func TestAfterTurnClose_AGraceClosedPromptsDrainNeverFiresOnElapsedTime(t *testing.T) {
	logs := captureLogs(t)
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)

		graceClose(t, h, turnID)
		time.Sleep(kasHoldWarnAfter + time.Hour)
		synctest.Wait()
		if n := drained.Load(); n != 0 {
			t.Fatalf("drained %d times with KAS still reporting the session busy, want none", n)
		}
		if h.coord.tryReserveNow("c1", marotte.TurnSourcePrompt) {
			t.Fatal("tryReserve acquired with KAS still reporting the session busy, want refused")
		}
		if !strings.Contains(logs.String(), "the chat's prompts wait for it") {
			t.Errorf("logs = %s, want the stuck hold logged", logs.String())
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times once KAS reported the session idle, want one", n)
		}
	})
}

// The bridge's forward ending means no execution of that session can still stream, so the hold ends and the drain runs.
func TestAfterTurnClose_TheForwardExitingEndsAGraceClosedPromptsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()

		lc := h.coord.turns.testLifecycle("c1")
		lc.mu.Lock()
		gen := lc.fwdGen
		lc.mu.Unlock()
		h.coord.turns.sealPosition("c1", gen)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times after the forward exited, want one", n)
		}
	})
}

// A teardown re-fences the chat: its session is going, so the hold ends with no drain. KAS's last report still
// stands until the session's forward exits, and then the next session admits.
func TestAfterTurnClose_ATeardownEndsAGraceClosedPromptsHoldWithoutADrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()

		h.coord.turns.refence("c1")
		synctest.Wait()
		if n := drained.Load(); n != 0 {
			t.Errorf("drained %d times after a teardown re-fenced the chat, want none", n)
		}
		lc := h.coord.turns.testLifecycle("c1")
		lc.mu.Lock()
		state, gen := lc.kas.state, lc.fwdGen
		lc.mu.Unlock()
		if state != kasRunning {
			t.Errorf("KAS state after the teardown = %v, want running with the hold ended", state)
		}

		h.coord.turns.sealPosition("c1", gen)
		if !h.coord.tryReserveNow("c1", marotte.TurnSourcePrompt) {
			t.Error("tryReserve refused once the session's forward exited, want admitted")
		}
	})
}

// A model switch and a compaction are session calls too, so they wait for KAS to go idle like the drain does.
func TestAfterTurnClose_AGraceClosedPromptsModelSwitchWaitsForKASToGoIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		var applied atomic.Int32
		h.coord.applyPendingModel = func(context.Context, marotte.ChatID) { applied.Add(1) }
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)

		graceClose(t, h, turnID)
		synctest.Wait()
		if n := applied.Load(); n != 0 {
			t.Fatalf("applied the pending model %d times while KAS still reported the session busy, want none", n)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if n := applied.Load(); n != 1 {
			t.Errorf("applied the pending model %d times once KAS reported the session idle, want one", n)
		}
	})
}

// Shutdown ends the wait at once: the close pipeline runs on inflight, which Shutdown joins.
func TestAfterTurnClose_ShutdownEndsAGraceClosedPromptsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()

		start := time.Now()
		shutdownHub(t, h)
		if waited := time.Since(start); waited >= time.Minute {
			t.Errorf("shutdown waited %v for the drain's KAS-idle wait, want it ended at once", waited)
		}
		if n := drained.Load(); n != 0 {
			t.Errorf("drained %d times across shutdown, want none", n)
		}
	})
}

// A wire turn KAS opens (its re-wake) ends the wait without a drain: that turn holds admission, and its own close drains.
func TestAfterTurnClose_AGraceClosedPromptsWaitEndsAtTheNextWireTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)

		h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{})
		synctest.Wait()
		if n := drained.Load(); n != 0 {
			t.Fatalf("drained %d times beside KAS's own turn, want none until that turn closes", n)
		}

		// KAS's own order: the idle report, then the turn_end.
		h.coord.WireTurnActivity("c1", false)
		h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times after KAS's turn closed, want one", n)
		}
	})
}

// The recorded shape again, with the user's own prompt in the gap: sent right after the grace closed the stopped turn
// while KAS still ran the session, it answers at once as queued and reaches kiro-cli only once KAS reports the session
// idle, exactly once, as written.
func TestCmdPrompt_AfterAGraceCloseQueuesUntilKASGoesIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()

		var prompted []string
		br.mu.Lock()
		br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method == marotte.MethodPrompt {
				b, _ := json.Marshal(params[marotte.KeyPrompt])
				prompted = append(prompted, string(b))
			}
			return nil, nil, false
		}
		br.mu.Unlock()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- postCmd(t, h, marotte.ClientCommand{
				Type: marotte.CmdPrompt, ChatID: "c1",
				Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-2"}`),
			})
		}()
		rec := <-done
		wantQueuedAck(t, rec, "m-2")
		time.Sleep(time.Second)
		synctest.Wait()
		if n := countCalls(br, marotte.MethodPrompt); n != 0 {
			t.Fatalf("session/prompt calls = %d while KAS still reported the session busy, want none", n)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if n := countCalls(br, marotte.MethodPrompt); n != 1 {
			t.Errorf("session/prompt calls = %d, want exactly one", n)
		}
		br.mu.Lock()
		got := slices.Clone(prompted)
		br.mu.Unlock()
		if len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v, want the user's text once", got)
		}
		if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-c1", "m-2"}) {
			t.Errorf("turn_open prompt ids = %v, want [m-c1 m-2]", ids)
		}
	})
}

// A prompt KAS stays busy past the admission wait is refused as starting, which hands its text back, never as the
// plain busy the client would steer into a turn that is not open.
func TestReserveTurnForPrompt_AKASHoldAnswersStartingAtTheBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()

		if got := h.coord.reserveForPromptNow(t.Context(), "c1", command.AdmissionWait); got != admissionStarting {
			t.Errorf("ReserveTurnForPrompt during a KAS hold = %v, want AdmissionStarting", got)
		}
		if h.coord.tryReserveNow("c1", marotte.TurnSourceRevert) {
			t.Error("tryReserve(revert) during a KAS hold acquired, want refused")
		}
	})
}

// promptCalls records each session/prompt's prompt blocks.
func promptCalls(br *fakeBridge) func() []string {
	var prompted []string
	br.mu.Lock()
	br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodPrompt {
			b, _ := json.Marshal(params[marotte.KeyPrompt])
			prompted = append(prompted, string(b))
		}
		return nil, nil, false
	}
	br.mu.Unlock()
	return func() []string {
		br.mu.Lock()
		defer br.mu.Unlock()
		return slices.Clone(prompted)
	}
}

// The cancel grace closing a turn KAS started frees bracketedWireLocked at the claim, and closing it takes durable
// writes and broadcasts: a prompt sent in that window queues until KAS goes idle like one sent after the close.
func TestCmdPrompt_DuringAWireTurnsGraceCloseQueuesUntilKASGoesIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		seedChat(t, cs, "c1")
		h.coord.turns.ensureTestLife("c1")
		h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{})
		h.coord.WireTurnActivity("c1", true)
		entered, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		turnEnded := h.coord.steerTurnEnded
		h.coord.steerTurnEnded = func(chatID marotte.ChatID, end command.SteerTurnEnd) {
			if blocked.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			turnEnded(chatID, end)
		}
		armWireStopGrace(t, h, command.CancelGrace)
		time.Sleep(command.CancelGrace)
		<-entered
		prompted := promptCalls(br)

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- postCmd(t, h, marotte.ClientCommand{
				Type: marotte.CmdPrompt, ChatID: "c1",
				Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-2"}`),
			})
		}()
		synctest.Wait()
		close(release)
		synctest.Wait()
		if got := prompted(); len(got) != 0 {
			t.Fatalf("session/prompt blocks = %v while KAS still reported the session busy, want none", got)
		}
		wantQueuedAck(t, <-done, "m-2")

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v, want the user's text once", got)
		}
	})
}

// The recorded shape sent in Queue mode: a follow-up queued once the user's stop closed the turn, while KAS
// still ran it, is held off the session until KAS reports idle and then sent exactly once. A row queued before the
// stop waits as after any stop.
func TestCmdQueuePrompt_DuringAKASHoldIsSentOnceKASGoesIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		seedChat(t, cs, "c1")
		runPrompt(t, h, cs, "c1", "m-first")
		turnID, _ := h.stagePromptTurn(t, "c1")
		if rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdQueuePrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"queued before the stop","message_id":"m-q0"}`),
		}); rec.Code != http.StatusOK {
			t.Fatalf("Setup: queue_prompt on the live turn = %d %s", rec.Code, rec.Body.String())
		}
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()
		prompted := promptCalls(br)

		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdQueuePrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-q1"}`),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("queue_prompt during a KAS hold = %d %s, want 200", rec.Code, rec.Body.String())
		}
		// The dock lists the queue in the order it is sent: the held follow-up goes before the stopped one.
		if ids, _ := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-q1", "m-q0"}) {
			t.Errorf("queue during the hold = %v, want [m-q1 m-q0]", ids)
		}
		synctest.Wait()
		if got := prompted(); len(got) != 0 {
			t.Fatalf("session/prompt blocks = %v while KAS still reported the session busy, want none", got)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v once KAS went idle, want the held follow-up once", got)
		}
		if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-q0" {
			t.Errorf("queue = %+v, want only m-q0, queued before the stop, left", rows)
		}
	})
}

// A tangent's findings for a parent under a KAS hold are queued, never failed, and taken once KAS goes idle, as a row
// the stop does not hold back.
func TestMergeTangent_TheFindingsAreTakenOnceKASGoesIdle(t *testing.T) {
	h, cs, calls := mergeHub(t)
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnActivity("c1", true)
	graceClose(t, h, turnID)

	if rec := postCmd(t, h, mergeCmd("c-tangent")); rec.Code != http.StatusOK {
		t.Fatalf("merge_tangent = %d %s, want 200", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool {
		c, _ := cs.Get(t.Context(), "c-tangent")
		return len(c.TangentMerges) == 1 && c.TangentMerges[0].State != marotte.TangentMergeRunning
	})
	if c, _ := cs.Get(t.Context(), "c-tangent"); c.TangentMerges[0].State != marotte.TangentMergeSucceeded {
		t.Errorf("merge state = %q, want succeeded", c.TangentMerges[0].State)
	}
	rows := queuedRows(t, cs, "c1")
	if len(rows) != 1 || !strings.Contains(rows[0].Row.Text, "Found it.") || rows[0].State != marotte.RunPending {
		t.Fatalf("parent's queue = %+v, want the findings pending during the hold", rows)
	}
	if n := calls(marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d during the hold, want the tangent's summary only", n)
	}

	// The parent has no session in this hub, so the take is the observable: its turn opens once KAS goes idle.
	h.coord.WireTurnActivity("c1", false)
	waitFor(t, func() bool {
		c, _ := cs.Get(t.Context(), "c1")
		return len(c.Run) == 1 && c.Run[0].ID == rows[0].ID && c.Run[0].Turn != ""
	})
}

// KAS's report of the cancelled run can still be in the read loop when the grace claims the turn: a run reported
// during the close holds a prompt queued like one reported before it, until KAS reports idle.
func TestCmdPrompt_ARunReportedDuringAGraceCloseHoldsThePromptQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		seedChat(t, cs, "c1")
		h.coord.turns.ensureTestLife("c1")
		h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{})
		entered, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		turnEnded := h.coord.steerTurnEnded
		h.coord.steerTurnEnded = func(chatID marotte.ChatID, end command.SteerTurnEnd) {
			if blocked.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			turnEnded(chatID, end)
		}
		armWireStopGrace(t, h, command.CancelGrace)
		time.Sleep(command.CancelGrace)
		<-entered
		prompted := promptCalls(br)

		h.coord.WireTurnActivity("c1", true)
		// Errorf: the close is parked on release, which a Fatal here would leave blocked.
		if h.coord.tryReserveNow("c1", marotte.TurnSourcePrompt) {
			h.coord.releaseHeld("c1")
			t.Error("tryReserve acquired after KAS reported a run during the grace close, want refused")
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- postCmd(t, h, marotte.ClientCommand{
				Type: marotte.CmdPrompt, ChatID: "c1",
				Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-2"}`),
			})
		}()
		close(release)
		synctest.Wait()
		if got := prompted(); len(got) != 0 || drained.Load() != 0 {
			t.Fatalf("session/prompt blocks = %v, tail ends = %d while KAS reported a run, want none",
				got, drained.Load())
		}
		wantQueuedAck(t, <-done, "m-2")

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v, want the user's text once", got)
		}
	})
}

// heldPrompt stages the recorded hold (a grace close while KAS runs) with the user's prompt parked behind it, and
// answers the prompt's reply and the order of session calls.
func heldPrompt(t *testing.T, h *Runtime, br *fakeBridge, record func(string)) chan *httptest.ResponseRecorder {
	t.Helper()
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnActivity("c1", true)
	graceClose(t, h, turnID)
	synctest.Wait()
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodPrompt {
			record("prompt")
		}
		return nil, nil, false
	}
	br.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-2"}`),
		})
	}()
	synctest.Wait()
	return done
}

// A prompt waiting out the hold must not reach the session before the model switch parked during it lands: it would
// run on the old model, and the switch would stay parked behind it.
func TestAfterTurnClose_AWaitingPromptFollowsTheParkedModelSwitch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		var mu sync.Mutex
		var order []string
		record := func(step string) {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, step)
		}
		steps := func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(order)
		}
		applying := make(chan struct{})
		h.coord.applyPendingModel = func(context.Context, marotte.ChatID) {
			record("model")
			<-applying
		}
		done := heldPrompt(t, h, br, record)

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if got := steps(); !slices.Equal(got, []string{"model"}) {
			t.Errorf("session steps = %v while the model switch runs, want [model]", got)
		}
		close(applying)
		if rec := <-done; rec.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s, want admitted after the switch", rec.Code, rec.Body.String())
		}
		synctest.Wait()
		// The prompt's own close applies again after it, finding nothing parked.
		if got := steps(); len(got) < 2 || !slices.Equal(got[:2], []string{"model", "prompt"}) {
			t.Errorf("session steps = %v, want [model prompt] first", got)
		}
	})
}

func TestAfterTurnClose_Order(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, step)
	}
	bc := &bridgeCoordinator{
		turns:             newTurnRegistry(nopTurnRecords{}),
		bridge:            &bridges{mgr: newBridgeManager()},
		lifecycle:         &lifetime{shutdownCtx: t.Context()},
		applyPendingModel: func(context.Context, marotte.ChatID) { record("model") },
		resolveEnds:       func(context.Context, marotte.ChatID, command.TurnFence) { record("resolve") },
	}
	bc.turns.tailHook = func(_ marotte.ChatID, ended bool) {
		if ended {
			record("tail ended")
		}
	}
	bc.afterTurnClose(t.Context(), bc.turns.testLifecycle("c1"), &closeFacts{outcome: marotte.TurnOutcomeCompleted})
	if want := []string{"resolve", "model", "tail ended"}; !slices.Equal(order, want) {
		t.Errorf("afterTurnClose order = %v, want %v", order, want)
	}
}

// Each grace close owns its own hold: the first close's wait, ended by KAS's own turn opening, must not lift the hold
// that turn's grace close started while KAS still runs.
func TestAfterTurnClose_AnEarlierGraceClosesWaitLeavesALaterHoldInPlace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		entered, release := gateFirstClose(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		go graceClose(t, h, turnID)
		<-entered

		h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{})
		armWireStopGrace(t, h, command.CancelGrace)
		time.Sleep(command.CancelGrace)
		synctest.Wait()
		close(release)
		synctest.Wait()
		if h.coord.tryReserveNow("c1", marotte.TurnSourcePrompt) {
			t.Fatal("tryReserve acquired with KAS still running the wire turn's cancelled execution, want refused")
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if !h.coord.tryReserveNow("c1", marotte.TurnSourcePrompt) {
			t.Error("tryReserve refused once KAS reported the session idle, want the hold ended")
		}
	})
}

// Every close waits out a run KAS still reports before its drain, a clean one too: a drain sent then is refused
// admission, and the queued row would stay with no close left to send it.
func TestAfterTurnClose_ACleanClosesDrainWaitsForKASToGoIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
		unblock := make(chan struct{})
		br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: unblock}
		postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
		})
		synctest.Wait()
		if rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdQueuePrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-q1"}`),
		}); rec.Code != http.StatusOK {
			t.Fatalf("Setup: queue_prompt mid-turn = %d %s", rec.Code, rec.Body.String())
		}

		h.coord.WireTurnActivity("c1", true)
		close(unblock)
		synctest.Wait()
		if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-1"}) {
			t.Fatalf("turn_open prompt ids = %v while KAS still reported a run, want [m-1]", ids)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-1", "m-q1"}) {
			t.Errorf("turn_open prompt ids = %v once KAS reported idle, want [m-1 m-q1]", ids)
		}
		if rows := queuedRows(t, cs, "c1"); len(rows) != 0 {
			t.Errorf("queue = %+v, want the row sent", rows)
		}
	})
}

// heldRowHub opens c1's bridge, so a drain can send, and records each session/prompt from then on.
func heldRowHub(t *testing.T) (*Runtime, *testChatStore, func() []string) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open bridge: %v", err)
	}
	return h, cs, promptCalls(br)
}

// queueHeldRow queues the user's follow-up with no turn live, under a KAS hold.
func queueHeldRow(t *testing.T, h *Runtime) {
	t.Helper()
	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdQueuePrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"Please tidy the settings page","message_id":"m-q1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("Setup: queue_prompt during a KAS hold = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// graceCloseWireTurn opens KAS's own turn and closes it on the cancel grace while KAS still runs.
func graceCloseWireTurn(t *testing.T, h *Runtime) {
	t.Helper()
	h.coord.WireTurnStart(t.Context(), "c1", translate.TurnStartFrame{})
	armWireStopGrace(t, h, command.CancelGrace)
	time.Sleep(command.CancelGrace)
	synctest.Wait()
}

// wantHeldRowKeptPaused reports KAS idle and expects the follow-up kept, paused, and nothing sent: the reader stopped
// KAS's turn after it was queued, so it waits for the reader's next turn, as after any stop.
func wantHeldRowKeptPaused(t *testing.T, h *Runtime, cs *testChatStore, prompted func() []string) {
	t.Helper()
	if got := prompted(); len(got) != 0 {
		t.Fatalf("session/prompt blocks = %v while KAS still reported a run, want none", got)
	}
	h.coord.WireTurnActivity("c1", false)
	synctest.Wait()
	if got := prompted(); len(got) != 0 {
		t.Errorf("session/prompt blocks = %v once KAS went idle, want none before the reader's next turn", got)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-q1" || rows[0].State != marotte.RunPaused {
		t.Errorf("queue = %+v, want the follow-up kept, paused", rows)
	}
}

// A row queued under one grace close's hold is kept when KAS's own turn, stopped by the reader, closes on its grace
// and takes the hold over before the first closer stands down.
func TestCmdQueuePrompt_AHeldRowIsKeptThroughALaterGraceClosesHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, prompted := heldRowHub(t)
		entered, release := gateFirstClose(h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		go graceClose(t, h, turnID)
		<-entered
		queueHeldRow(t, h)

		graceCloseWireTurn(t, h)
		close(release)
		synctest.Wait()
		wantHeldRowKeptPaused(t, h, cs, prompted)
	})
}

// The other order: KAS's own turn opens while the first closer waits, which stands that closer down, and that turn
// then closes on its grace. The row is still kept.
func TestCmdQueuePrompt_AHeldRowIsKeptWhenKASsTurnOpensBeforeItsGraceClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, prompted := heldRowHub(t)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()
		queueHeldRow(t, h)

		graceCloseWireTurn(t, h)
		wantHeldRowKeptPaused(t, h, cs, prompted)
	})
}

// armWireStopGrace is a reader's stop of c1's open wire turn: the list marks it stopping and its grace is armed.
func armWireStopGrace(t *testing.T, h *Runtime, d time.Duration) {
	t.Helper()
	lc, ok := h.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("Setup: c1 has no lifecycle")
	}
	lc.mu.Lock()
	own := lc.own
	lc.mu.Unlock()
	if own == nil || own.Source != marotte.TurnSourceWireTurnStart || own.finalizing {
		t.Fatalf("Setup: own = %+v, want an open wire turn for the stop's grace", own)
	}
	// The reader's stop marks the wire turn stopping on the list, as cmdCancel's does, before its grace is armed.
	rs := h.coord.Stop(t.Context(), "c1", own.ID)
	h.coord.armWireGrace("c1", own.ID, d)
	if rs.Stop != nil {
		h.coord.ReleaseStop(rs.Stop)
	}
}
