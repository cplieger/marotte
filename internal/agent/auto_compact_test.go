package agent

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

type compactRig struct {
	ac     *autoCompactor
	bridge *fakeBridge
	block  chan struct{}
	// onCompact runs inside each compact call, before it answers.
	onCompact func()
	failures  []string
	usage     marotte.Usage
	calls     int
	marks     int
	opened    int
	// finished is the id of the turn that finished last, "" before any.
	finished string
	pct      int
	enabled  bool
	live     bool
	// hold is a close's KAS hold with no turn live, under which a press is queued for the release.
	hold   bool
	refuse bool
	queued bool
	mu     sync.Mutex
}

// rigQueue is the record's row as a flag. r.mu is its section, as the lifecycle is the record queue's; marks counts
// each time the row goes on.
type rigQueue struct{ r *compactRig }

func (q rigQueue) section(_ context.Context, _ marotte.ChatID, fn func(t queueTurn, row func(bool) error) error) error {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	t := queueTurn{id: q.r.turnIDLocked(), live: q.r.live, holds: q.r.hold, open: true}
	return fn(t, func(on bool) error {
		if on && !q.r.queued {
			q.r.marks++
		}
		q.r.queued = on
		return nil
	})
}

func newCompactRig(pct int) *compactRig {
	r := &compactRig{enabled: true, pct: pct, bridge: newFakeBridge()}
	entry := testBridge(r.bridge, bridgeIdle)
	r.ac = &autoCompactor{
		chats: map[marotte.ChatID]*autoCompactState{},
		turnOf: func(marotte.ChatID) (string, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.turnIDLocked(), r.live
		},
		bridgeOf: func(marotte.ChatID) *sharedBridge { return entry },
		usage: func(context.Context, marotte.ChatID) marotte.Usage {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.usage
		},
		policy: func(context.Context) (bool, int) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.enabled, r.pct
		},
		compact: func(ctx context.Context, _ command.CompactBridge) (bool, uint64, error) {
			r.mu.Lock()
			r.calls++
			block, onCompact := r.block, r.onCompact
			r.mu.Unlock()
			if onCompact != nil {
				onCompact()
			}
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return false, 0, ctx.Err()
				}
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.refuse {
				// The translator clears the stored reading when the summary lands.
				r.usage = marotte.Usage{}
			}
			return !r.refuse, 0, nil
		},
		failed: func(_ context.Context, _ marotte.ChatID, detail string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.failures = append(r.failures, detail)
		},
	}
	r.ac.queue = rigQueue{r: r}
	return r
}

// setLive opens a turn (a new id) when the chat goes live, or finishes the live one.
func (r *compactRig) setLive(live bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case live && !r.live:
		r.opened++
	case !live && r.live:
		r.finished = r.turnIDLocked()
	}
	r.live = live
}

// turnIDLocked is the newest opened turn's id, which is the live one while a turn is live and the finished one
// otherwise; "" before any turn. r.mu must be held.
func (r *compactRig) turnIDLocked() string {
	if r.opened == 0 {
		return ""
	}
	return "t" + strconv.Itoa(r.opened)
}

// lastTurn is the id turnEnd hands the compactor, as closeFacts does: the turn that finished last, even when the next
// one is already live.
func (r *compactRig) lastTurn() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

func (r *compactRig) setRefuse(refuse bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse = refuse
}

func (r *compactRig) reading(pct float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage = marotte.Usage{ContextPct: pct, HasRealData: true}
}

func (r *compactRig) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *compactRig) markCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.marks
}

func (r *compactRig) isQueued() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queued
}

// pressUnderHold is the reader's press under a close's KAS hold with no turn live, which queues it for the release.
func (r *compactRig) pressUnderHold(t *testing.T) {
	t.Helper()
	setHold := func(on bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.hold = on
	}
	setHold(true)
	defer setHold(false)
	if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactQueued {
		t.Errorf("a press under the close's hold = (%v, %v), want queued", out, err)
	}
}

// turn models one turn in which KAS reports each reading in order; the turn stays live until closed.
func (r *compactRig) turn(t *testing.T, readings ...float64) {
	t.Helper()
	r.setLive(true)
	for _, pct := range readings {
		r.reading(pct)
		r.ac.noteUsage(t.Context(), "c1", pct)
	}
}

// closeTurn ends the live turn: the turn-end trigger runs on the now idle chat.
func (r *compactRig) closeTurn(t *testing.T, contextLimit bool) {
	t.Helper()
	r.finishTurn()
	r.turnEnd(t, contextLimit)
}

// finishTurn closes the live turn without running its end's compaction yet.
func (r *compactRig) finishTurn() { r.setLive(false) }

// turnEnd runs the turn-end trigger for the turn that finished last.
func (r *compactRig) turnEnd(t *testing.T, contextLimit bool) {
	t.Helper()
	r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), contextLimit)
}

// kasOpensTheNextTurn closes the live turn and opens the next before the closed turn's end ran.
func (r *compactRig) kasOpensTheNextTurn() {
	r.setLive(false)
	r.setLive(true)
}

func TestAutoCompact_CrossingThePointMidTurnQueuesOne(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 60, 89.9)
	if r.isQueued() {
		t.Fatal("a reading below the point queued a compaction")
	}
	r.turn(t, 90.2, 94, 97)
	if !r.isQueued() || r.markCount() != 1 || r.count() != 0 {
		t.Fatalf("after crossing 90 mid-turn: queued %v, marks %d, calls %d; want queued once, no call yet",
			r.isQueued(), r.markCount(), r.count())
	}
	r.closeTurn(t, false)
	if r.count() != 1 || r.isQueued() {
		t.Errorf("at the turn's end: calls %d, queued %v; want 1 call and the row gone", r.count(), r.isQueued())
	}
}

// A production sequence, at pct 90: turn 72 reaches 91.7311; turn 73, 4.6 hours
// long, grows the context back from the summary to 93.5573, and KAS starts turn 74 on its own while that close's
// compaction runs; turn 74 reaches the limit. Each crossing queues, whatever the last compaction freed.
func TestAutoCompact_TheProductionSequenceQueuesAgainAfterRegrowth(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 88, 91.7311)
	r.closeTurn(t, false)
	if r.count() != 1 || r.isQueued() {
		t.Fatalf("turn 72: calls %d, queued %v; want its compaction run", r.count(), r.isQueued())
	}
	r.turn(t, 40, 75, 93.5573)
	if !r.isQueued() {
		t.Fatal("turn 73 crossed 90 again after a compaction and queued nothing")
	}
	r.setRefuse(true)
	r.onCompact = func() { r.setLive(true) }
	r.closeTurn(t, false)
	if r.count() != 2 || !r.isQueued() || len(r.failures) != 0 {
		t.Fatalf("turn 73's end, aborted by KAS's own turn: calls %d, queued %v, failures %v; want 2 calls and still queued",
			r.count(), r.isQueued(), r.failures)
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.turn(t, 100)
	r.closeTurn(t, true)
	if r.count() != 3 || r.isQueued() {
		t.Errorf("turn 74's end: calls %d, queued %v; want 3 calls and the row gone", r.count(), r.isQueued())
	}
}

// A compaction that leaves the context above the point never disables the next: the next turn's crossing queues.
func TestAutoCompact_ACompactionThatFreedLittleNeverDisablesTheNext(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 91)
	r.closeTurn(t, false)
	r.turn(t, 89, 91)
	r.closeTurn(t, false)
	r.turn(t, 92)
	r.closeTurn(t, false)
	if got := r.count(); got != 3 {
		t.Errorf("three turns crossing 90 made %d compact calls, want 3", got)
	}
}

// One run per turn end, whatever queued it: a crossing, the reader's request and a context-limit close together.
func TestAutoCompact_OneRunPerTurnEnd(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 95)
	if out, _ := r.ac.request(t.Context(), "c1"); out != command.CompactQueued {
		t.Fatalf("the reader's request during the turn = %v, want queued", out)
	}
	r.closeTurn(t, true)
	if got := r.count(); got != 1 {
		t.Errorf("one turn end made %d compact calls, want 1", got)
	}
}

// The turn's last reading can fold after its close, so the end compacts at the point even with nothing queued.
func TestAutoCompact_TheTurnEndCompactsAtThePointWithNothingQueued(t *testing.T) {
	r := newCompactRig(90)
	r.reading(93)
	r.closeTurn(t, false)
	r.reading(80)
	r.closeTurn(t, false)
	if got := r.count(); got != 1 {
		t.Errorf("closes at 93 then 80 with pct 90 made %d compact calls, want 1", got)
	}
}

func TestAutoCompact_AContextLimitTurnEndCompactsWhateverTheReading(t *testing.T) {
	r := newCompactRig(90)
	r.reading(50)
	r.closeTurn(t, true)
	if got := r.count(); got != 1 {
		t.Errorf("a context-limit close with a stale reading of 50 made %d compact calls, want 1", got)
	}
}

func TestAutoCompact_TheSwitchOffCompactsOnlyWhatTheReaderAsks(t *testing.T) {
	r := newCompactRig(90)
	r.enabled = false
	r.turn(t, 99)
	r.closeTurn(t, true)
	if r.count() != 0 || r.markCount() != 0 {
		t.Fatalf("switch off: calls %d, marks %d; want none", r.count(), r.markCount())
	}
	r.turn(t)
	if out, _ := r.ac.request(t.Context(), "c1"); out != command.CompactQueued {
		t.Fatalf("the reader's request = %v, want queued", out)
	}
	r.closeTurn(t, false)
	if got := r.count(); got != 1 {
		t.Errorf("the reader's queued compaction with the switch off made %d calls, want 1", got)
	}
}

// The reader's cancel holds for the turn it was made in: no later reading re-queues it and its end compacts nothing.
// The next turn's crossing queues again.
func TestAutoCompact_ACancelFromTheHoldingAreaHoldsForTheTurn(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	if err := r.ac.cancel(t.Context(), "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if r.isQueued() {
		t.Fatal("the cancelled compaction is still queued")
	}
	r.turn(t, 95, 97)
	r.closeTurn(t, false)
	if r.count() != 0 || r.isQueued() {
		t.Fatalf("after a cancel: calls %d, queued %v; want neither for this turn", r.count(), r.isQueued())
	}
	r.turn(t, 98)
	if !r.isQueued() {
		t.Fatal("the next turn's crossing queued nothing after a cancel")
	}
	r.closeTurn(t, false)
	if got := r.count(); got != 1 {
		t.Errorf("the next turn's end made %d compact calls, want 1", got)
	}
}

// The row still shows between the turn's close and its end's compaction (the close waits for KAS to go idle); a
// cancel there stops that compaction.
func TestAutoCompact_ACancelBeforeTheTurnEndsCompactionStopsIt(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	if err := r.ac.cancel(t.Context(), "c1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	r.turnEnd(t, false)
	if r.count() != 0 || r.isQueued() {
		t.Errorf("a cancel before the turn end's compaction: calls %d, queued %v; want neither", r.count(), r.isQueued())
	}
}

// kasOpensTheNextTurnPaths are the ways a turn KAS opens itself meets the close of the turn before it: the close's
// wait stands down and its turn end never runs, or the turn end runs with the new turn already live.
var kasOpensTheNextTurnPaths = []struct {
	name string
	open func(t *testing.T, r *compactRig)
}{
	{"the close stands down", func(_ *testing.T, r *compactRig) { r.kasOpensTheNextTurn() }},
	{"the turn end meets the new turn", func(t *testing.T, r *compactRig) {
		r.kasOpensTheNextTurn()
		r.turnEnd(t, false)
	}},
}

func TestAutoCompact_ACancelNeverStopsTheNextTurnsCrossing(t *testing.T) {
	for _, p := range kasOpensTheNextTurnPaths {
		t.Run(p.name, func(t *testing.T) {
			r := newCompactRig(90)
			r.turn(t, 92)
			if err := r.ac.cancel(t.Context(), "c1"); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			p.open(t, r)
			r.turn(t, 95, 99)
			if !r.isQueued() {
				t.Error("the next turn crossed 90 and queued nothing: the previous turn's cancel carried over")
			}
			r.closeTurn(t, false)
			if got := r.count(); got != 1 {
				t.Errorf("the next turn's end made %d compact calls, want 1", got)
			}
		})
	}
}

func TestAutoCompact_ACancelNeverStopsTheNextTurnsContextLimitEnd(t *testing.T) {
	for _, p := range kasOpensTheNextTurnPaths {
		t.Run(p.name, func(t *testing.T) {
			r := newCompactRig(90)
			r.turn(t, 92)
			if err := r.ac.cancel(t.Context(), "c1"); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			p.open(t, r)
			r.turn(t, 50)
			r.closeTurn(t, true)
			if got := r.count(); got != 1 {
				t.Errorf("the next turn failed on the context limit and its end made %d compact calls, want 1", got)
			}
		})
	}
}

func TestAutoCompact_AQueuedRunKASRefusesIsDroppedAndReported(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.setRefuse(true)
	r.closeTurn(t, false)
	if r.isQueued() || len(r.failures) != 1 {
		t.Errorf("a queued run refused with no turn: queued %v, failures %v; want dropped and one failure", r.isQueued(), r.failures)
	}
}

// A reading outside a turn (KAS reports the pre-compaction usage as a compaction starts) queues nothing.
func TestAutoCompact_AReadingOutsideATurnQueuesNothing(t *testing.T) {
	r := newCompactRig(90)
	r.ac.noteUsage(t.Context(), "c1", 95)
	if r.markCount() != 0 {
		t.Errorf("an idle reading of 95 made %d marks, want none", r.markCount())
	}
}

func TestAutoCompact_TurnEndSkipsALiveChat(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 95)
	r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), false)
	if got := r.count(); got != 0 {
		t.Errorf("a live chat got %d compact calls at turn end, want 0", got)
	}
}

// KAS sends no reading after a summary lands, so a turn that ends before its first reading finds no reading at all,
// and compacts nothing.
func TestAutoCompact_AReadingLessTurnAfterACompactionCompactsNothing(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 93)
	r.closeTurn(t, false)
	r.turn(t)
	r.closeTurn(t, false)
	if got := r.count(); got != 1 {
		t.Errorf("a reading-less turn right after a compaction made %d compact calls in total, want 1", got)
	}
}

// A close that finds no session runs nothing and keeps the queued compaction, which the next turn's end runs once.
func TestAutoCompact_ACloseWithNoSessionKeepsTheQueuedCompaction(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	withSession := r.ac.bridgeOf
	r.ac.bridgeOf = func(marotte.ChatID) *sharedBridge { return nil }
	r.closeTurn(t, false)
	r.ac.bridgeOf = withSession
	if r.count() != 0 || !r.isQueued() {
		t.Fatalf("a sessionless close: calls %d, queued %v; want no call and the row kept", r.count(), r.isQueued())
	}
	r.turn(t, 95)
	r.closeTurn(t, false)
	if r.count() != 1 || r.isQueued() {
		t.Errorf("the next turn's end: calls %d, queued %v; want 1 call and the row gone", r.count(), r.isQueued())
	}
}

// The reader's compaction on an idle chat takes a request left queued, so no later turn end runs it again.
func TestCompactRequest_AnIdleRunTakesTheQueuedRow(t *testing.T) {
	r := newCompactRig(90)
	r.pressUnderHold(t)
	if out, err := r.ac.request(t.Context(), "c1"); out != command.CompactAccepted || err != nil {
		t.Fatalf("request on an idle chat = %v, %v; want accepted", out, err)
	}
	if r.isQueued() {
		t.Error("the row stayed on the record after the reader's compaction ran")
	}
	r.turn(t, 31)
	r.closeTurn(t, false)
	if got := r.count(); got != 1 {
		t.Errorf("a manual compaction then a turn ending at 31%% made %d compact calls, want 1", got)
	}
}

// A press while the turn end's compaction runs (under the close's KAS hold) is satisfied by that compaction once KAS
// accepts it.
func TestAutoCompact_APressDuringTheTurnEndsRunIsSatisfiedByIt(t *testing.T) {
	r := newCompactRig(90)
	r.reading(93)
	r.onCompact = func() { r.pressUnderHold(t) }
	accepted := r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), false)
	r.onCompact = nil
	if !accepted || r.isQueued() {
		t.Fatalf("a press during the turn end's run: accepted %v, queued %v; want accepted and the row taken by it",
			accepted, r.isQueued())
	}
	r.ac.afterRelease(t.Context(), "c1", accepted)
	if got := r.count(); got != 1 {
		t.Errorf("the close made %d compact calls, want 1", got)
	}
}

// A press under the close's KAS hold after the turn end ran nothing runs once the hold is released, before the drain.
func TestAutoCompact_ARowWrittenUnderTheHoldRunsAtTheRelease(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 50)
	r.setLive(false)
	accepted := r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), false)
	if accepted {
		t.Fatal("a turn end at 50 with pct 90 and nothing queued reported a compaction")
	}
	r.pressUnderHold(t)
	r.ac.afterRelease(t.Context(), "c1", accepted)
	if r.count() != 1 || r.isQueued() {
		t.Errorf("the row written under the hold: calls %d, queued %v; want 1 call and the row gone", r.count(), r.isQueued())
	}
}

// The turn end's compaction is refused with no turn live and the reader presses under the close's hold: the refused
// call satisfies nothing, so the press runs at the release; refused again there, it is dropped and reported.
func TestAutoCompact_APressAfterARefusedTurnEndRunRunsAtTheRelease(t *testing.T) {
	for _, tc := range []struct {
		name         string
		refuseAgain  bool
		wantFailures int
	}{
		{name: "accepted at the release"},
		{name: "refused again at the release", refuseAgain: true, wantFailures: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCompactRig(90)
			r.reading(93)
			r.setRefuse(true)
			accepted := r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), false)
			if accepted {
				t.Errorf("a refused turn-end call reported accepted")
			}
			r.pressUnderHold(t)
			r.setRefuse(tc.refuseAgain)
			r.ac.afterRelease(t.Context(), "c1", accepted)
			if r.count() != 2 || r.isQueued() || len(r.failures) != tc.wantFailures {
				t.Errorf("the press at the release: calls %d, queued %v, failures %v; want 2 calls, the row gone, %d failures",
					r.count(), r.isQueued(), r.failures, tc.wantFailures)
			}
		})
	}
}

// KAS opens its own turn during the turn end's call and refuses it, so the run is queued again for that turn, and
// that turn closes before this close's release: the refused call satisfies nothing, so the release runs the run.
func TestAutoCompact_ARunQueuedAgainForAClosedKASTurnRunsAtTheRelease(t *testing.T) {
	r := newCompactRig(90)
	r.turn(t, 92)
	r.finishTurn()
	r.setRefuse(true)
	r.onCompact = r.kasOpensTheNextTurn
	accepted := r.ac.afterTurn(t.Context(), "c1", r.lastTurn(), false)
	if !r.isQueued() {
		t.Fatalf("not queued again after the refusal under KAS's turn")
	}
	r.setRefuse(false)
	r.onCompact = nil
	r.finishTurn()
	r.ac.afterRelease(t.Context(), "c1", accepted)
	if r.count() != 2 || r.isQueued() || len(r.failures) != 0 {
		t.Errorf("the re-queued run at the release: calls %d, queued %v, failures %v; want 2 calls, the row gone, no failure",
			r.count(), r.isQueued(), r.failures)
	}
}

// After the release the row belongs to a turn that opened meanwhile, whose end runs it.
func TestAutoCompact_ARowUnderANewTurnWaitsForThatTurnsEnd(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		r := newCompactRig(90)
		r.pressUnderHold(t)
		r.setLive(true)
		r.ac.afterRelease(t.Context(), "c1", accepted)
		if r.count() != 0 || !r.isQueued() {
			t.Errorf("close accepted %v, a turn live at the release: calls %d, queued %v; want no call and the row kept",
				accepted, r.count(), r.isQueued())
		}
	}
}

func TestAwaitCompaction_HoldsAPromptUntilTheRunningCompactionReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newCompactRig(90)
		r.block = make(chan struct{})
		r.reading(95)
		closed := make(chan struct{})
		go func() {
			r.ac.afterTurn(context.Background(), "c1", r.lastTurn(), false)
			close(closed)
		}()
		synctest.Wait()
		sent := make(chan struct{})
		go func() {
			r.ac.awaitRunning(context.Background(), "c1")
			close(sent)
		}()
		synctest.Wait()
		select {
		case <-sent:
			t.Fatal("the prompt went ahead while the turn-end compaction was still running")
		default:
		}
		close(r.block)
		<-sent
		<-closed
		if got := r.count(); got != 1 {
			t.Errorf("compact calls = %d, want 1: a prompt waits and never compacts", got)
		}
	})
}

func TestAwaitCompaction_ACompactionThatNeverAnswersIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newCompactRig(90)
		r.block = make(chan struct{})
		r.reading(95)
		go r.ac.afterTurn(context.Background(), "c1", r.lastTurn(), false)
		synctest.Wait()
		start := time.Now()
		r.ac.awaitRunning(context.Background(), "c1")
		if waited := time.Since(start); waited != compactCallBudget {
			t.Errorf("the prompt waited %s, want the %s budget", waited, compactCallBudget)
		}
		close(r.block)
		synctest.Wait()
	})
}

func TestAwaitCompaction_NoCompactionRunningSendsAtOnce(t *testing.T) {
	r := newCompactRig(90)
	r.reading(99)
	r.ac.awaitRunning(t.Context(), "c1")
	if got := r.count(); got != 0 {
		t.Errorf("a prompt with nothing running made %d compact calls, want 0", got)
	}
}

// TestAutoCompact_TurnEndCallsCompact pins that a real closer reaches the trigger through afterTurnClose.
func TestAutoCompact_TurnEndCallsCompact(t *testing.T) {
	h, _, _ := newTestHub()
	r := newCompactRig(85)
	r.reading(88)
	h.coord.autoCompact = r.ac
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return r.count() == 1 })
}

// TestAutoCompact_AContextLimitCloseReachesTheTrigger pins that a turn the model refused for its size reaches the
// compactor as a context-limit close, whatever the stored reading.
func TestAutoCompact_AContextLimitCloseReachesTheTrigger(t *testing.T) {
	h, _, _ := newTestHub()
	r := newCompactRig(90)
	r.reading(50)
	h.coord.autoCompact = r.ac
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "too long", marotte.FailureKindContextLimit, 0)
	waitFor(t, func() bool { return r.count() == 1 })
}

// The wire half: a reading KAS reports during a live turn reaches the record as the queued compaction every page's
// holding area shows, and the turn's close runs it and takes it off.
func TestAutoCompact_AReadingFromTheWireQueuesOnTheRecord(t *testing.T) {
	h, cs, _ := newTestHub()
	r := newCompactRig(90)
	r.onHub(h)
	id, _ := h.stagePromptTurn(t, "c1")

	h.coord.ObserveContextUsage(t.Context(), "c1", 91)
	if c, _ := cs.Get(t.Context(), "c1"); c.CompactPending == nil || c.Header().CompactPending == nil {
		t.Fatal("a reading of 91 during the turn did not queue the compaction on the record and its header")
	}
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool {
		c, _ := cs.Get(t.Context(), "c1")
		return r.count() == 1 && c.CompactPending == nil
	})
}

// onHub points the rig's queue and turn reads at the hub's record and registry.
func (r *compactRig) onHub(h *Runtime) {
	r.ac.queue = recordCompactQueue{bc: h.coord}
	r.ac.turnOf = h.coord.turns.compactTurn
	h.coord.autoCompact = r.ac
}

// Through the real close: crossing the point queues one compaction on the record, and the turn's end runs it before
// the drain sends any queued prompt.
func TestAutoCompact_AQueuedCompactionRunsBeforeTheQueuedPrompts(t *testing.T) {
	h, cs, _ := newTestHub()
	r := newCompactRig(90)
	r.onHub(h)
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
	inner := r.ac.compact
	r.ac.compact = func(ctx context.Context, b command.CompactBridge) (bool, uint64, error) {
		record("compact")
		return inner(ctx, b)
	}
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) { record("drain") }
	id, _ := h.stagePromptTurn(t, "c1")

	h.coord.ObserveContextUsage(t.Context(), "c1", 91)
	h.coord.ObserveContextUsage(t.Context(), "c1", 94)
	if c, _ := cs.Get(t.Context(), "c1"); c.CompactPending == nil || r.count() != 0 {
		t.Fatalf("crossing 90 mid-turn: queued %v, calls %d; want queued and no call yet", c.CompactPending != nil, r.count())
	}
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return len(steps()) == 2 })
	if got := steps(); !slices.Equal(got, []string{"compact", "drain"}) {
		t.Errorf("the turn's end ran %v, want [compact drain]", got)
	}
}

// Through the real close: a cancel after the turn closed, while its close has not yet reached the turn end's
// compaction (here from the parked-model step just before it), stops that compaction.
func TestAutoCompact_ACancelWhileTheCloseRunsStopsItsTurnEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	r := newCompactRig(90)
	r.onHub(h)
	var once sync.Once
	cancelled := make(chan error, 1)
	h.coord.applyPendingModel = func(ctx context.Context, chatID marotte.ChatID) {
		once.Do(func() { cancelled <- r.ac.cancel(ctx, chatID) })
	}
	drained := make(chan struct{})
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) { close(drained) }
	id, _ := h.stagePromptTurn(t, "c1")

	r.reading(92)
	h.coord.ObserveContextUsage(t.Context(), "c1", 92)
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	<-drained
	if err := <-cancelled; err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if c, _ := cs.Get(t.Context(), "c1"); r.count() != 0 || c.CompactPending != nil {
		t.Errorf("a cancel before the turn end's compaction: calls %d, queued %v; want neither",
			r.count(), c.CompactPending != nil)
	}
}

// compactTurn names the open turn while it runs, then the finished one, and the next turn by its own id.
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

// The reader's compaction on an idle chat runs now, whatever the policy says.
func TestCompactRequest_AnIdleChatCompactsNow(t *testing.T) {
	r := newCompactRig(60)
	r.enabled = false
	r.reading(10)
	out, err := r.ac.request(t.Context(), "c1")
	if err != nil || out != command.CompactAccepted {
		t.Fatalf("request on an idle chat = (%v, %v), want accepted", out, err)
	}
	if got := r.count(); got != 1 {
		t.Errorf("compact calls = %d, want 1", got)
	}
}

func TestCompactRequest_WhileATurnRunsIsQueuedForItsEnd(t *testing.T) {
	r := newCompactRig(90)
	r.reading(10)
	r.turn(t)
	out, err := r.ac.request(t.Context(), "c1")
	if err != nil || out != command.CompactQueued {
		t.Fatalf("request during a turn = (%v, %v), want queued", out, err)
	}
	if again, _ := r.ac.request(t.Context(), "c1"); again != command.CompactQueued || r.count() != 0 {
		t.Fatalf("a second press = %v with %d calls, want queued and none", again, r.count())
	}
	r.closeTurn(t, false)
	if r.count() != 1 || r.isQueued() {
		t.Errorf("at the end: calls %d, queued %v; want 1 call and the row gone", r.count(), r.isQueued())
	}
}

// A request that meets a running compaction answers that compaction's outcome instead of a second call.
func TestCompactRequest_JoinsARunningCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newCompactRig(90)
		r.block = make(chan struct{})
		r.reading(95)
		go r.ac.afterTurn(context.Background(), "c1", r.lastTurn(), false)
		synctest.Wait()
		answered := make(chan command.CompactOutcome, 1)
		go func() {
			out, _ := r.ac.request(context.Background(), "c1")
			answered <- out
		}()
		synctest.Wait()
		close(r.block)
		if out := <-answered; out != command.CompactAccepted {
			t.Errorf("the joined request = %v, want accepted", out)
		}
		synctest.Wait()
		if got := r.count(); got != 1 {
			t.Errorf("compact calls = %d, want 1", got)
		}
	})
}

// A close that finds the queued compaction already running (the reader's own, started on an idle chat) waits for it
// and settles the request with it, instead of compacting twice.
func TestCompactRequest_ATurnEndJoinsTheRunningCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newCompactRig(90)
		r.block = make(chan struct{})
		go func() { _, _ = r.ac.request(context.Background(), "c1") }()
		synctest.Wait()
		r.pressUnderHold(t)
		closed := make(chan struct{})
		go func() {
			r.ac.afterTurn(context.Background(), "c1", r.lastTurn(), false)
			close(closed)
		}()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("the turn's end settled the queued compaction while its run was still going")
		default:
		}
		close(r.block)
		<-closed
		if got := r.count(); got != 1 || r.isQueued() {
			t.Errorf("calls %d, queued %v; want 1 call and the request settled", got, r.isQueued())
		}
	})
}

// A run refused because a turn opened between the idle check and the call is queued behind that turn.
func TestCompactRequest_RefusedUnderAnOpeningTurnIsQueued(t *testing.T) {
	r := newCompactRig(90)
	r.setRefuse(true)
	r.onCompact = func() { r.setLive(true) }
	out, err := r.ac.request(t.Context(), "c1")
	if err != nil || out != command.CompactQueued || !r.isQueued() {
		t.Errorf("request refused under an opening turn = (%v, %v), queued %v; want queued", out, err, r.isQueued())
	}
}

func TestCompactRequest_RefusedOnAnIdleChatIsRefused(t *testing.T) {
	r := newCompactRig(90)
	r.setRefuse(true)
	if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactRefused {
		t.Errorf("request KAS refused on an idle chat = (%v, %v), want refused", out, err)
	}
}

// KAS opens and closes its own turn during an idle press's call, a second press is queued behind that turn, and the
// call is then refused with no turn live: the second press's row is dropped and its pages are told.
func TestCompactRequest_APressQueuedDuringARefusedIdlePressIsReported(t *testing.T) {
	r := newCompactRig(90)
	r.setRefuse(true)
	var once sync.Once
	r.onCompact = func() {
		once.Do(func() {
			r.setLive(true)
			if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactQueued {
				t.Errorf("a press during KAS's turn = (%v, %v), want queued", out, err)
			}
			r.setLive(false)
		})
	}
	if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactRefused {
		t.Errorf("the idle press = (%v, %v), want refused", out, err)
	}
	if r.count() != 1 || r.isQueued() || !slices.Equal(r.failures, []string{queuedCompactFailed}) {
		t.Errorf("after the refusal: calls %d, queued %v, failures %v; want 1 call, the row gone, one failure",
			r.count(), r.isQueued(), r.failures)
	}
}

func TestCompactRequest_NoSessionCompactsNothing(t *testing.T) {
	r := newCompactRig(90)
	r.ac.bridgeOf = func(marotte.ChatID) *sharedBridge { return nil }
	if out, err := r.ac.request(t.Context(), "c1"); err != nil || out != command.CompactNoSession || r.count() != 0 {
		t.Errorf("request with no session = (%v, %v), %d calls; want no session, 0 calls", out, err, r.count())
	}
}

// The record half of the button: a request during a live turn is on the header, and a cancel takes it off.
func TestCompactRequest_TheRecordCarriesTheQueuedRequest(t *testing.T) {
	h, cs, _ := newTestHub()
	r := newCompactRig(90)
	r.onHub(h)
	h.stagePromptTurn(t, "c1")

	if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactQueued {
		t.Fatalf("RequestCompact during a turn = (%v, %v), want queued", out, err)
	}
	if c, _ := cs.Get(t.Context(), "c1"); c.CompactPending == nil {
		t.Fatal("the queued compaction is not on the chat record")
	}
	if err := h.CancelCompact(t.Context(), "c1"); err != nil {
		t.Fatalf("CancelCompact: %v", err)
	}
	if c, _ := cs.Get(t.Context(), "c1"); c.CompactPending != nil {
		t.Error("the cancelled compaction is still on the record")
	}
}

// A compaction waits for the frames before KAS's reply, the summary among them, so the next prompt opens after it.
func TestCompactRequest_WaitsForTheFramesBeforeTheReply(t *testing.T) {
	r := newCompactRig(90)
	r.ac.compact = func(context.Context, command.CompactBridge) (bool, uint64, error) { return true, 7, nil }
	var waited []uint64
	r.ac.folded = func(_ context.Context, _ marotte.ChatID, seq uint64) bool {
		waited = append(waited, seq)
		return true
	}
	if out, _ := r.ac.request(t.Context(), "c1"); out != command.CompactAccepted {
		t.Fatalf("request = %v, want accepted", out)
	}
	if !slices.Equal(waited, []uint64{7}) {
		t.Errorf("read loop waits = %v, want one at the reply's position 7", waited)
	}
}
