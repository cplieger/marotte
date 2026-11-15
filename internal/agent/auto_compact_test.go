package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// compactCalls counts the hub's compaction calls and answers each accepted (refuse false), refused, or failed with
// err; a call waits on gate when one is set. reading is the stored context reading onCompactor reports; failures
// collects the toasts.
type compactCalls struct {
	err      error
	gate     chan struct{}
	failures []string
	mu       sync.Mutex
	calls    int
	reading  float64
	refuse   bool
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

func (c *compactCalls) setRefuse(refuse bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuse = refuse
}

func (c *compactCalls) setReading(pct float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reading = pct
}

// onCompactor points the hub's compactor at a policy of pct, the reading at usage, and a counting compact call.
func onCompactor(h *Runtime, pct int, enabled bool, usage float64) *compactCalls {
	c := countCompactions(h, pct, enabled)
	c.reading = usage
	h.coord.autoCompact.usage = func(context.Context, marotte.ChatID) marotte.Usage {
		c.mu.Lock()
		defer c.mu.Unlock()
		return marotte.Usage{ContextPct: c.reading, HasRealData: c.reading > 0}
	}
	return c
}

// countCompactions points the hub's compactor at a policy of pct and a counting compact call, leaving the reading to
// the chat's stored usage.
func countCompactions(h *Runtime, pct int, enabled bool) *compactCalls {
	c := &compactCalls{}
	ac := h.coord.autoCompact
	ac.policy = func(context.Context) (bool, int) { return enabled, pct }
	ac.compact = func(context.Context, command.CompactBridge) (bool, uint64, error) {
		c.mu.Lock()
		c.calls++
		gate, refuse, err := c.gate, c.refuse, c.err
		c.mu.Unlock()
		if gate != nil {
			<-gate
		}
		return !refuse && err == nil, 0, err
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

// A compaction that did not run at a turn end over the point is attempted again at the next turn's end.
func TestAutoCompact_AFailedCompactionIsAttemptedAgainAtTheNextTurnEnd(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	calls.setRefuse(true)

	runPrompt(t, h, cs, "c1", "m-2")
	waitFor(t, func() bool { return calls.count() == 1 && compactionEntry(t, cs, "c1").ID == "" })
	runPrompt(t, h, cs, "c1", "m-3")

	waitFor(t, func() bool { return calls.count() == 2 })
}

// Every turn end over the point attempts a compaction until one succeeds; the test then sets the reading a success
// leaves (0), which compacts nothing.
func TestAutoCompact_EveryTurnEndOverThePointAttemptsUntilOneSucceeds(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	calls.setRefuse(true)
	for turn := 1; turn <= 3; turn++ {
		runPrompt(t, h, cs, "c1", "m-fail-"+string(rune('0'+turn)))
		waitFor(t, func() bool { return calls.count() == turn && len(calls.toasts()) == turn })
	}
	calls.setRefuse(false)
	runPrompt(t, h, cs, "c1", "m-ok")
	waitFor(t, func() bool { return calls.count() == 4 })
	calls.setReading(0)

	runPrompt(t, h, cs, "c1", "m-after")

	staysFalse(t, 100*time.Millisecond, func() bool { return calls.count() != 4 }, "a turn end after the success compacted again")
	if got := len(calls.toasts()); got != 3 {
		t.Errorf("toasts = %d, want one per failed attempt (3)", got)
	}
}

// Once the reading is back under the point, a turn end after a failure attempts nothing.
func TestAutoCompact_UnderThePointAfterAFailureAttemptsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.setRefuse(true)
		runPrompt(t, h, cs, "c1", "m-2")
		waitFor(t, func() bool { return calls.count() == 1 })
		calls.setReading(40)

		runPrompt(t, h, cs, "c1", "m-3")
		waitFor(t, func() bool { return !listLive(h, "c1") })
		time.Sleep(2 * time.Hour)
		synctest.Wait()

		if got := calls.count(); got != 1 {
			t.Errorf("calls = %d, want 1: a compaction ran after the reading fell under the point", got)
		}
	})
}

// A failure starts no attempt while the next turn runs; that turn's end does.
func TestAutoCompact_NoAttemptStartsMidTurnAfterAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.setRefuse(true)
		runPrompt(t, h, cs, "c1", "m-2")
		waitFor(t, func() bool { return calls.count() == 1 && compactionEntry(t, cs, "c1").ID == "" })
		open := promptGates(t, br)
		if code := postPrompt(t, h, "parked"); code != 200 {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return h.coord.turns.live("c1") })
		time.Sleep(2 * time.Hour)
		synctest.Wait()

		if got, entry := calls.count(), compactionEntry(t, cs, "c1").ID; got != 1 || entry != "" {
			t.Errorf("calls = %d, listed compaction %q while the turn ran; want 1 and none", got, entry)
		}
		open(0)

		waitFor(t, func() bool { return calls.count() == 2 })
	})
}

// An idle chat two hours after a failure has listed and called nothing more: the next attempt waits for a turn end.
func TestAutoCompact_AnIdleChatAfterAFailureStartsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.setRefuse(true)
		runPrompt(t, h, cs, "c1", "m-2")
		waitFor(t, func() bool { return calls.count() == 1 && !listLive(h, "c1") })

		time.Sleep(2 * time.Hour)
		synctest.Wait()

		if got, entry := calls.count(), compactionEntry(t, cs, "c1").ID; got != 1 || entry != "" {
			t.Errorf("calls = %d, listed compaction %q after two idle hours; want 1 and none", got, entry)
		}
	})
}

// The retry needs no state of its own: a runtime restarted between the failure and the next turn end reads the
// stored reading at that end and attempts.
func TestAutoCompact_ARestartBetweenTheFailureAndTheNextTurnEndStillAttempts(t *testing.T) {
	cs := newTestChatStore()
	boot := func() *Runtime {
		h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession {
			br := newFakeBridge()
			br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
			return br
		}, nil), cs)
		cs.wire(h)
		settleBoot(t, h)
		return h
	}
	before := boot()
	seedQueue(t, cs, "c1")
	runPrompt(t, before, cs, "c1", "m-first")
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Usage.ContextPct, c.Usage.HasRealData = 95, true
		return true
	}); err != nil {
		t.Fatalf("Setup: store the reading: %v", err)
	}
	first := countCompactions(before, 90, true)
	first.setRefuse(true)
	runPrompt(t, before, cs, "c1", "m-2")
	waitFor(t, func() bool { return first.count() == 1 })
	shutdownHub(t, before)

	after := boot()
	t.Cleanup(func() { shutdownHub(t, after) })
	calls := countCompactions(after, 90, true)
	runPrompt(t, after, cs, "c1", "m-3")

	waitFor(t, func() bool { return calls.count() == 1 })
}

// The reader's cancel of a listed compaction takes it off and the turn's end compacts nothing unasked; the next
// turn's end, still over the point, attempts again.
func TestAutoCompact_ACancelEndsOnlyThatAttempt(t *testing.T) {
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

	runPrompt(t, h, cs, "c1", "m-next")

	waitFor(t, func() bool { return calls.count() == 1 })
}

// The reader's cancel of a running compaction ends that call with no toast; the next turn's end, still over the
// point, attempts again.
func TestAutoCompact_ACancelledRunningCompactionIsAttemptedAgainAtTheNextTurnEnd(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	gate := make(chan struct{})
	calls.gate, calls.refuse = gate, true
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	pressed := make(chan struct{})
	go func() {
		defer close(pressed)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	waitFor(t, func() bool { return calls.count() == 1 })
	c := compactionEntry(t, cs, "c1")
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdCancelCompact, ChatID: "c1", Payload: json.RawMessage(`{"id":"` + c.ID + `"}`),
	})
	if rec.Code != 200 {
		t.Fatalf("cancel_compact = %d %s", rec.Code, rec.Body.String())
	}
	calls.mu.Lock()
	calls.gate = nil
	calls.mu.Unlock()
	release()
	<-pressed
	waitFor(t, func() bool { return compactionEntry(t, cs, "c1").ID == "" })
	staysFalse(t, 100*time.Millisecond, func() bool { return len(calls.toasts()) != 0 }, "the cancelled call raised a toast")
	if got := failedRows(t, cs, "c1"); len(got) != 0 {
		t.Errorf("rows after the cancel = %q, want none", got)
	}

	runPrompt(t, h, cs, "c1", "m-next")

	waitFor(t, func() bool { return calls.count() == 2 && len(calls.toasts()) == 1 })
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
	if got, want := calls.toasts(), compactFailedDetail(false, true, compactNotRunCauses); !slices.Equal(got, []string{want}) {
		t.Errorf("toasts = %v, want one %q", got, want)
	}
}

// A queued press refused on a chat under the point says nothing of a retry: no turn end will try again.
func TestAutoCompact_AFailedPressUnderThePointSaysNothingOfARetry(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 30)
	calls.setRefuse(true)
	id, _ := h.stagePromptTurn(t, "c1")
	if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactQueued {
		t.Fatalf("Setup: press during the turn = (%v, %v), want queued", out, err)
	}

	endTurn(t, h, "c1", id)

	waitFor(t, func() bool { return len(calls.toasts()) == 1 && compactionEntry(t, cs, "c1").ID == "" })
	if got, want := calls.toasts(), compactFailedDetail(false, false, compactNotRunCauses); !slices.Equal(got, []string{want}) {
		t.Errorf("toasts = %v, want one %q", got, want)
	}
}

// The page clips a toast past its MAX_TOAST_CHARS, so a longer compaction toast would lose its last words, the retry
// sentence among them.
func TestCompactionToasts_FitThePagesToastCap(t *testing.T) {
	const page = "../../static-src/failure-notice.ts"
	src, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read %s: %v", page, err)
	}
	m := regexp.MustCompile(`const MAX_TOAST_CHARS = (\d+);`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s declares no `const MAX_TOAST_CHARS = <n>;`", page)
	}
	capChars, _ := strconv.Atoi(string(m[1]))
	toasts := map[string]string{
		"resume limit":   resumeLimitNotice,
		"resume refused": resumeRefusedNotice,
	}
	for _, cause := range []string{compactNotRunCauses, compactCallFailedCause} {
		toasts["failed: "+cause] = compactFailedDetail(false, false, cause)
		toasts["failed, retrying: "+cause] = compactFailedDetail(false, true, cause)
		toasts["failsafe failed: "+cause] = compactFailedDetail(true, false, cause)
		toasts["failsafe failed, retrying: "+cause] = compactFailedDetail(true, true, cause)
	}
	retrying := &autoCompactor{
		policy: func(context.Context) (bool, int) { return true, 90 },
		usage: func(context.Context, marotte.ChatID) marotte.Usage {
			return marotte.Usage{ContextPct: 95, HasRealData: true}
		},
	}
	// kiro-cli's longest reason: translate bounds it to 200 bytes and marks the cut past them.
	for name, reason := range map[string]string{
		"ascii":      strings.Repeat("x", 200) + "...",
		"multi-byte": strings.Repeat("é", 100) + "...",
	} {
		row := retrying.failureReason(t.Context(), "c1", reason)
		toasts["row of kiro-cli's longest reason, "+name] = row
		if !strings.HasSuffix(row, ". "+command.CompactRetryNote+".") || !utf8.ValidString(row) {
			t.Errorf("%s row = %q, want valid text ending in the whole retry sentence", name, row)
		}
	}
	for name, text := range toasts {
		if n := len([]rune(text)); n > capChars {
			t.Errorf("%s toast is %d characters, over the page's %d: %q", name, n, capChars, text)
		}
	}
}

// A press refused over the point answers that the next turn end tries again, and it does; under the point it
// answers a plain refusal, and the next turn end attempts nothing.
func TestCompactRequest_AFailedPressIsAttemptedAgainAtTheNextTurnEnd(t *testing.T) {
	t.Run("over the point", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.setRefuse(true)

		out, err := h.RequestCompact(t.Context(), "c1")

		if err != nil || out != command.CompactRefusedRetrying {
			t.Errorf("press = (%v, %v), want refused, retrying", out, err)
		}
		runPrompt(t, h, cs, "c1", "m-2")
		waitFor(t, func() bool { return calls.count() == 2 })
	})
	t.Run("under the point", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 30)
		calls.setRefuse(true)

		out, err := h.RequestCompact(t.Context(), "c1")

		if err != nil || out != command.CompactRefused {
			t.Errorf("press = (%v, %v), want refused", out, err)
		}
		runPrompt(t, h, cs, "c1", "m-2")
		staysFalse(t, 100*time.Millisecond, func() bool { return calls.count() != 1 }, "a turn end under the point compacted")
	})
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

// logRecords is the captured JSON log records whose msg is msg.
func logRecords(t *testing.T, logs *logCapture, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// Each failed attempt logs one WARN with its reason and whether the next turn end tries again; a cancelled call logs
// none.
func TestCompaction_AFailedAttemptLogsWarnWithItsReason(t *testing.T) {
	t.Run("a refusal", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.setRefuse(true)

		runPrompt(t, h, cs, "c1", "m-2")
		waitFor(t, func() bool { return len(logRecords(t, logs, "compaction did not run")) == 1 })

		rec := logRecords(t, logs, "compaction did not run")[0]
		if rec["level"] != "WARN" {
			t.Errorf("level = %v, want WARN", rec["level"])
		}
		if r, _ := rec["reason"].(string); !strings.HasPrefix(r, "refused") {
			t.Errorf("reason = %q, want one starting refused", r)
		}
		if rec["retry_at_turn_end"] != true {
			t.Errorf("retry_at_turn_end = %v, want true over the point", rec["retry_at_turn_end"])
		}
	})
	t.Run("a transport error", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.mu.Lock()
		calls.err = errors.New("the pipe broke")
		calls.mu.Unlock()

		runPrompt(t, h, cs, "c1", "m-2")
		waitFor(t, func() bool { return len(logRecords(t, logs, "compaction did not run")) == 1 })

		rec := logRecords(t, logs, "compaction did not run")[0]
		if rec["level"] != "WARN" || rec["reason"] != "the pipe broke" {
			t.Errorf("record = %v, want WARN with reason %q", rec, "the pipe broke")
		}
	})
	t.Run("a cancelled call", func(t *testing.T) {
		logs := captureLogs(t)
		h, _, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		gate := make(chan struct{})
		calls.gate, calls.refuse = gate, true
		release := sync.OnceFunc(func() { close(gate) })
		t.Cleanup(release)
		pressed := make(chan struct{})
		go func() {
			defer close(pressed)
			_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
		}()
		waitFor(t, func() bool { return calls.count() == 1 })
		if err := h.coord.autoCompact.cancel(t.Context(), "c1", ""); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		release()
		<-pressed
		waitFor(t, func() bool { return len(logRecords(t, logs, "compaction cancelled while its call ran")) == 1 })

		for _, rec := range logRecords(t, logs, "compaction did not run") {
			t.Errorf("a cancelled call logged %v, want no WARN", rec)
		}
	})
}

// retryWording is the retry sentence (command.CompactRetryNote) a failure over the point carries.
const retryWording = "tries again at the end of the next turn"

// retryingRow is the row of a failure over the point whose reason is cause.
func retryingRow(cause string) string { return cause + ". " + command.CompactRetryNote + "." }

// failedRows is the reasons of the chat's compaction_failed entries, in log order.
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

// A refused attempt leaves one durable "Compaction failed" row; over the point it says the next turn end tries
// again, under it it does not.
func TestAutoCompact_ARefusalLeavesOneFailedRow(t *testing.T) {
	t.Run("over the point", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.setRefuse(true)

		runPrompt(t, h, cs, "c1", "m-2")

		waitFor(t, func() bool { return calls.count() == 1 && len(failedRows(t, cs, "c1")) == 1 })
		if got, want := failedRows(t, cs, "c1")[0], retryingRow(compactNotRunCauses); got != want {
			t.Errorf("row reason = %q, want %q", got, want)
		}
	})
	t.Run("a call that errored", func(t *testing.T) {
		const cause = "the call to kiro-cli failed or timed out"
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 95)
		calls.mu.Lock()
		calls.err = errors.New("the pipe broke")
		calls.mu.Unlock()

		runPrompt(t, h, cs, "c1", "m-2")

		waitFor(t, func() bool { return len(failedRows(t, cs, "c1")) == 1 && len(calls.toasts()) == 1 })
		if got, want := failedRows(t, cs, "c1")[0], retryingRow(cause); got != want {
			t.Errorf("row reason = %q, want %q", got, want)
		}
		if got := calls.toasts()[0]; !strings.Contains(got, cause) || strings.Contains(got, compactNotRunCauses) {
			t.Errorf("toast = %q, want it to name %q, not the refusal's causes", got, cause)
		}
	})
	t.Run("under the point", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 30)
		calls.setRefuse(true)

		if out, _ := h.RequestCompact(t.Context(), "c1"); out != command.CompactRefused {
			t.Fatalf("Setup: press = %v, want refused", out)
		}

		waitFor(t, func() bool { return len(failedRows(t, cs, "c1")) == 1 })
		if got := failedRows(t, cs, "c1"); strings.Contains(got[0], retryWording) {
			t.Errorf("row reason = %q, want no retry under the point", got[0])
		}
	})
}

// A summary kiro-cli reports failed, then refuses, is one attempt: one row, with kiro-cli's reason and the retry.
func TestAutoCompact_ASummaryKiroCliReportsFailedLeavesOneRow(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	ac := h.coord.autoCompact
	ac.policy = func(context.Context) (bool, int) { return true, 90 }
	ac.usage = func(context.Context, marotte.ChatID) marotte.Usage {
		return marotte.Usage{ContextPct: 95, HasRealData: true}
	}
	br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":false}`))
	br.mu.Lock()
	br.notifsOnCall = map[string][]*marotte.RPCResponse{marotte.MethodSessionCompact: {
		newSessionInfoMsg(map[string]any{"summarization": map[string]any{"status": "model error"}}),
	}}
	br.mu.Unlock()

	if out, _ := h.RequestCompact(t.Context(), "c1"); out != command.CompactRefusedRetrying {
		t.Fatalf("Setup: press = %v, want refused, retrying", out)
	}

	waitFor(t, func() bool { return len(failedRows(t, cs, "c1")) >= 1 })
	staysFalse(t, 100*time.Millisecond, func() bool { return len(failedRows(t, cs, "c1")) != 1 },
		"the attempt left more than one row")
	if got, want := failedRows(t, cs, "c1")[0], retryingRow("model error"); got != want {
		t.Errorf("row reason = %q, want %q", got, want)
	}
}

// An attempt the chat's close ended leaves no row and no toast, and lists no attempt for a kiro-cli turn that
// closed beside it: no later turn end of that life tries again.
func TestAutoCompact_AnAttemptTheChatsCloseEndedLeavesNoRowAndListsNothing(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	calls.err = marotte.ErrChatClosedDuringCall
	var once sync.Once
	prev := testHookAfterCompactionReturn
	t.Cleanup(func() { testHookAfterCompactionReturn = prev })
	testHookAfterCompactionReturn = func(marotte.ChatID) {
		once.Do(func() {
			wireOpen(t, cs, br)
			br.deliver(newChunkMsg("kiro-cli on its own"))
			br.deliver(newTurnEndMsg("end_turn"))
			waitFor(t, func() bool { return !slices.Contains(runStates(t, cs, "c1"), "wire running") })
		})
	}

	runPrompt(t, h, cs, "c1", "m-2")

	waitFor(t, func() bool { return calls.count() == 1 && !listLive(h, "c1") })
	staysFalse(t, 100*time.Millisecond, func() bool {
		return len(failedRows(t, cs, "c1")) != 0 || len(calls.toasts()) != 0 || calls.count() != 1
	}, "the closed chat's attempt left a row or a toast, or listed the turn end beside it")
}

// An attempt requeued behind kiro-cli's turn after kiro-cli reported its summary failed is a new attempt: its own
// bare refusal leaves its own row, after kiro-cli's.
func TestAutoCompact_ARequeuedAttemptAfterAReportedFailureLeavesItsOwnRow(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	release := holdCall(t, br, marotte.MethodSessionCompact)
	h.coord.autoCompact.compact = func(ctx context.Context, b command.CompactBridge) (bool, uint64, error) {
		_, _, _ = b.CallAt(ctx, marotte.MethodSessionCompact, map[string]any{})
		calls.mu.Lock()
		defer calls.mu.Unlock()
		calls.calls++
		return false, 0, nil
	}
	out := make(chan command.CompactOutcome, 1)
	go func() {
		o, _ := h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
		out <- o
	}()
	waitForCall(t, br, marotte.MethodSessionCompact)
	br.deliver(newTurnStartMsg())
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
	br.deliver(newSessionInfoMsg(map[string]any{"summarization": map[string]any{"status": "model error"}}))
	waitFor(t, func() bool { return len(failedRows(t, cs, "c1")) == 1 })
	release()
	if got := <-out; got != command.CompactQueued {
		t.Fatalf("Setup: /compact answered %v, want queued behind kiro-cli's turn", got)
	}

	br.deliver(newTurnEndMsg("end_turn"))

	waitFor(t, func() bool { return calls.count() == 2 && !listLive(h, "c1") })
	staysFalse(t, 100*time.Millisecond, func() bool { return len(failedRows(t, cs, "c1")) > 2 },
		"the two attempts left more than two rows")
	got := failedRows(t, cs, "c1")
	if len(got) != 2 {
		t.Fatalf("rows = %q, want kiro-cli's then the requeued attempt's", got)
	}
	if !strings.Contains(got[0], "model error") {
		t.Errorf("first row = %q, want kiro-cli's reason", got[0])
	}
	if want := retryingRow(compactNotRunCauses); got[1] != want {
		t.Errorf("second row = %q, want %q", got[1], want)
	}
}

// withCompactionRowHook runs fn once, the first time a failed compaction's row is about to be written.
func withCompactionRowHook(t *testing.T, fn func(marotte.ChatID)) {
	t.Helper()
	var once sync.Once
	prev := testHookBeforeCompactionRow
	testHookBeforeCompactionRow = func(chatID marotte.ChatID) { once.Do(func() { fn(chatID) }) }
	t.Cleanup(func() { testHookBeforeCompactionRow = prev })
}

// A message sent as a refused compaction ends goes behind that compaction's row: the attempt's "Compaction failed"
// mark sits ahead of the message's turn, never inside it.
func TestAutoCompact_AFailedRowLandsAheadOfTheTurnQueuedBehindIt(t *testing.T) {
	h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	calls.setRefuse(true)
	withCompactionRowHook(t, func(chatID marotte.ChatID) {
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: chatID, Payload: json.RawMessage(`{"text":"sent as it ends","message_id":"m-behind"}`),
		})
		if rec.Code != 200 {
			t.Errorf("Setup: prompt = %d %s", rec.Code, rec.Body.String())
		}
		// Long enough for a turn the wake started to open before the row is written.
		deadline := time.Now().Add(300 * time.Millisecond)
		for !h.coord.turns.live(chatID) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})

	if out, _ := h.RequestCompact(t.Context(), "c1"); out != command.CompactRefusedRetrying {
		t.Fatalf("Setup: press = %v, want refused, retrying", out)
	}

	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-behind") && !listLive(h, "c1") })
	row, open := -1, -1
	for i, e := range logOf(t, cs, "c1") {
		switch e.Kind {
		case marotte.EntryKindCompactionFailed:
			if row < 0 {
				row = i
			}
		case marotte.EntryKindTurnOpen:
			var o marotte.EntryTurnOpen
			decodePayload(t, e, &o)
			if o.Prompt != nil && o.Prompt.ID == "m-behind" {
				open = i
			}
		}
	}
	if row < 0 || open < 0 || row > open {
		t.Errorf("first row at %d, m-behind's turn opens at %d; want the row first", row, open)
	}
}

// attemptSlot is a point in a failed compaction attempt where a kiro-cli turn's open or close lands.
type attemptSlot int

const (
	// slotTaken: the attempt is taken, before its start.
	slotTaken attemptSlot = iota
	// slotCall: its call returned, before its end is settled.
	slotCall
	// slotRowDue: it owes its row, before the row is written.
	slotRowDue
	// slotRowWritten: its row is written, before its end is applied.
	slotRowWritten
	// slotSettled: its end is applied.
	slotSettled
)

var attemptSlotNames = [...]string{"taken", "call", "row_due", "row_written", "settled"}

// orderAllowed reports whether a turn can open at open and close at shut: a turn open before the start puts the
// attempt back until it closed, and a bare refusal with a turn open beside its call is listed again, owing no row.
func orderAllowed(err error, open, shut attemptSlot) bool {
	switch {
	case open == slotTaken:
		return shut == slotTaken
	case err == nil && open == slotCall:
		return shut == slotCall || shut == slotSettled
	}
	return true
}

// A kiro-cli turn that opens and closes at any point a failed attempt allows, beside a message queued behind that
// attempt: every turn end over the point is followed by exactly one attempt, no attempt starts while a turn is live,
// and the message's turn opens only after the attempts ahead of it wrote their rows.
func TestAutoCompact_EveryOrderOfATurnBesideAFailedAttemptKeepsItsTurnEndAttempt(t *testing.T) {
	outcomes := []struct {
		err  error
		name string
	}{{nil, "refused"}, {errors.New("the pipe broke"), "errored"}}
	for _, oc := range outcomes {
		for open := range slotSettled {
			for shut := open; shut <= slotSettled; shut++ {
				if !orderAllowed(oc.err, open, shut) {
					continue
				}
				name := oc.name + "_open_" + attemptSlotNames[open] + "_close_" + attemptSlotNames[shut]
				t.Run(name, func(t *testing.T) { turnBesideAFailedAttempt(t, oc.err, open, shut) })
			}
		}
	}
}

func turnBesideAFailedAttempt(t *testing.T, callErr error, open, shut attemptSlot) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 95)
	calls.setRefuse(true)
	calls.err = callErr
	var (
		mu     sync.Mutex
		events []string
	)
	note := func(ev string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}
	count := h.coord.autoCompact.compact
	h.coord.autoCompact.compact = func(ctx context.Context, s command.CompactBridge) (bool, uint64, error) {
		if h.coord.turns.live("c1") {
			note("call mid-turn")
		} else {
			note("call")
		}
		return count(ctx, s)
	}
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if !ended {
			note("close")
		}
	})
	wireLive := func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") }
	act := func(slot attemptSlot) {
		if slot == slotTaken {
			rec := postCmd(t, h, marotte.ClientCommand{
				Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"queued","message_id":"m-queued"}`),
			})
			if rec.Code != 200 {
				t.Errorf("Setup: prompt = %d %s", rec.Code, rec.Body.String())
			}
		}
		if slot == open {
			br.deliver(newTurnStartMsg())
			waitFor(t, wireLive)
		}
		if slot == shut {
			// A wire turn that emitted nothing closes unclean, which would pause m-queued.
			br.deliver(newChunkMsg("kiro-cli on its own"))
			br.deliver(newTurnEndMsg("end_turn"))
			waitFor(t, func() bool { return !wireLive() })
		}
	}
	hooks := []*func(marotte.ChatID){
		&testHookBeforeCompactionStart, &testHookAfterCompactionReturn,
		&testHookBeforeCompactionRow, &testHookAfterCompactionRow,
	}
	for i, hook := range hooks {
		var once sync.Once
		prev := *hook
		*hook = func(marotte.ChatID) { once.Do(func() { act(attemptSlot(i)) }) }
		t.Cleanup(func() { *hook = prev })
	}

	_, _ = h.RequestCompact(t.Context(), "c1")
	act(slotSettled)

	done := func() bool {
		mu.Lock()
		last := len(events) > 0 && strings.HasPrefix(events[len(events)-1], "call")
		mu.Unlock()
		return last && slices.Contains(promptIDs(t, cs, "c1"), "m-queued") && !listLive(h, "c1")
	}
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			mu.Lock()
			got := slices.Clone(events)
			mu.Unlock()
			t.Fatalf("the run did not settle: events %v, list %v, prompts %v", got, runStates(t, cs, "c1"),
				promptIDs(t, cs, "c1"))
		}
	}
	mu.Lock()
	got := slices.Clone(events)
	mu.Unlock()
	if slices.Contains(got, "call mid-turn") {
		t.Errorf("an attempt started while a turn was live: %v", got)
	}
	for i, ev := range got {
		if ev != "close" {
			continue
		}
		next := len(got)
		if j := slices.Index(got[i+1:], "close"); j >= 0 {
			next = i + 1 + j
		}
		if n := next - i - 1; n != 1 {
			t.Errorf("the turn end at event %d was followed by %d attempts, want 1: %v", i, n, got)
		}
	}
	rowsBefore, rowsInside, inside := 0, 0, ""
	for _, e := range logOf(t, cs, "c1") {
		switch {
		case e.Kind == marotte.EntryKindTurnOpen:
			var o marotte.EntryTurnOpen
			decodePayload(t, e, &o)
			if o.Prompt != nil && o.Prompt.ID == "m-queued" {
				inside = e.Turn
			}
		case e.Kind == marotte.EntryKindTurnClose && e.Turn == inside:
			inside = "-"
		case e.Kind == marotte.EntryKindCompactionFailed && inside == "":
			rowsBefore++
		case e.Kind == marotte.EntryKindCompactionFailed && e.Turn == inside:
			rowsInside++
		}
	}
	if rowsBefore == 0 || rowsInside != 0 {
		t.Errorf("rows before the queued turn = %d, inside it = %d; want at least 1 before and none inside",
			rowsBefore, rowsInside)
	}
}

// A cancel of a running call declines the turn ends that came before it, never one after: a kiro-cli turn that
// closed beside the call before the cancel gets no attempt when the call ends, one that closed after it does.
func TestAutoCompact_ACancelDeclinesOnlyTheTurnEndsBeforeIt(t *testing.T) {
	cases := []struct {
		name        string
		closeFirst  bool
		wantAttempt bool
	}{
		{"a_turn_end_before_the_cancel", true, false},
		{"a_turn_end_after_the_cancel", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
			calls := onCompactor(h, 90, true, 95)
			gate := make(chan struct{})
			calls.gate, calls.refuse = gate, true
			release := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(release)
			pressed := make(chan struct{})
			go func() {
				defer close(pressed)
				_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
			}()
			waitFor(t, func() bool { return calls.count() == 1 })
			id := compactionEntry(t, cs, "c1").ID
			wireTurn := func() {
				wireOpen(t, cs, br)
				br.deliver(newChunkMsg("kiro-cli on its own"))
				br.deliver(newTurnEndMsg("end_turn"))
				waitFor(t, func() bool { return !slices.Contains(runStates(t, cs, "c1"), "wire running") })
			}
			if tc.closeFirst {
				wireTurn()
			}
			rec := postCmd(t, h, marotte.ClientCommand{
				Type: marotte.CmdCancelCompact, ChatID: "c1", Payload: json.RawMessage(`{"id":"` + id + `"}`),
			})
			if rec.Code != 200 {
				t.Fatalf("Setup: cancel_compact = %d %s", rec.Code, rec.Body.String())
			}
			if !tc.closeFirst {
				wireTurn()
			}
			calls.mu.Lock()
			calls.gate = nil
			calls.mu.Unlock()
			release()
			<-pressed

			if tc.wantAttempt {
				waitFor(t, func() bool { return calls.count() == 2 })
				return
			}
			waitFor(t, func() bool { return !listLive(h, "c1") })
			staysFalse(t, 200*time.Millisecond, func() bool { return calls.count() != 1 },
				"the call cancelled after the turn end was followed by an attempt")
		})
	}
}
