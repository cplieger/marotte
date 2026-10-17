package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// The recorded shape of turn n=834: KAS ran the prompt (it launched a workflow) but its session adapter owned another
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

// The recorded shape of turn n=834's start: the cancel's grace closed n=833 while KAS still ran executions for the
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

// A shell turn's busy refusal also concludes `cancelled`, but sent kiro-cli nothing, so KAS's activity (another
// turn's) does not hold its drain.
func TestAfterTurnClose_AShellBusyRefusalDrainsAtOnceWhileKASIsBusy(t *testing.T) {
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
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times after a shell busy refusal, want one at once", n)
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

// A teardown re-fences the chat: its session is going, so the hold ends with no drain, and the next session admits.
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
		if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			t.Error("TryReserveTurn refused after the teardown, want the hold ended")
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

		h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)
		synctest.Wait()
		if n := drained.Load(); n != 1 {
			t.Errorf("drained %d times after KAS's turn closed, want one", n)
		}
	})
}

// The recorded shape again, with the user's own prompt in the gap: sent right after the grace closed n=833 while KAS
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
				Payload: json.RawMessage(`{"text":"The turn footer layout is also just messy","message_id":"m-2"}`),
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
		if len(got) != 1 || !strings.Contains(got[0], "The turn footer layout is also just messy") {
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
				Payload: json.RawMessage(`{"text":"The turn footer layout is also just messy","message_id":"m-2"}`),
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
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "The turn footer layout is also just messy") {
			t.Errorf("session/prompt blocks = %v, want the user's text once", got)
		}
	})
}

// A Queue-mode send during a KAS hold is queued, not refused as no turn, and never sent beside KAS's execution.
// Once KAS goes idle the drain grades the cancelled close, which holds the queue as any stop does.
func TestCmdQueuePrompt_DuringAKASHoldIsQueuedBehindIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		turnID, _ := h.stagePromptTurn(t, "c1")
		h.coord.WireTurnActivity("c1", true)
		graceClose(t, h, turnID)
		synctest.Wait()
		prompted := promptCalls(br)

		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdQueuePrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"then run the tests","message_id":"m-q1"}`),
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
		if got := prompted(); len(got) != 0 {
			t.Errorf("session/prompt blocks = %v after a cancelled close, want the queue held", got)
		}
		if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-q1" {
			t.Errorf("queue = %+v, want m-q1 still queued", rows)
		}
	})
}

// A tangent's findings for a parent under a KAS hold are queued behind the hold, never failed as undeliverable.
func TestMergeTangent_QueuesTheFindingsBehindAParentsKASHold(t *testing.T) {
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
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || !strings.Contains(rows[0].Text, "Found it.") {
		t.Errorf("parent's queue = %+v, want the findings queued", rows)
	}
	if n := calls(marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d, want the tangent's summary only", n)
	}
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
