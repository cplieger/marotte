package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// compactCallCount is how many _kiro/session/compact calls the bridge received.
func compactCallCount(br *fakeBridge) int {
	n := 0
	for _, m := range br.callLog() {
		if m == marotte.MethodSessionCompact {
			n++
		}
	}
	return n
}

// pressCompact posts the reader's Compact on its own goroutine; the channel carries the answer once it came.
func pressCompact(t *testing.T, h *Runtime) <-chan *httptest.ResponseRecorder {
	t.Helper()
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		out <- postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCompact, ChatID: "c1"})
	}()
	return out
}

// answeredWithin2s is the press's answer, failing the test when none came within 2 s, far under a summary's minutes.
func answeredWithin2s(t *testing.T, answer <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-answer:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("the press was not answered while its compaction call ran")
		return nil
	}
}

// storeReading sets the chat's stored context reading, as KAS's last report left it.
func storeReading(t *testing.T, cs *testChatStore, pct float64) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, ok bool) bool {
		c.Usage.ContextPct, c.Usage.HasRealData = pct, true
		return ok
	}); err != nil {
		t.Fatalf("Setup: store the reading: %v", err)
	}
}

// failedRows is the reasons of the chat's compaction_failed rows, in log order.
func failedRows(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	log, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All(%q): %v", chatID, err)
	}
	var out []string
	for _, e := range entriesOfKind(t, log, marotte.EntryKindCompactionFailed) {
		var p marotte.EntryCompactionFailed
		decodePayload(t, e, &p)
		out = append(out, p.Reason)
	}
	return out
}

// compactionToasts is the compaction_failed error events broadcast so far.
func compactionToasts(t *testing.T, h *Runtime) []string {
	t.Helper()
	var out []string
	for _, p := range errorPayloadsSince(t, h, 0) {
		if p.Code == marotte.ErrCodeCompactionFailed {
			out = append(out, p.Message)
		}
	}
	return out
}

// compactingShown is the chat header's Compacting, which every header a page takes carries.
func compactingShown(t *testing.T, cs *testChatStore) bool {
	t.Helper()
	c, ok := cs.Get(t.Context(), "c1")
	return ok && c.Header().Compacting
}

var pressToolCalls atomic.Int64

// agentToolCallMsg is a tool call the chat's own agent opens, which KAS sends before its response's reading.
func agentToolCallMsg() *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": fmt.Sprintf("press-tc-%d", pressToolCalls.Add(1)),
		"title": "readFile", "kind": "read", "status": "pending",
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// modelReadingMsg is a model response's own context reading, KAS's in-turn context_usage (no summarizationThreshold).
func modelReadingMsg(pct float64) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind": "context_usage", "usagePercentage": pct, "contextUsage": map[string]any{"usagePercentage": pct},
	})
}

// The press is answered while its summary runs: KAS takes minutes to answer _kiro/session/compact, and a press held
// that long reaches the command budget and answers the page an error for a compaction still running.
func TestCompactPress_AnswersWhileTheCallRuns(t *testing.T) {
	t.Parallel()
	h, _, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)

	answer := pressCompact(t, h)
	waitForCall(t, br, marotte.MethodSessionCompact)

	if rec := answeredWithin2s(t, answer); rec.Code != http.StatusOK {
		t.Errorf("press = %d %s, want 200 while the call runs", rec.Code, rec.Body.String())
	}
	release()
}

// One press is one call and one outcome: a refusal is one toast, no compaction_failed row, and no later turn end
// calls again, however full the context reads.
func TestCompactPress_ARefusalIsOneCallAndOneToast(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":false}`))
	storeReading(t, cs, 95)

	if rec := answeredWithin2s(t, pressCompact(t, h)); rec.Code != http.StatusOK {
		t.Fatalf("press = %d %s, want 200", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(compactionToasts(t, h)) == 1 })
	runPrompt(t, h, cs, "c1", "m-2")
	runPrompt(t, h, cs, "c1", "m-3")

	if got := compactCallCount(br); got != 1 {
		t.Errorf("compact calls = %d, want the press's one", got)
	}
	if got := failedRows(t, cs, "c1"); len(got) != 0 {
		t.Errorf("compaction_failed rows = %q, want none: the refusal is the press's toast", got)
	}
	if got := compactionToasts(t, h); len(got) != 1 {
		t.Errorf("toasts = %q, want one", got)
	}
}

// A second press while the first one's call runs sends no second call and is refused at once, in words the page
// shows.
func TestCompactPress_ASecondPressWhileOneRunsSendsNoSecondCall(t *testing.T) {
	t.Parallel()
	h, _, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)
	first := pressCompact(t, h)
	waitForCall(t, br, marotte.MethodSessionCompact)

	rec := answeredWithin2s(t, pressCompact(t, h))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "the context is already being compacted") {
		t.Errorf("second press = %d %s, want 409 saying the context is already being compacted", rec.Code, rec.Body.String())
	}
	release()
	answeredWithin2s(t, first)

	staysFalse(t, 200*time.Millisecond, func() bool { return compactCallCount(br) != 1 },
		"two presses sent more than one compact call")
}

// A prompt sent while the press's call runs goes to kiro-cli at once: nothing holds a send behind a compaction.
func TestCompactPress_APromptSentWhileTheCallRunsIsSentAtOnce(t *testing.T) {
	t.Parallel()
	h, _, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)
	pressCompact(t, h)
	waitForCall(t, br, marotte.MethodSessionCompact)
	br.forgetCalls()

	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want 200", code)
	}

	waitFor(t, func() bool { return slices.Contains(br.callLog(), marotte.MethodPrompt) })
	release()
}

// The chat's header says a compaction runs for exactly as long as the press's call does, whatever it answers, so a
// reload keeps the dock's row and no row outlives the call.
func TestCompactPress_TheHeaderShowsTheCallWhileItRuns(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{`{"success":true}`, `{"success":false}`} {
		t.Run(answer, func(t *testing.T) {
			h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
			br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(answer))
			release := holdCall(t, br, marotte.MethodSessionCompact)
			if compactingShown(t, cs) {
				t.Fatal("Setup: the header shows a compaction before any press")
			}

			answeredWithin2s(t, pressCompact(t, h))
			waitForCall(t, br, marotte.MethodSessionCompact)
			if !compactingShown(t, cs) {
				t.Error("the header does not show the running compaction")
			}
			release()

			waitFor(t, func() bool { return !compactingShown(t, cs) })
		})
	}
}

// kiro-cli's own report that its summary failed is the outcome the reader sees. KAS's handleCompact sends no
// failure frame, so the report comes from a wake turn's own summary while the press's call runs: turn_start, the
// failed summarization frame, turn_end, then the call answers success false. The refusal after it adds no toast.
func TestCompactPress_KiroCLIsFailureReportIsTheOneOutcome(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":false}`))
	release := holdCall(t, br, marotte.MethodSessionCompact)
	answeredWithin2s(t, pressCompact(t, h))
	waitForCall(t, br, marotte.MethodSessionCompact)

	send(t, h,
		newTurnStartMsg(),
		newSessionInfoMsg(map[string]any{"summarization": map[string]any{"status": "model error"}}),
		newTurnEndMsg("end_turn"),
	)
	waitFor(t, func() bool { return len(compactionToasts(t, h)) == 1 })
	release()

	waitFor(t, func() bool { return !compactingShown(t, cs) })
	staysFalse(t, 200*time.Millisecond, func() bool { return len(compactionToasts(t, h)) != 1 },
		"kiro-cli's failure report and the press's refusal both toasted")
	if got := failedRows(t, cs, "c1"); len(got) != 1 || got[0] != "model error" {
		t.Errorf("compaction_failed rows = %q, want kiro-cli's one, \"model error\"", got)
	}
}

// kiro-cli's own in-turn summary shows in the header between its running frame and its outcome, and a turn that
// closes with no outcome frame leaves no mark behind.
func TestCompaction_KiroCLIsInTurnSummaryShowsWhileItRuns(t *testing.T) {
	t.Parallel()
	t.Run("running then landed", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		send(t, h, newTurnStartMsg(), newSessionInfoMsg(map[string]any{
			"kind": "summarization_started", "summarization": map[string]any{"status": "running"},
		}))
		waitFor(t, func() bool { return compactingShown(t, cs) })

		send(t, h, newSessionInfoMsg(map[string]any{"summarization": map[string]any{
			"status": "success", "summary": map[string]any{"conversationSummary": "the summary"},
		}}))
		waitFor(t, func() bool { return !compactingShown(t, cs) })
		send(t, h, newTurnEndMsg("end_turn"))
	})
	t.Run("running then the turn closes", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		send(t, h, newTurnStartMsg(), newSessionInfoMsg(map[string]any{
			"kind": "summarization_started", "summarization": map[string]any{"status": "running"},
		}))
		waitFor(t, func() bool { return compactingShown(t, cs) })

		send(t, h, newTurnEndMsg("end_turn"))
		waitFor(t, func() bool { return !compactingShown(t, cs) })
	})
}

// A press while kiro-cli's own summary runs sends no call and is refused in the page's words: KAS answers one with
// {success: false} before its summary lands. The header keeps the summary's mark until its outcome.
func TestCompactPress_DuringKiroCLIsSummaryIsRefused(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	send(t, h, newTurnStartMsg(), newSessionInfoMsg(map[string]any{
		"kind": "summarization_started", "summarization": map[string]any{"status": "running"},
	}))
	waitFor(t, func() bool { return compactingShown(t, cs) })

	rec := answeredWithin2s(t, pressCompact(t, h))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "the context is already being compacted") {
		t.Errorf("press = %d %s, want 409 saying the context is already being compacted", rec.Code, rec.Body.String())
	}
	staysFalse(t, 200*time.Millisecond, func() bool { return compactCallCount(br) != 0 || !compactingShown(t, cs) },
		"the press sent a call or cleared the summary's mark")

	send(t, h, newSessionInfoMsg(map[string]any{"summarization": map[string]any{
		"status": "success", "summary": map[string]any{"conversationSummary": "the summary"},
	}}))
	waitFor(t, func() bool { return !compactingShown(t, cs) })
	send(t, h, newTurnEndMsg("end_turn"))
}

// A summary that keeps failing across wake turns is shown once, and a press refused during that streak still tells
// the reader its own outcome: the suppressed failure is not the press's account.
func TestCompaction_AFailingStreakShowsOnceAndAPressStillToasts(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	failedTurn := []*marotte.RPCResponse{
		newTurnStartMsg(),
		newSessionInfoMsg(map[string]any{"summarization": map[string]any{"status": "model error"}}),
		newTurnEndMsg("end_turn"),
	}
	send(t, h, failedTurn...)
	waitFor(t, func() bool { return len(compactionToasts(t, h)) == 1 })

	br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":false}`))
	release := holdCall(t, br, marotte.MethodSessionCompact)
	answeredWithin2s(t, pressCompact(t, h))
	waitForCall(t, br, marotte.MethodSessionCompact)
	send(t, h, failedTurn...)
	release()

	want := []string{"model error", compactRefusedDetail}
	waitFor(t, func() bool { return len(compactionToasts(t, h)) >= 2 })
	staysFalse(t, 200*time.Millisecond, func() bool { return len(compactionToasts(t, h)) != 2 }, "a third toast came")
	if got := compactionToasts(t, h); !slices.Equal(got, want) {
		t.Errorf("toasts = %q, want %q", got, want)
	}
	if got := failedRows(t, cs, "c1"); len(got) != 1 {
		t.Errorf("compaction_failed rows = %q, want the streak's one", got)
	}
}

// Deleting a chat ends its summary-failure streak, so the process keeps no state for a chat that is gone. The test
// store keeps the record the delete's membership step would have erased, so a later failure shows what was kept.
func TestCompaction_DeletingAChatEndsItsFailingStreak(t *testing.T) {
	t.Parallel()
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	send(t, h, newTurnStartMsg(),
		newSessionInfoMsg(map[string]any{"summarization": map[string]any{"status": "model error"}}),
		newTurnEndMsg("end_turn"))
	waitFor(t, func() bool { return len(failedRows(t, cs, "c1")) == 1 })

	h.DeleteChatStateByChain(t.Context(), "c1", nil, command.RunStopChatDeleted)
	failure := json.RawMessage(`{"_meta":{"kiro":{"summarization":{"status":"model error"}}}}`)
	h.translator.HandleSessionInfoUpdate(t.Context(), "c1", failure, translate.FrameAttribution{})

	if got := failedRows(t, cs, "c1"); len(got) != 2 {
		t.Errorf("compaction_failed rows = %q, want 2: the deleted chat's streak was kept", got)
	}
}

// A summary mark whose header write was lost still ends with its turn: the press after it is accepted, not refused
// for a compaction nothing on screen shows.
func TestCompaction_ALostRunningMarkStillEndsWithItsTurn(t *testing.T) {
	t.Parallel()
	h, cs, br, tails := idleAfterFirstTurn(t, "/tmp/work")
	send(t, h, newTurnStartMsg())
	fault := errors.New("disk full")
	cs.mutateFault.Store(&fault)
	send(t, h, newSessionInfoMsg(map[string]any{
		"kind": "summarization_started", "summarization": map[string]any{"status": "running"},
	}))
	cs.mutateFault.Store(nil)
	if compactingShown(t, cs) {
		t.Fatal("Setup: the running mark was written despite the fault")
	}

	send(t, h, newTurnEndMsg("end_turn"))
	waitFor(t, func() bool { return tails.Load() == 2 })
	if rec := answeredWithin2s(t, pressCompact(t, h)); rec.Code != http.StatusOK {
		t.Errorf("press = %d %s, want 200 once the summary's turn closed", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodSessionCompact)
}

// A press whose call ends while kiro-cli's own summary runs leaves the summary's mark, and the summary's outcome
// leaves a running press's mark: the header clears only once both ended.
func TestCompaction_TheMarkOutlivesWhicheverEndsFirst(t *testing.T) {
	t.Parallel()
	summaryRunning := newSessionInfoMsg(map[string]any{
		"kind": "summarization_started", "summarization": map[string]any{"status": "running"},
	})
	summaryLanded := newSessionInfoMsg(map[string]any{"summarization": map[string]any{
		"status": "success", "summary": map[string]any{"conversationSummary": "the summary"},
	}})
	t.Run("the press ends first", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":false}`))
		release := holdCall(t, br, marotte.MethodSessionCompact)
		answeredWithin2s(t, pressCompact(t, h))
		waitForCall(t, br, marotte.MethodSessionCompact)
		send(t, h, newTurnStartMsg(), summaryRunning)

		release()
		waitFor(t, func() bool { return len(compactionToasts(t, h)) == 1 })
		staysFalse(t, 200*time.Millisecond, func() bool { return !compactingShown(t, cs) },
			"the press's end cleared the mark of the summary still running")

		send(t, h, summaryLanded)
		waitFor(t, func() bool { return !compactingShown(t, cs) })
		send(t, h, newTurnEndMsg("end_turn"))
	})
	t.Run("the summary ends first", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		release := holdCall(t, br, marotte.MethodSessionCompact)
		answeredWithin2s(t, pressCompact(t, h))
		waitForCall(t, br, marotte.MethodSessionCompact)
		send(t, h, newTurnStartMsg(), summaryRunning, summaryLanded, newTurnEndMsg("end_turn"))

		staysFalse(t, 200*time.Millisecond, func() bool { return !compactingShown(t, cs) },
			"the summary's outcome cleared the mark of the press still running")
		release()
		waitFor(t, func() bool { return !compactingShown(t, cs) })
	})
}

// marotte starts no compaction of its own: neither a turn end over the stored reading nor an in-turn reading at
// 96%, from a response that called a tool, calls _kiro/session/compact or stops the turn. kiro-cli compacts inside
// the turn at its own threshold.
func TestCompaction_MarotteStartsNone(t *testing.T) {
	t.Parallel()
	t.Run("turn ends over a full stored reading", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		storeReading(t, cs, 99)

		runPrompt(t, h, cs, "c1", "m-2")
		runPrompt(t, h, cs, "c1", "m-3")

		staysFalse(t, 200*time.Millisecond, func() bool { return compactCallCount(br) != 0 },
			"a turn end started a compaction")
	})
	t.Run("in-turn readings past the point", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("prompt = %d, want 200", code)
		}
		waitFor(t, func() bool { return slices.Contains(br.callLog(), marotte.MethodPrompt) })

		br.deliver(agentToolCallMsg())
		br.deliver(modelReadingMsg(91))
		br.deliver(agentToolCallMsg())
		br.deliver(modelReadingMsg(96))
		waitFor(t, func() bool {
			c, _ := cs.Get(context.Background(), "c1")
			return c != nil && c.Usage.ContextPct == 96
		})
		open(0)
		waitFor(t, func() bool { return !h.coord.turns.live("c1") && !listLive(h, "c1") })

		if got := compactCallCount(br); got != 0 {
			t.Errorf("compact calls = %d, want none", got)
		}
		if slices.Contains(br.callLog(), marotte.MethodCancel) {
			t.Error("marotte cancelled the turn to compact it")
		}
		closes := closesOf(t, logOf(t, cs, "c1"))
		if last := closes[len(closes)-1]; last.Outcome != marotte.TurnOutcomeCompleted {
			t.Errorf("the turn closed %q, want completed", last.Outcome)
		}
	})
}

// A press on a chat with no live session compacts nothing.
func TestCompactRequest_NoSessionCompactsNothing(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	if out, err := h.RequestCompact(t.Context(), "c-none"); err != nil || out != command.CompactNoSession {
		t.Errorf("press with no session = (%v, %v), want no session", out, err)
	}
}
