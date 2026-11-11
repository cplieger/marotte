package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// compactCalls counts the hub's compaction calls and answers each accepted (refuse false) or refused; failures
// collects the toasts.
type compactCalls struct {
	mu       sync.Mutex
	calls    int
	refuse   bool
	failures []string
}

func (c *compactCalls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *compactCalls) toasts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.failures)
}

// onCompactor points the hub's compactor at a policy of pct, the reading at usage, and a counting compact call.
func onCompactor(h *Runtime, pct int, enabled bool, usage float64) *compactCalls {
	c := &compactCalls{}
	ac := h.coord.autoCompact
	ac.policy = func(context.Context) (bool, int) { return enabled, pct }
	ac.usage = func(context.Context, marotte.ChatID) marotte.Usage {
		return marotte.Usage{ContextPct: usage, HasRealData: usage > 0}
	}
	ac.compact = func(context.Context, command.CompactBridge) (bool, uint64, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls++
		return !c.refuse, 0, nil
	}
	ac.toast = func(_ context.Context, _ marotte.ChatID, _ marotte.ErrorCode, detail string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.failures = append(c.failures, detail)
	}
	return c
}

// A turn whose end reads the context at the point compacts once at that end.
func TestAutoCompact_ATurnEndAtThePointCompactsOnce(t *testing.T) {
	h, _, _, tails := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 85, true, 88)
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return tails.Load() == 2 && calls.count() == 1 })
	staysFalse(t, 0, func() bool { return calls.count() != 1 }, "the turn end compacted more than once")
}

// A turn the model refused for the context's size compacts at its end whatever the stored reading.
func TestAutoCompact_AContextLimitTurnEndCompactsWhateverTheReading(t *testing.T) {
	h, _, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 50)
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "too long", marotte.FailureKindContextLimit, 0)
	waitFor(t, func() bool { return calls.count() == 1 })
}

// With the switch off, a turn end at the point compacts nothing; the reader's press still does.
func TestAutoCompact_TheSwitchOffCompactsOnlyWhatTheReaderAsks(t *testing.T) {
	h, _, _, tails := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, false, 95)
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "too long", marotte.FailureKindContextLimit, 0)
	waitFor(t, func() bool { return tails.Load() == 2 })
	if calls.count() != 0 {
		t.Fatalf("compact calls with the switch off = %d, want 0", calls.count())
	}
	if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactAccepted {
		t.Errorf("the reader's press = (%v, %v), want accepted", out, err)
	}
}

// A reading at the point during a turn lists one compaction on the record behind that turn; its end runs it before
// any queued row, and a second reading in the same turn lists no second one.
func TestAutoCompact_AReadingDuringATurnListsOneCompactionRunAtItsEnd(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	calls := onCompactor(h, 90, true, 0)
	if code := postPrompt(t, h, "a"); code != 200 {
		t.Fatalf("Setup: prompt = %d", code)
	}
	queueTextDuring(t, h, "m-q1", "after the turn")

	h.coord.ObserveContextUsage(t.Context(), "c1", 91)
	h.coord.ObserveContextUsage(t.Context(), "c1", 94)
	if got := runIDs(t, cs, "c1"); !slices.Equal(got, []string{"compaction", "m-q1"}) {
		t.Fatalf("dock after the crossing = %v, want [compaction m-q1]", got)
	}
	if calls.count() != 0 {
		t.Fatalf("compact calls mid-turn = %d, want 0", calls.count())
	}
	open(0)
	open(1)
	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q1") })
	if calls.count() != 1 {
		t.Errorf("compact calls = %d, want the compaction once, before m-q1", calls.count())
	}
}

// The reader's cancel of a listed compaction takes it off, and the turn's end then compacts nothing unasked.
func TestAutoCompact_ACancelHoldsForTheTurn(t *testing.T) {
	h, cs, _, tails := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	id, _ := h.stagePromptTurn(t, "c1")
	h.coord.ObserveContextUsage(t.Context(), "c1", 92)
	c := compactionEntry(t, cs, "c1")
	if c.ID == "" {
		t.Fatal("Setup: the crossing listed no compaction")
	}
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdCancelCompact, ChatID: "c1", Payload: json.RawMessage(`{"id":"` + c.ID + `"}`),
	})
	if rec.Code != 200 {
		t.Fatalf("cancel_compact = %d %s", rec.Code, rec.Body.String())
	}
	if compactionEntry(t, cs, "c1").ID != "" {
		t.Error("the cancelled compaction is still listed")
	}
	endTurn(t, h, "c1", id)
	waitFor(t, func() bool { return tails.Load() == 2 })
	if calls.count() != 0 {
		t.Errorf("compact calls after the cancel = %d, want 0", calls.count())
	}
}

// A compaction kiro-cli refuses with no turn beside it is dropped and reported once.
func TestAutoCompact_ARefusedTurnEndCompactionIsDroppedAndReported(t *testing.T) {
	h, cs, _, tails := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	calls.refuse = true
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return tails.Load() == 2 && calls.count() == 1 && len(calls.toasts()) == 1 })
	if compactionEntry(t, cs, "c1").ID != "" {
		t.Error("the refused compaction is still listed")
	}
	if got := calls.toasts(); !slices.Equal(got, []string{compactFailed}) {
		t.Errorf("toasts = %v, want one %q", got, compactFailed)
	}
}

// The reader's press while a turn runs is listed for that turn's end and answers queued; one on an idle chat runs
// now and answers accepted; a second press joins the listed one.
func TestCompactRequest_QueuedBehindATurnAndRunAtOnceWhenIdle(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 10)
	id, _ := h.stagePromptTurn(t, "c1")
	for range 2 {
		if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactQueued {
			t.Fatalf("press during the turn = (%v, %v), want queued", out, err)
		}
	}
	if got := runIDs(t, cs, "c1"); !slices.Equal(got, []string{"compaction"}) {
		t.Errorf("dock = %v, want one compaction", got)
	}
	endTurn(t, h, "c1", id)
	waitFor(t, func() bool { return calls.count() == 1 && compactionEntry(t, cs, "c1").ID == "" })

	if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactAccepted {
		t.Errorf("idle press = (%v, %v), want accepted", out, err)
	}
	if calls.count() != 2 {
		t.Errorf("compact calls = %d, want 2", calls.count())
	}
}

// A press on a chat with no live session compacts nothing.
func TestCompactRequest_NoSessionCompactsNothing(t *testing.T) {
	h, _, _ := newTestHub()
	if out, err := h.RequestCompact(t.Context(), "c-none"); err != nil || out != command.CompactNoSession {
		t.Errorf("press with no session = (%v, %v), want no session", out, err)
	}
}

// compactTurn names the open turn while it runs, then the finished one.
func TestCompactTurn_NamesTheOpenTurnThenTheFinishedOne(t *testing.T) {
	h, _, _ := newTestHub()
	if id, live := h.coord.turns.compactTurn("c1"); id != "" || live {
		t.Errorf("before any turn: (%q, %v), want (\"\", false)", id, live)
	}
	first, _ := h.stagePromptTurn(t, "c1")
	if id, live := h.coord.turns.compactTurn("c1"); id != first || !live {
		t.Errorf("during the turn: (%q, %v), want (%q, true)", id, live, first)
	}
	h.AbandonInFlightTurn(t.Context(), "c1", first, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return !h.coord.turns.live("c1") })
	if id, live := h.coord.turns.compactTurn("c1"); id != first || live {
		t.Errorf("after the close: (%q, %v), want (%q, false)", id, live, first)
	}
}

// marotte's own compaction follows the last readable config.json.
func TestUnreadableConfig_MarottesCompactorKeepsTheLastReadablePolicy(t *testing.T) {
	dir := writeSettings(t, `{"auto_compaction_enabled":false,"auto_compact_pct":90}`)
	h := reopenFixtureIn(t, dir, nil)
	settleBoot(t, h)
	rewriteConfigByHand(t, dir, `{"auto_compaction_enabled":`)

	if on, pct := h.coord.autoCompact.policy(t.Context()); on || pct != 90 {
		t.Errorf("compaction policy over an unreadable config.json = (%v, %d), want the last readable (false, 90)", on, pct)
	}
}
