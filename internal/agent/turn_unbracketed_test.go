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
	h.coord.WireTurnStart(t.Context(), "c1")

	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, endTurnResponse())

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeEmpty {
		t.Errorf("turn_close entries = %+v, want one graded empty", closes)
	}
}

// countDrains replaces the close pipeline's drain with a counter.
func countDrains(h *Runtime) *atomic.Int32 {
	var drained atomic.Int32
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) {
		drained.Add(1)
	}
	return &drained
}

// graceClose closes the turn as the cancel grace's expiry does on the prompt-failure path.
func graceClose(t *testing.T, h *Runtime, turnID string) {
	t.Helper()
	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerPromptFailure, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})
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

// A shell turn's busy refusal also concludes `cancelled`, but sent kiro-cli nothing, so it starts no hold: a follow-up
// queued after it is refused as no turn, and its drain waits only for KAS's run, like any close's.
func TestAfterTurnClose_AShellBusyRefusalStartsNoHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		turnID, err := h.coord.OpenTurn(t.Context(), "c1", command.TurnOpen{
			Source: marotte.TurnSourceLocalShell,
			Prompt: &marotte.EntryPrompt{ID: "m-shell", Text: "!ls"},
			Init:   func(c *marotte.Chat) { c.Name = "test chat" },
		})
		if err != nil {
			t.Fatalf("Setup: OpenTurn(local shell): %v", err)
		}
		h.coord.WireTurnActivity("c1", true)

		h.coord.AbandonInFlightTurn(t.Context(), "c1", turnID, marotte.StopReasonCancelled, "", "", 0)
		synctest.Wait()
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdQueuePrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"after the shell","message_id":"m-q1"}`),
		})
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_turn") {
			t.Errorf("queue_prompt after a shell busy refusal = %d %s, want 409 no_turn", rec.Code, rec.Body.String())
		}
		if n := drained.Load(); n != 0 {
			t.Errorf("drained %d times while KAS still reported a run, want none", n)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times once KAS reported idle, want one", n)
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
		if h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			t.Fatal("TryReserveTurn acquired with KAS still reporting the session busy, want refused")
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

		lc := h.coord.turns.lifecycleFor("c1")
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
		lc := h.coord.turns.lifecycleFor("c1")
		lc.mu.Lock()
		state, gen := lc.kas.state, lc.fwdGen
		lc.mu.Unlock()
		if state != kasRunning {
			t.Errorf("KAS state after the teardown = %v, want running with the hold ended", state)
		}

		h.coord.turns.sealPosition("c1", gen)
		if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			t.Error("TryReserveTurn refused once the session's forward exited, want admitted")
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

		h.coord.WireTurnStart(t.Context(), "c1")
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

// The recorded shape again, with the user's own prompt in the gap: sent right after the grace closed the stopped turn while KAS
// still ran the session, it must reach kiro-cli only once KAS reports the session idle, exactly once, as written.
func TestCmdPrompt_AfterAGraceCloseWaitsForKASToGoIdle(t *testing.T) {
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
		time.Sleep(time.Second)
		synctest.Wait()
		if n := countCalls(br, marotte.MethodPrompt); n != 0 {
			t.Fatalf("session/prompt calls = %d while KAS still reported the session busy, want none", n)
		}
		if len(done) != 0 {
			t.Fatalf("the prompt was answered while KAS still reported the session busy, want it held")
		}

		h.coord.WireTurnActivity("c1", false)
		rec := <-done
		synctest.Wait()
		if rec.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s, want admitted once KAS went idle", rec.Code, rec.Body.String())
		}
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

		if got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", command.AdmissionWait); got != command.AdmissionStarting {
			t.Errorf("ReserveTurnForPrompt during a KAS hold = %v, want AdmissionStarting", got)
		}
		if h.coord.TryReserveTurn("c1", marotte.TurnSourceLocalShell) {
			t.Error("TryReserveTurn(local shell) during a KAS hold acquired, want refused")
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
// writes and broadcasts: a prompt sent in that window must wait for KAS to go idle like one sent after the close.
func TestCmdPrompt_DuringAWireTurnsGraceCloseWaitsForKASToGoIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		seedChat(t, cs, "c1")
		h.coord.WireTurnStart(t.Context(), "c1")
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
		if !h.coord.ArmWireCancelGrace("c1", command.CancelGrace) {
			t.Fatal("Setup: ArmWireCancelGrace found no wire turn")
		}
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
		if len(done) != 0 {
			t.Fatal("the prompt was answered while KAS still reported the session busy, want it held")
		}

		h.coord.WireTurnActivity("c1", false)
		rec := <-done
		synctest.Wait()
		if rec.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s, want admitted once KAS went idle", rec.Code, rec.Body.String())
		}
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

// A tangent's findings for a parent under a KAS hold are queued, never failed, and handed to the hold's drain once KAS
// goes idle, as a row the stop does not hold back.
func TestMergeTangent_TheFindingsGoToTheParentsDrainOnceKASGoesIdle(t *testing.T) {
	h, cs, calls := mergeHub(t)
	var mu sync.Mutex
	var facts []command.CloseFacts
	h.coord.drainAfterClose = func(_ context.Context, chatID marotte.ChatID, cl command.CloseFacts, _ command.EndFacts) {
		if chatID != "c1" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		facts = append(facts, cl)
	}
	drains := func() []command.CloseFacts {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(facts)
	}
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
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "Found it.") {
		t.Fatalf("parent's queue = %+v, want the findings queued during the hold", rows)
	}
	if n := calls(marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d during the hold, want the tangent's summary only", n)
	}
	if got := drains(); len(got) != 0 {
		t.Fatalf("drains = %+v during the hold, want none", got)
	}

	h.coord.WireTurnActivity("c1", false)
	waitFor(t, func() bool { return len(drains()) == 1 })
	if got := drains()[0]; !slices.Equal(got.AfterStop, []string{rows[0].ID}) || got.Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("drain facts = %+v, want the cancelled close with the findings row %s to send", got, rows[0].ID)
	}
}

// KAS's report of the cancelled run can still be in the read loop when the grace claims the turn: a run reported
// during the close holds admission like one reported before it, until KAS reports idle.
func TestCmdPrompt_ARunReportedDuringAGraceCloseHoldsAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		drained := countDrains(h)
		seedChat(t, cs, "c1")
		h.coord.WireTurnStart(t.Context(), "c1")
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
		if !h.coord.ArmWireCancelGrace("c1", command.CancelGrace) {
			t.Fatal("Setup: ArmWireCancelGrace found no wire turn")
		}
		time.Sleep(command.CancelGrace)
		<-entered
		prompted := promptCalls(br)

		h.coord.WireTurnActivity("c1", true)
		// Errorf: the close is parked on release, which a Fatal here would leave blocked.
		if h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			h.coord.ReleaseTurnReservation("c1")
			t.Error("TryReserveTurn acquired after KAS reported a run during the grace close, want refused")
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
		if got := prompted(); len(got) != 0 || len(done) != 0 || drained.Load() != 0 {
			t.Fatalf("session/prompt blocks = %v, answered = %v, drains = %d while KAS reported a run, want none",
				got, len(done) != 0, drained.Load())
		}

		h.coord.WireTurnActivity("c1", false)
		rec := <-done
		synctest.Wait()
		if rec.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s, want admitted once KAS went idle", rec.Code, rec.Body.String())
		}
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v, want the user's text once", got)
		}
	})
}

// KAS can report a run after the close's hold ended and before its drain reserved: a prompt sent then waits for KAS to
// report idle like one sent during the hold, and the drain sends nothing beside the run.
func TestCmdPrompt_ARunReportedAfterTheHoldEndsHoldsAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, br := newTestHub()
		defer shutdownHub(t, h)
		entered, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		drain := h.coord.drainAfterClose
		h.coord.drainAfterClose = func(ctx context.Context, chatID marotte.ChatID, cl command.CloseFacts, ends command.EndFacts) {
			if blocked.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			drain(ctx, chatID, cl, ends)
		}
		turnID, _ := h.stagePromptTurn(t, "c1")
		graceClose(t, h, turnID)
		<-entered
		prompted := promptCalls(br)

		h.coord.WireTurnActivity("c1", true)
		// Errorf: the drain is parked on release, which a Fatal here would leave blocked.
		if h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			h.coord.ReleaseTurnReservation("c1")
			t.Error("TryReserveTurn acquired after KAS reported a run, want refused")
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
		if got := prompted(); len(got) != 0 || len(done) != 0 {
			t.Fatalf("session/prompt blocks = %v, answered = %v while KAS reported a run, want none", got, len(done) != 0)
		}

		h.coord.WireTurnActivity("c1", false)
		rec := <-done
		synctest.Wait()
		if rec.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s, want admitted once KAS went idle", rec.Code, rec.Body.String())
		}
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

// The same for the turn-end compaction: the waiting prompt reaches the session only once it ran.
func TestAfterTurnClose_AWaitingPromptFollowsTheTurnEndCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		r := newCompactRig(60, false)
		r.reading(70)
		r.block = make(chan struct{})
		r.ac.live = h.coord.turns.live
		// Read at the compaction's entry, so it does not depend on which goroutine wins once a hold lifts early: no
		// reservation may exist then, and none may be taken.
		var admittedAtEntry atomic.Bool
		compact := r.ac.compact
		r.ac.compact = func(ctx context.Context, b command.CompactBridge) (bool, error) {
			_, held := h.coord.AdmissionHolderSource("c1")
			if !held && h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
				h.coord.ReleaseTurnReservation("c1")
				held = true
			}
			admittedAtEntry.Store(held)
			return compact(ctx, b)
		}
		h.coord.autoCompact = r.ac
		var prompts atomic.Int32
		done := heldPrompt(t, h, br, func(string) { prompts.Add(1) })

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if admittedAtEntry.Load() {
			t.Error("a prompt was admitted when the turn-end compaction began, want the hold still refusing it")
		}
		if n, p := r.count(), prompts.Load(); n != 1 || p != 0 {
			t.Errorf("compactions = %d, session/prompt calls = %d once KAS went idle, want the compaction first, alone", n, p)
		}
		close(r.block)
		if rec := <-done; rec.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s, want admitted after the compaction", rec.Code, rec.Body.String())
		}
		synctest.Wait()
		if p := prompts.Load(); p != 1 {
			t.Errorf("session/prompt calls = %d, want one", p)
		}
	})
}

// Each grace close owns its own hold: the first close's wait, ended by KAS's own turn opening, must not lift the hold
// that turn's grace close started while KAS still runs.
func TestAfterTurnClose_AnEarlierGraceClosesWaitLeavesALaterHoldInPlace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, _ := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		entered, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		resolve := h.coord.resolveEnds
		h.coord.resolveEnds = func(ctx context.Context, chatID marotte.ChatID, f command.TurnFence) command.EndFacts {
			// Not a sync.Once: a second caller parked on its mutex is not durably blocked, so synctest.Wait would hang.
			if blocked.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			return resolve(ctx, chatID, f)
		}
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		<-entered

		h.coord.WireTurnStart(t.Context(), "c1")
		if !h.coord.ArmWireCancelGrace("c1", command.CancelGrace) {
			t.Fatal("Setup: ArmWireCancelGrace found no wire turn")
		}
		time.Sleep(command.CancelGrace)
		synctest.Wait()
		close(release)
		synctest.Wait()
		if h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			t.Fatal("TryReserveTurn acquired with KAS still running the wire turn's cancelled execution, want refused")
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			t.Error("TryReserveTurn refused once KAS reported the session idle, want the hold ended")
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
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
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
	h.coord.WireTurnStart(t.Context(), "c1")
	if !h.coord.ArmWireCancelGrace("c1", command.CancelGrace) {
		t.Fatal("Setup: ArmWireCancelGrace found no wire turn")
	}
	time.Sleep(command.CancelGrace)
	synctest.Wait()
}

// wantHeldRowSentOnce reports KAS idle and expects the held follow-up sent exactly once and gone from the queue.
func wantHeldRowSentOnce(t *testing.T, h *Runtime, cs *testChatStore, prompted func() []string) {
	t.Helper()
	if got := prompted(); len(got) != 0 {
		t.Fatalf("session/prompt blocks = %v while KAS still reported a run, want none", got)
	}
	h.coord.WireTurnActivity("c1", false)
	synctest.Wait()
	if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
		t.Errorf("session/prompt blocks = %v once KAS went idle, want the held follow-up once", got)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 0 {
		t.Errorf("queue = %+v, want the held follow-up sent", rows)
	}
}

// A row queued under one grace close's hold is still owed when KAS's own turn closes on its grace and takes the hold
// over before the first closer stands down: it goes once KAS reports idle.
func TestCmdQueuePrompt_AHeldRowSurvivesALaterGraceClosesHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, prompted := heldRowHub(t)
		entered, release := make(chan struct{}), make(chan struct{})
		var blocked atomic.Bool
		resolve := h.coord.resolveEnds
		h.coord.resolveEnds = func(ctx context.Context, chatID marotte.ChatID, f command.TurnFence) command.EndFacts {
			if blocked.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			return resolve(ctx, chatID, f)
		}
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		<-entered
		queueHeldRow(t, h)

		graceCloseWireTurn(t, h)
		close(release)
		synctest.Wait()
		wantHeldRowSentOnce(t, h, cs, prompted)
	})
}

// The other order: KAS's own turn opens while the first closer waits, which stands that closer down, and that turn
// then closes on its grace. The row is still owed and goes once KAS reports idle.
func TestCmdQueuePrompt_AHeldRowSurvivesKASsTurnOpeningBeforeItsGraceClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, prompted := heldRowHub(t)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()
		queueHeldRow(t, h)

		graceCloseWireTurn(t, h)
		wantHeldRowSentOnce(t, h, cs, prompted)
	})
}
