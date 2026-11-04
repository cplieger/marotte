package command

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/slogx/capture"
)

// loggingTabs logs each Close into the event log, and can make Close of named tabs answer nothing.
type loggingTabs struct {
	*tabs.Store
	log *holdLog
	// gone are tab ids whose Close answers nothing closed, as if another close removed them first.
	gone map[string]bool
	// failCloses counts the Closes that fail before one succeeds.
	failCloses int
	mu         sync.Mutex
}

func (l *loggingTabs) Close(ctx context.Context, id string) ([]marotte.TabSubject, uint64, error) {
	l.log.add("tabs.Close " + id)
	l.mu.Lock()
	fail, gone := l.failCloses > 0, l.gone[id]
	if fail {
		l.failCloses--
	}
	l.mu.Unlock()
	if fail {
		return nil, 0, errCloseFailed
	}
	if gone {
		return nil, 0, nil
	}
	return l.Store.Close(ctx, id)
}

// logTabs wraps the store in a loggingTabs on the host's log, storing it in *out when out is non-nil.
func logTabs(out **loggingTabs) func(*tabs.Store, *holdLog) TabSet {
	return func(st *tabs.Store, log *holdLog) TabSet {
		lt := &loggingTabs{Store: st, log: log, gone: map[string]bool{}}
		if out != nil {
			*out = lt
		}
		return lt
	}
}

func TestCloseTab_AnswersBeforeItsTeardownRuns(t *testing.T) {
	h := newRunnerHost(t, runnerOpts{})
	chatID, tab := h.chatUnder(t, "op-a", "")
	h.gates.park(chatID)

	closed, _, err := h.mem.closeTab(t.Context(), tab, "op-close", nil)

	if err != nil || !slices.Equal(subjectIDs(closed), []string{tab}) {
		t.Fatalf("closeTab = %v, %v; want the tab closed", subjectIDs(closed), err)
	}
	if frames := h.bus.frames(t); len(frames) == 0 || !slices.Contains(frames[len(frames)-1].RemovedIDs, tab) {
		t.Error("closeTab answered before its tabs_changed frame")
	}
	if n := h.log.count("end close " + string(chatID)); n != 0 {
		t.Error("the teardown finished before closeTab answered")
	}
	h.gates.open(chatID)
	h.log.awaitEvent(t, "end close "+string(chatID))
}

func TestDeleteChatAndCloseTabs_AnswersBeforeItsTeardown(t *testing.T) {
	h := newRunnerHost(t, runnerOpts{})
	chatID, _ := h.chatUnder(t, "op-a", "")
	h.gates.park(chatID)

	if err := h.mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del", nil); err != nil {
		t.Fatal(err)
	}

	if n := h.log.count("end delete " + string(chatID)); n != 0 {
		t.Error("the delete's teardown finished before it answered")
	}
	h.gates.open(chatID)
	h.log.awaitEvent(t, "end delete "+string(chatID))
}

func TestCloseTab_HoldsEveryClosedChatFromTheCheckUntilItsJobReturns(t *testing.T) {
	impact := &fakeImpact{work: map[marotte.ChatID]CloseWork{}}
	h := newRunnerHost(t, runnerOpts{wrapTabs: logTabs(nil), retention: retentionOff, impact: impact})
	log := h.log
	impact.onRead = func() { log.add("read") }
	parent, parentTab := h.chatUnder(t, "op-p", "")
	doomedChild, _ := h.chatUnder(t, "op-c1", parentTab)
	keptChild, _ := h.chatUnder(t, "op-c2", parentTab)
	if _, err := h.store.Delete(t.Context(), keptChild); err != nil {
		t.Fatal(err) // a chat with no record is not doomed: it gets the close grade
	}
	var underLock []string
	log.onEvent = func(e string) {
		if rest, ok := strings.CutPrefix(e, "commit "); ok {
			if h.mem.mu.TryLock() {
				h.mem.mu.Unlock()
			} else {
				underLock = append(underLock, rest)
			}
		}
	}
	page := &marotte.CloseImpact{Chats: []marotte.ChatWork{{Ref: string(parent)}, {Ref: string(doomedChild)}, {Ref: string(keptChild)}}}

	closed, _, err := h.mem.closeTab(t.Context(), parentTab, "op-close", page)
	if err != nil {
		t.Fatal(err)
	}
	grades := map[marotte.ChatID]string{parent: "close-delete", doomedChild: "close-delete", keptChild: "close"}
	for id, grade := range grades {
		log.awaitEvent(t, "release "+string(id))
		c := string(id)
		if !log.before("hold "+c, "read") || !log.before("read", "tabs.Close "+parentTab) {
			t.Errorf("%s: the hold and the impact read must precede tabs.Close; events %v", c, log.snapshot())
		}
		if !log.before("tabs.Close "+parentTab, "commit "+c) {
			t.Errorf("%s: committed before tabs.Close; events %v", c, log.snapshot())
		}
		if !log.before("end "+grade+" "+c, "release "+c) {
			t.Errorf("%s: released before its %s grade returned; events %v", c, grade, log.snapshot())
		}
	}
	slices.Sort(underLock)
	held := log.chatsWith("hold")
	slices.Sort(held)
	if !slices.Equal(underLock, held) {
		t.Errorf("commits under the commit lock = %v, want every held chat %v", underLock, held)
	}
	var closedChats []string
	for _, s := range closed {
		if s.Kind == marotte.TabKindChat {
			closedChats = append(closedChats, s.Ref)
		}
	}
	slices.Sort(closedChats)
	if !slices.Equal(held, closedChats) {
		t.Errorf("held chats = %v, want the chat subjects tabs.Close returned %v", held, closedChats)
	}
}

func TestDeleteChatAndCloseTabs_TearsDownADescendantChat(t *testing.T) {
	h := newRunnerHost(t, runnerOpts{})
	c, cTab := h.chatUnder(t, "op-c", "")
	d, _ := h.chatUnder(t, "op-d", cTab)
	var frameAfterCommit bool
	h.bus.onBroadcast = func() { frameAfterCommit = h.log.count("commit "+string(d)) == 1 }

	if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", nil); err != nil {
		t.Fatal(err)
	}

	h.log.awaitEvent(t, "end close "+string(d))
	if !frameAfterCommit {
		t.Errorf("the tabs_changed frame went out before D's hold committed; events %v", h.log.snapshot())
	}
	if !h.log.before("hold "+string(d), "commit "+string(d)) || !h.log.before("end close "+string(d), "release "+string(d)) {
		t.Errorf("D's hold, commit, grade and release are out of order: %v", h.log.snapshot())
	}
}

func TestRetentionClose_TearsDownADescendantChat(t *testing.T) {
	h := newRunnerHost(t, runnerOpts{})
	c, cTab := h.chatUnder(t, "op-c", "")
	d, _ := h.chatUnder(t, "op-d", cTab)
	chain, err := h.store.Delete(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	h.gates.park(d)

	h.mem.RetentionClose(t.Context(), c, chain)

	if !h.log.before("end retention "+string(c), "hold "+string(d)) {
		t.Errorf("D was held before C's retention job finished, want it in the second section: %v", h.log.snapshot())
	}
	if h.log.count("commit "+string(d)) != 1 {
		t.Errorf("D's hold was not committed: %v", h.log.snapshot())
	}
	if h.log.count("end close "+string(d)) != 0 {
		t.Error("RetentionClose waited for D's job")
	}
	h.gates.open(d)
	h.log.awaitEvent(t, "end close "+string(d))
}

func TestCloseTabsFor_AFailedCloseReleasesTheDescendantHoldsUncommitted(t *testing.T) {
	var lt *loggingTabs
	h := newRunnerHost(t, runnerOpts{wrapTabs: logTabs(&lt)})
	log := h.log
	c, cTab := h.chatUnder(t, "op-c", "")
	d, _ := h.chatUnder(t, "op-d", cTab)
	lt.failCloses = 2

	if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", nil); err != nil {
		t.Fatal(err)
	}

	log.awaitEvent(t, "end delete "+string(c))
	if log.count("commit "+string(d)) != 0 || log.count("release "+string(d)) != 1 {
		t.Errorf("D's hold after a failed tab close: %v, want released once and never committed", log.snapshot())
	}
	if log.count("start close "+string(d)) != 0 {
		t.Error("a descendant whose tab did not close got a close grade")
	}
}

func TestCloseTabsFor_AHoldWithNoClosedTabIsReleased(t *testing.T) {
	var lt *loggingTabs
	h := newRunnerHost(t, runnerOpts{wrapTabs: logTabs(&lt)})
	log := h.log
	c, cTab := h.chatUnder(t, "op-c", "")
	d, _ := h.chatUnder(t, "op-d", cTab)
	lt.gone[cTab] = true

	if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", nil); err != nil {
		t.Fatal(err)
	}

	if log.count("commit "+string(d)) != 0 || log.count("release "+string(d)) != 1 {
		t.Errorf("D's hold: %v, want released uncommitted", log.snapshot())
	}
}

// Each commit path enqueues its jobs inside its critical section.
func TestCloseTab_EnqueuesBeforeItUnlocks(t *testing.T) {
	for _, path := range []string{"closeTab", "DeleteChatAndCloseTabs", "RetentionClose"} {
		t.Run(path, func(t *testing.T) {
			h := newCloseHost(t)
			c, cTab := h.chatUnder(t, "op-c", "")
			h.chatUnder(t, "op-d", cTab)
			var unlocked []string
			h.queue.onEnqueue = func(job teardownJob) {
				if h.mem.mu.TryLock() {
					h.mem.mu.Unlock()
					unlocked = append(unlocked, string(job.chatID))
				}
			}

			switch path {
			case "closeTab":
				_, _, err := h.mem.closeTab(t.Context(), cTab, "op-close", nil)
				if err != nil {
					t.Fatal(err)
				}
			case "DeleteChatAndCloseTabs":
				if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", nil); err != nil {
					t.Fatal(err)
				}
			case "RetentionClose":
				chain, err := h.store.Delete(t.Context(), c)
				if err != nil {
					t.Fatal(err)
				}
				h.mem.RetentionClose(t.Context(), c, chain)
			}

			if n := len(h.queue.snapshot()); n != 2 {
				t.Errorf("enqueued %d jobs, want 2 (the chat and its descendant)", n)
			}
			if len(unlocked) != 0 {
				t.Errorf("jobs for %v were enqueued outside the commit lock", unlocked)
			}
		})
	}
}

func TestRetentionClose_WaitsForABackgroundCloseOfTheSameChat(t *testing.T) {
	h := newRunnerHost(t, runnerOpts{})
	c, cTab := h.chatUnder(t, "op-c", "")
	h.gates.park(c)
	if _, _, err := h.mem.closeTab(t.Context(), cTab, "op-close", nil); err != nil {
		t.Fatal(err)
	}
	h.log.awaitEvent(t, "start close "+string(c))
	chain, err := h.store.Delete(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() { h.mem.RetentionClose(context.Background(), c, chain); close(returned) }()
	select {
	case <-returned:
		t.Fatal("RetentionClose returned while the close it queued behind still ran")
	case <-time.After(50 * time.Millisecond):
	}

	h.gates.open(c)
	awaitDone(t, returned, "RetentionClose")
	if !h.log.before("end close "+string(c), "start retention "+string(c)) || h.log.count("end retention "+string(c)) != 1 {
		t.Errorf("events = %v, want the close, then the retention job, before RetentionClose returned", h.log.snapshot())
	}
}

func TestRetentionClose_ACloseCommittedAfterItRunsAfterIt(t *testing.T) {
	h := newRunnerHost(t, runnerOpts{})
	c, _ := h.chatUnder(t, "op-c", "")
	chain, err := h.store.Delete(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	h.gates.park(c)
	returned := make(chan struct{})
	go func() { h.mem.RetentionClose(context.Background(), c, chain); close(returned) }()
	h.log.awaitEvent(t, "start retention "+string(c))
	// A tab for the chat opened after the purge read HasOpenTab: its close commits while the retention job runs.
	reopened, _, _, err := h.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(c)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.mem.closeTab(t.Context(), reopened.ID, "op-close", nil); err != nil {
		t.Fatal(err)
	}

	h.gates.open(c)
	h.log.awaitEvent(t, "end close "+string(c))
	awaitDone(t, returned, "RetentionClose")
	if !h.log.before("end retention "+string(c), "start close "+string(c)) {
		t.Errorf("events = %v, want the later close after the retention job", h.log.snapshot())
	}
}

func TestRetentionClose_AStuckTeardownCostsThePassOneBound(t *testing.T) {
	logs := capture.Default(t)
	old := retentionTeardownWait
	retentionTeardownWait = 50 * time.Millisecond
	t.Cleanup(func() { retentionTeardownWait = old })
	h := newRunnerHost(t, runnerOpts{})
	c, cTab := h.chatUnder(t, "op-c", "")
	d, _ := h.chatUnder(t, "op-d", cTab)
	chain, err := h.store.Delete(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	h.gates.park(c)

	start := time.Now()
	h.mem.RetentionClose(t.Context(), c, chain)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("RetentionClose took %v with its job stuck, want about the bound", elapsed)
	}
	if n := logs.CountLevel(slog.LevelDebug, "a purged chat's teardown is still running; the purge goes on"); n != 1 {
		t.Errorf("logged the stuck purge %d times, want once", n)
	}
	h.log.awaitEvent(t, "end close "+string(d))
	reopened, _, _, err := h.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(c)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.mem.closeTab(t.Context(), reopened.ID, "op-close", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // a close that did not queue behind the stuck job would start here
	if h.log.count("start close "+string(c)) != 0 {
		t.Error("a close committed after the stuck retention job started before it")
	}
	h.gates.open(c)
	h.log.awaitEvent(t, "end close "+string(c))
}

func TestTeardownJobs_CarryTheirStopCause(t *testing.T) {
	causeOf := func(h *runnerHost, id marotte.ChatID) RunStopCause {
		v, ok := h.teardown.causes.Load(id)
		if !ok {
			t.Fatalf("no by-chain grade ran for %s; events %v", id, h.log.snapshot())
		}
		return v.(RunStopCause)
	}
	t.Run("a History delete", func(t *testing.T) {
		h := newRunnerHost(t, runnerOpts{})
		c, _ := h.chatUnder(t, "op-c", "")
		if err := h.mem.DeleteChatAndCloseTabs(t.Context(), c, "op-del", nil); err != nil {
			t.Fatal(err)
		}
		h.log.awaitEvent(t, "end delete "+string(c))
		if got := causeOf(h, c); got != RunStopChatDeleted {
			t.Errorf("cause = %v, want RunStopChatDeleted", got)
		}
	})
	t.Run("a retention-off close", func(t *testing.T) {
		h := newRunnerHost(t, runnerOpts{retention: retentionOff})
		c, tab := h.chatUnder(t, "op-c", "")
		if _, _, err := h.mem.closeTab(t.Context(), tab, "op-close", nil); err != nil {
			t.Fatal(err)
		}
		h.log.awaitEvent(t, "end close-delete "+string(c))
		if got := causeOf(h, c); got != RunStopTabClosed {
			t.Errorf("cause = %v, want RunStopTabClosed", got)
		}
	})
	t.Run("the retention purge", func(t *testing.T) {
		h := newRunnerHost(t, runnerOpts{})
		c, _ := h.chatUnder(t, "op-c", "")
		chain, err := h.store.Delete(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		h.mem.RetentionClose(t.Context(), c, chain)
		if got := causeOf(h, c); got != RunStopRetention {
			t.Errorf("cause = %v, want RunStopRetention", got)
		}
	})
}
