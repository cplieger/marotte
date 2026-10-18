package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
)

// admitPromptBehindSteerLock opens c1's bridge and posts a prompt that is admitted and acknowledged while the steer
// lock keeps it short of its session/prompt; the caller reports KAS's state, then calls the returned unlock.
func admitPromptBehindSteerLock(t *testing.T, h *Runtime, cs *testChatStore, text string) func() {
	t.Helper()
	seedChat(t, cs, "c1")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open bridge: %v", err)
	}
	unlock, err := h.steerQueue.LockSteerOps(t.Context(), "c1")
	if err != nil {
		t.Fatalf("Setup: lock steer ops: %v", err)
	}
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"` + text + `","message_id":"m-2"}`),
	})
	if rec.Code != http.StatusOK {
		unlock()
		t.Fatalf("Setup: prompt = %d %s, want admitted on an idle chat", rec.Code, rec.Body.String())
	}
	return unlock
}

func promptFailures(h *Runtime) []string {
	var failed []string
	for _, e := range eventsOfType(h, marotte.EventError) {
		var p marotte.ErrorPayload
		if json.Unmarshal(e.Payload, &p) == nil && p.Code == marotte.ErrCodePromptFailed {
			failed = append(failed, p.Message)
		}
	}
	return failed
}

// turnSource names the source of the turn whose turn_open has id turn, "" when the log holds none.
func turnSource(t *testing.T, entries []marotte.Entry, turn string) marotte.TurnOpenSourceName {
	t.Helper()
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnOpen || entries[i].Turn != turn {
			continue
		}
		var o marotte.EntryTurnOpen
		if err := json.Unmarshal(entries[i].Payload, &o); err != nil {
			t.Fatalf("decode turn_open %q: %v", entries[i].ID, err)
		}
		return o.Source
	}
	return ""
}

// sendGateStep is one wire input during an admitted prompt's wait, or the release of its send.
type sendGateStep int

const (
	gateRelease   sendGateStep = iota // the prompt reaches its send
	kasActive                         // focus_update activity.turnActive true
	kasIdleReport                     // focus_update activity.turnActive false
	kasTurnStart                      // turn_start
	kasRevision                       // a content frame carrying agentInitiated
	kasReply                          // a content frame's text, folded where the chat's frames go
	kasTurnEnd                        // turn_end
	promptClosed                      // a failure closer ends the waiting prompt's turn
)

// apply delivers s; for kasReply it sets *folded to the turn the re-wake's text folded into.
func (s sendGateStep) apply(t *testing.T, h *Runtime, release func(), folded *string) {
	t.Helper()
	switch s {
	case gateRelease:
		release()
	case kasActive:
		h.coord.WireTurnActivity("c1", true)
	case kasIdleReport:
		h.coord.WireTurnActivity("c1", false)
	case kasTurnStart:
		h.coord.WireTurnStart(t.Context(), "c1")
	case kasRevision:
		h.coord.ReviseTurnBinding(t.Context(), "c1")
	case kasReply:
		log := h.coord.TurnFoldTarget(t.Context(), "c1")
		if log == nil {
			t.Fatal("TurnFoldTarget refused KAS's re-wake text")
		}
		if _, err := log.TextDelta(t.Context(), "", "say-k", "re-wake reply"); err != nil {
			t.Fatalf("TextDelta: %v", err)
		}
		*folded = log.ID()
	case kasTurnEnd:
		h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)
	case promptClosed:
		log, ok := h.coord.PromptTurn("c1")
		if !ok {
			t.Fatal("no prompt turn to close")
		}
		h.coord.AbandonInFlightTurn(t.Context(), "c1", log.ID(), marotte.StopReasonInterrupted, "closed while waiting", "", 0)
	}
}

// A notification re-wake can reach marotte in any order around an admitted prompt's send, and no frame of it is the
// prompt's own run, since KAS has not been sent the prompt. Each order holds the send until KAS's run is over, then
// sends it once as written, never fails it, and records the re-wake's text in a turn of its own.
func TestCmdPrompt_ARewakeBeforeTheSendIsWaitedOutInEveryOrder(t *testing.T) {
	cases := []struct {
		name  string
		steps []sendGateStep
	}{
		{"an active report before the send", []sendGateStep{kasActive, gateRelease, kasReply, kasIdleReport}},
		{
			"turn_start while the send waits, then a revising frame",
			[]sendGateStep{kasActive, gateRelease, kasTurnStart, kasRevision, kasReply, kasIdleReport, kasTurnEnd},
		},
		{
			"turn_start while the send waits, never revised",
			[]sendGateStep{kasActive, gateRelease, kasTurnStart, kasReply, kasIdleReport, kasTurnEnd},
		},
		{
			"an active report then turn_start, both before the send",
			[]sendGateStep{kasActive, kasTurnStart, gateRelease, kasReply, kasIdleReport, kasTurnEnd},
		},
		{
			"turn_start before the active report, the send between",
			[]sendGateStep{kasTurnStart, gateRelease, kasActive, kasReply, kasIdleReport, kasTurnEnd},
		},
		{"turn_start and no activity report at all", []sendGateStep{kasTurnStart, gateRelease, kasReply, kasTurnEnd}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, cs, br := newTestHub()
				defer shutdownHub(t, h)
				countDrains(h)
				prompted := promptCalls(br)
				// KAS's answer to the prompt stays out until the test ends, so the prompt's own run can follow its send.
				answer := make(chan struct{})
				br.mu.Lock()
				br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
				br.mu.Unlock()
				defer close(answer)
				release := admitPromptBehindSteerLock(t, h, cs, "Please tidy the settings page")

				last, folded := len(tc.steps)-1, ""
				for i, s := range tc.steps {
					s.apply(t, h, release, &folded)
					synctest.Wait()
					if i == last {
						break
					}
					if got, failed := prompted(), promptFailures(h); len(got) != 0 || len(failed) != 0 {
						t.Fatalf("after step %d: session/prompt blocks = %v, prompt_failed = %q while KAS runs, want neither",
							i, got, failed)
					}
				}

				if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
					t.Errorf("session/prompt blocks = %v once KAS's run ended, want the user's text once", got)
				}
				if failed := promptFailures(h); len(failed) != 0 {
					t.Errorf("prompt_failed = %q, want none", failed)
				}
				if src := turnSource(t, logOf(t, cs, "c1"), folded); src != marotte.TurnOpenNameWireTurnStart {
					t.Errorf("the re-wake's text is in a %q turn, want its own %q turn", src, marotte.TurnOpenNameWireTurnStart)
				}

				// The prompt's own run, bracketed after its send, folds into the prompt's turn.
				rewake := folded
				for _, s := range []sendGateStep{kasTurnStart, kasReply} {
					s.apply(t, h, release, &folded)
				}
				if folded == rewake || turnSource(t, logOf(t, cs, "c1"), folded) != marotte.TurnOpenNamePrompt {
					t.Errorf("the prompt's run folded into turn %q (re-wake %q), want the prompt's own turn", folded, rewake)
				}
			})
		})
	}
}

// openTurnsBut lists the turns the log opened and never closed, except keep.
func openTurnsBut(entries []marotte.Entry, keep string) []string {
	closed := map[string]bool{}
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindTurnClose {
			closed[entries[i].Turn] = true
		}
	}
	var open []string
	for i := range entries {
		if e := entries[i]; e.Kind == marotte.EntryKindTurnOpen && e.Turn != keep && !closed[e.Turn] {
			open = append(open, e.Turn)
		}
	}
	return open
}

// closeOfTurn decodes turn's turn_close, false when the log holds none.
func closeOfTurn(t *testing.T, entries []marotte.Entry, turn string) (marotte.EntryTurnClose, bool) {
	t.Helper()
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindTurnClose && entries[i].Turn == turn {
			return closeEntryOf(t, entries[i]), true
		}
	}
	return marotte.EntryTurnClose{}, false
}

// earlyRunFrames delivers a run's text and tool call before its turn_start, as KAS may for the prompt it was just
// sent, and returns the turns they folded into.
func earlyRunFrames(t *testing.T, h *Runtime, cs *testChatStore) (textTurn, toolTurn string) {
	t.Helper()
	text := h.coord.TurnFoldTarget(t.Context(), "c1")
	if text == nil {
		t.Fatal("TurnFoldTarget refused the run's text")
	}
	if _, err := text.TextDelta(t.Context(), "", "say-p", "prompt reply"); err != nil {
		t.Fatalf("TextDelta: %v", err)
	}
	tool := h.coord.TurnFoldTarget(t.Context(), "c1")
	if tool == nil {
		t.Fatal("TurnFoldTarget refused the run's tool call")
	}
	if _, err := tool.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "call-p", Title: "Read settings.ts"}); err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind == marotte.EntryKindToolCall {
			toolTurn = e.Turn
		}
	}
	return text.ID(), toolTurn
}

// The send gate decides by what holds the chat's own turn when KAS lets the prompt go: one row per state. Each row
// then feeds the run that follows the gate its text and tool call before its turn_start, the frame order KAS uses
// for the prompt it was just sent, and checks where they are recorded and how the prompt's turn closes.
func TestCmdPrompt_TheSendGateHandsThePromptTheChatFromEveryOwnState(t *testing.T) {
	cases := []struct {
		name  string
		steps []sendGateStep
		// prompts is how many session/prompt calls go out; failed the prompt_failed messages.
		prompts int
		failed  []string
		// earlyIn is the source of the turn the run's early frames land in.
		earlyIn marotte.TurnOpenSourceName
		// reason is the prompt turn's turn_close failure_reason, rewakeReason the re-wake turn's.
		reason       string
		rewakeReason string
	}{
		{
			name:    "own is the prompt's turn",
			steps:   []sendGateStep{gateRelease},
			prompts: 1, earlyIn: marotte.TurnOpenNamePrompt,
		},
		{
			name:    "own is nil after a bracketed re-wake ended",
			steps:   []sendGateStep{kasTurnStart, gateRelease, kasReply, kasTurnEnd},
			prompts: 1, earlyIn: marotte.TurnOpenNamePrompt,
		},
		{
			name:    "own is a bracketed re-wake until its turn_end",
			steps:   []sendGateStep{kasActive, kasTurnStart, gateRelease, kasReply, kasIdleReport, kasTurnEnd},
			prompts: 1, earlyIn: marotte.TurnOpenNamePrompt,
		},
		{
			name:    "own is a fold-opened re-wake KAS reported idle",
			steps:   []sendGateStep{kasActive, gateRelease, kasReply, kasIdleReport},
			prompts: 1, earlyIn: marotte.TurnOpenNamePrompt, rewakeReason: promptHandoffReason,
		},
		{
			name:    "own is a fold-opened re-wake with no activity report",
			steps:   []sendGateStep{kasReply, gateRelease},
			prompts: 1, earlyIn: marotte.TurnOpenNamePrompt, rewakeReason: promptHandoffReason,
		},
		{
			name:    "own is the bracketed turn a fold-opened re-wake's late turn_start opened",
			steps:   []sendGateStep{kasReply, kasTurnStart, gateRelease, kasReply, kasTurnEnd},
			prompts: 1, earlyIn: marotte.TurnOpenNamePrompt,
		},
		{
			name:    "the prompt's turn closed while it waited",
			steps:   []sendGateStep{kasActive, gateRelease, promptClosed},
			failed:  []string{"closed while waiting"},
			earlyIn: marotte.TurnOpenNameWireTurnStart, reason: "closed while waiting",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, cs, br := newTestHub()
				defer shutdownHub(t, h)
				countDrains(h)
				prompted := promptCalls(br)
				// KAS answers the prompt only after its run's frames, so they reach a turn still open.
				answer := make(chan struct{})
				closeAnswer := sync.OnceFunc(func() { close(answer) })
				defer closeAnswer()
				br.mu.Lock()
				br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
				br.mu.Unlock()
				release := admitPromptBehindSteerLock(t, h, cs, "Please tidy the settings page")
				prompt, ok := h.coord.PromptTurn("c1")
				if !ok {
					t.Fatal("Setup: the admitted prompt has no turn")
				}

				folded := ""
				for _, s := range tc.steps {
					s.apply(t, h, release, &folded)
					synctest.Wait()
				}
				if got := prompted(); len(got) != tc.prompts {
					t.Errorf("session/prompt blocks = %v, want %d", got, tc.prompts)
				}
				if got := promptFailures(h); !slices.Equal(got, tc.failed) {
					t.Errorf("prompt_failed = %q, want %q", got, tc.failed)
				}
				if open := openTurnsBut(logOf(t, cs, "c1"), prompt.ID()); len(open) != 0 {
					t.Errorf("turns %q still open beside the prompt once the gate decided, want none", open)
				}
				if folded != "" {
					if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), folded); !closed || c.FailureReason != tc.rewakeReason {
						t.Errorf("the re-wake's turn_close = %+v (closed %v), want failure_reason %q", c, closed, tc.rewakeReason)
					}
				}

				textTurn, toolTurn := earlyRunFrames(t, h, cs)
				kasTurnStart.apply(t, h, release, &folded)
				closeAnswer()
				synctest.Wait()

				// The log holds one prompt turn, the admitted one, so a prompt source names it.
				entries := logOf(t, cs, "c1")
				if src := turnSource(t, entries, textTurn); src != tc.earlyIn {
					t.Errorf("the run's early text is in a %q turn %q, want a %q turn", src, textTurn, tc.earlyIn)
				}
				if src := turnSource(t, entries, toolTurn); src != tc.earlyIn {
					t.Errorf("the run's early tool call is in a %q turn %q, want a %q turn", src, toolTurn, tc.earlyIn)
				}
				c, closed := closeOfTurn(t, entries, prompt.ID())
				if !closed || c.FailureReason != tc.reason {
					t.Errorf("the prompt's turn_close = %+v (closed %v), want failure_reason %q", c, closed, tc.reason)
				}
			})
		})
	}
}

// gateRewakeHub is c1 with a prompt admitted behind the steer lock, its send released while KAS reports a run, and
// that run's text folded into a fold-opened re-wake turn. hook, when set, is the registry's claimHook. KAS answers
// the prompt only when the test ends.
func gateRewakeHub(t *testing.T, hook func(h *Runtime)) (h *Runtime, cs *testChatStore, br *fakeBridge, rewake string) {
	t.Helper()
	h, cs, br = newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	countDrains(h)
	if hook != nil {
		h.coord.turns.claimHook = func() { hook(h) }
	}
	answer := make(chan struct{})
	t.Cleanup(func() { close(answer) })
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
	br.mu.Unlock()
	release := admitPromptBehindSteerLock(t, h, cs, "Please tidy the settings page")
	for _, s := range []sendGateStep{kasActive, gateRelease, kasReply} {
		s.apply(t, h, release, &rewake)
		synctest.Wait()
	}
	return h, cs, br, rewake
}

// KAS reporting a run between the send gate's wake and its decision keeps the fold-opened re-wake open, its next
// text in that turn, and the prompt unsent until KAS goes idle again.
func TestCmdPrompt_ARunReportedAsTheSendGateWakesKeepsTheRewakeOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var armed atomic.Bool
		h, cs, br, rewake := gateRewakeHub(t, func(h *Runtime) {
			if armed.CompareAndSwap(true, false) {
				h.coord.WireTurnActivity("c1", true)
			}
		})
		prompted := promptCalls(br)
		armed.Store(true)
		kasIdleReport.apply(t, h, nil, nil)
		synctest.Wait()

		if armed.Load() {
			t.Fatal("Setup: the send gate never woke on KAS's idle report")
		}
		if got := prompted(); len(got) != 0 {
			t.Errorf("session/prompt blocks = %v while KAS reports a run, want none", got)
		}
		if _, closed := closeOfTurn(t, logOf(t, cs, "c1"), rewake); closed {
			t.Error("the re-wake turn closed while KAS reported its run, want it open")
		}
		folded := ""
		kasReply.apply(t, h, nil, &folded)
		if folded != rewake {
			t.Errorf("the re-wake's next text folded into %q, want its turn %q", folded, rewake)
		}

		kasIdleReport.apply(t, h, nil, nil)
		synctest.Wait()
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v once KAS went idle, want the user's text once", got)
		}
		if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), rewake); !closed || c.FailureReason != promptHandoffReason {
			t.Errorf("the re-wake's turn_close = %+v (closed %v), want failure_reason %q", c, closed, promptHandoffReason)
		}
	})
}

// gateCloseBlock holds the send gate on its fold-close log line, which follows its claim of the re-wake turn.
type gateCloseBlock struct {
	entered chan struct{}
	release chan struct{}
}

func (*gateCloseBlock) Enabled(context.Context, slog.Level) bool { return true }

func (b *gateCloseBlock) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "closing the agent's unbracketed turn so an admitted prompt can be sent" {
		close(b.entered)
		<-b.release
	}
	return nil
}

func (b *gateCloseBlock) WithAttrs([]slog.Attr) slog.Handler { return b }
func (b *gateCloseBlock) WithGroup(string) slog.Handler      { return b }

// A re-wake's turn_start reaching marotte after the send gate claimed the fold-opened turn for the prompt is still
// a frame before the send: it opens a bracketed turn the prompt waits out, and the re-wake's later text and tool
// call are recorded there, never in the prompt's turn.
func TestCmdPrompt_ARewakeTurnStartAfterTheGateClaimedIsWaitedOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := &gateCloseBlock{entered: make(chan struct{}), release: make(chan struct{})}
		swapDefaultLogger(t, block)
		h, cs, br, rewake := gateRewakeHub(t, nil)
		prompted := promptCalls(br)
		prompt, ok := h.coord.PromptTurn("c1")
		if !ok {
			t.Fatal("Setup: the admitted prompt has no turn")
		}

		kasIdleReport.apply(t, h, nil, nil)
		<-block.entered
		br.deliver(newTurnStartMsg())
		synctest.Wait()
		close(block.release)
		synctest.Wait()

		if got := prompted(); len(got) != 0 {
			t.Errorf("session/prompt blocks = %v before the re-wake's turn_end, want none", got)
		}
		if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), rewake); !closed || c.FailureReason != promptHandoffReason {
			t.Errorf("the fold-opened turn's close = %+v (closed %v), want failure_reason %q", c, closed, promptHandoffReason)
		}
		textTurn, toolTurn := earlyRunFrames(t, h, cs)
		entries := logOf(t, cs, "c1")
		for what, turn := range map[string]string{"text": textTurn, "tool call": toolTurn} {
			if turn == prompt.ID() || turn == rewake || turnSource(t, entries, turn) != marotte.TurnOpenNameWireTurnStart {
				t.Errorf("the re-wake's later %s is in turn %q, want the bracketed turn its turn_start opened", what, turn)
			}
		}

		br.deliver(newTurnEndMsg("end_turn"))
		synctest.Wait()
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v after the re-wake's turn_end, want the user's text once", got)
		}
		if failed := promptFailures(h); len(failed) != 0 {
			t.Errorf("prompt_failed = %q, want none", failed)
		}
	})
}
