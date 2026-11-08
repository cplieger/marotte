package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// refreshHub is closeTabHub with c1's session open, answering its entry and the record's session id.
func refreshHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge, marotte.TabSubject, *sharedBridge, string) {
	t.Helper()
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	return h, cs, br, tab, sb, recordSessionID(t, cs)
}

// An empty turn's refresh that runs after the chat's close committed touches nothing: neither the reopened chat's
// session nor the closing one, and the record keeps its session id.
func TestEmptyRetry_ARefreshAfterAReopenTouchesNothing(t *testing.T) {
	t.Run("after a reopen", func(t *testing.T) {
		h, cs, _, tab, old, sessionID := refreshHub(t)
		if code := postCloseTab(t, h, tab); code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		awaitTeardown(t, h, "c1")
		next := newFakeBridge()
		fakeSourceOf(t, h).setFactory(func() engineSession { return next })
		reopenTab(t, h, "op-reopen")
		reopened, err := h.coord.openReleased(t.Context(), "c1", "")
		if err != nil {
			t.Fatalf("open after the reopen: %v", err)
		}

		err = h.coord.refreshForRetry(t.Context(), "c1", old)

		if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
			t.Errorf("refresh = %v, want the close's refusal", err)
		}
		if h.bridge.mgr.get("c1") != reopened || next.Released() || !next.Open() {
			t.Error("the old life's refresh reached the reopened chat's session")
		}
		if got := recordSessionID(t, cs); got != sessionID {
			t.Errorf("record session = %q, want %q kept", got, sessionID)
		}
	})
	t.Run("after the commit, before the teardown lets the session go", func(t *testing.T) {
		h, cs, br, tab, old, sessionID := refreshHub(t)
		reached, release := parkTeardownAt(t, h, "begun")
		t.Cleanup(release)
		closed := make(chan int, 1)
		go func() { closed <- postCloseTab(t, h, tab) }()
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			t.Fatal("the teardown never began")
		}

		err := h.coord.refreshForRetry(t.Context(), "c1", old)

		if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
			t.Errorf("refresh = %v, want the close's refusal", err)
		}
		if br.Released() {
			t.Error("the refresh released the session the teardown owns")
		}
		if got := recordSessionID(t, cs); got != sessionID {
			t.Errorf("record session = %q, want %q kept", got, sessionID)
		}
		release()
		if code := <-closed; code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		awaitTeardown(t, h, "c1")
		if !br.Released() {
			t.Error("the teardown never released the session")
		}
	})
}

// A close that commits while the refresh waits for its forward ends the refresh there: it closes no turn
// interrupted, logs no drain failure and keeps the record's session id, and the teardown closes the chat's open
// turn cancelled.
func TestEmptyRetry_ACutDuringTheForwardWaitClosesNoTurn(t *testing.T) {
	logs := captureLogs(t)
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	const marker = "the frame the forward is held on"
	held, unhold := make(chan struct{}), make(chan struct{})
	var unholdOnce sync.Once
	release := func() { unholdOnce.Do(func() { close(unhold) }) }
	t.Cleanup(release)
	observe := h.coord.observeLaneFrame
	h.coord.observeLaneFrame = func(origin acpResponder, seq uint64, msg *marotte.RPCResponse) {
		if strings.Contains(string(msg.Params), marker) {
			close(held)
			<-unhold
		}
		if observe != nil {
			observe(origin, seq, msg)
		}
	}
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	sessionID := recordSessionID(t, cs)
	br.deliver(newTurnStartMsg())
	awaitOpenTurns(t, h, 1)
	turn := h.coord.turns.openTurnIDs("c1")[0].ID
	br.mu.Lock()
	br.beforeRelease = func() { br.deliver(newChunkMsg(marker)) }
	br.mu.Unlock()

	refreshed := make(chan error, 1)
	go func() { refreshed <- h.coord.refreshForRetry(context.Background(), "c1", sb) }()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the forward never reached the held frame")
	}
	closed := make(chan int, 1)
	go func() { closed <- postCloseTab(t, h, tab) }()
	var refreshErr error
	select {
	case refreshErr = <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("the close's commit never cut the refresh's forward wait")
	}
	release()
	if code := <-closed; code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	joinInflight(t, h)

	if !errors.Is(refreshErr, marotte.ErrChatClosedDuringCall) {
		t.Errorf("refresh = %v, want the close's cut", refreshErr)
	}
	if got := recordSessionID(t, cs); got != sessionID {
		t.Errorf("record session = %q, want %q kept", got, sessionID)
	}
	if strings.Contains(logs.String(), "did not drain in time") {
		t.Error("the cut refresh logged a drain failure")
	}
	if c, ok := closeOfTurn(t, logOf(t, cs, "c1"), turn); !ok || c.Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close = %+v (closed %v), want the teardown's cancelled close", c, ok)
	}
}

// signalMutate is a chat store whose Mutate reports its entry before it waits for the chat's lock.
type signalMutate struct {
	bridgeChatRecords
	entered chan struct{}
}

func (s *signalMutate) Mutate(ctx context.Context, id marotte.ChatID, fn func(c *marotte.Chat, exists bool) bool) (string, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	return s.bridgeChatRecords.Mutate(ctx, id, fn)
}

// A close that commits while the refresh's detach waits for the chat's store lock keeps the record's session id.
func TestEmptyRetry_ACutDuringTheDetachsLockWaitKeepsTheSessionID(t *testing.T) {
	h, cs, _, _, old, sessionID := refreshHub(t)
	locked, unblock := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = cs.Mutate(context.Background(), "c1", func(*marotte.Chat, bool) bool {
			close(locked)
			<-unblock
			return false
		})
	}()
	<-locked
	sig := &signalMutate{bridgeChatRecords: h.coord.chatStore, entered: make(chan struct{}, 1)}
	h.coord.chatStore = sig
	t.Cleanup(func() { h.coord.chatStore = sig.bridgeChatRecords })
	refreshed := make(chan error, 1)
	go func() { refreshed <- h.coord.refreshForRetry(context.Background(), "c1", old) }()
	select {
	case <-sig.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh never reached its detach")
	}
	hold := h.HoldChat("c1")
	hold.Commit()
	close(unblock)
	err := <-refreshed
	hold.Release()

	if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
		t.Errorf("refresh = %v, want the close's cut", err)
	}
	if got := recordSessionID(t, cs); got != sessionID {
		t.Errorf("record session = %q, want %q kept", got, sessionID)
	}
}

// The refresh lets go of its session through its own lease: a ready entry's session is released once and the entry
// is gone; an entry the release check claimed is left to it, with no turn closed and the detach still comparing.
func TestEmptyRetry_TheRefreshReleasesThroughItsLease(t *testing.T) {
	t.Run("a ready entry", func(t *testing.T) {
		h, cs, br, _, sb, _ := refreshHub(t)
		releases := 0
		br.mu.Lock()
		br.beforeRelease = func() { releases++ }
		br.mu.Unlock()

		if err := h.coord.refreshForRetry(t.Context(), "c1", sb); err != nil {
			t.Fatalf("refresh = %v, want nil", err)
		}

		if releases != 1 || !br.Released() {
			t.Errorf("releases = %d (released %v), want one", releases, br.Released())
		}
		if h.bridge.mgr.get("c1") != nil {
			t.Error("the refreshed entry is still registered")
		}
		if got := recordSessionID(t, cs); got != "" {
			t.Errorf("record session = %q, want it detached", got)
		}
	})
	t.Run("an entry a granted close owns", func(t *testing.T) {
		h, cs, br, _, sb, sessionID := refreshHub(t)
		br.deliver(newTurnStartMsg())
		awaitOpenTurns(t, h, 1)
		lc := lifecycleOf(t, h, "c1")
		a := lc.askClose(sb, closeDead, 0, true)
		if a.verdict != askGranted {
			t.Fatalf("Setup: the door's close of the entry = %+v, want it granted", a)
		}
		t.Cleanup(func() { lc.endClose(a.grant, false) })
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.RecordSession("sess-other")
			return true
		}); err != nil {
			t.Fatalf("Setup: record another session: %v", err)
		}

		if err := h.coord.refreshForRetry(t.Context(), "c1", sb); !errors.Is(err, marotte.ErrSessionClosing) {
			t.Fatalf("refresh = %v, want %v: the close owns the session", err, marotte.ErrSessionClosing)
		}

		if br.Released() {
			t.Error("the refresh released a session a granted close owns")
		}
		if open := h.coord.turns.openTurnIDs("c1"); len(open) != 1 {
			t.Errorf("open turns = %d, want the turn left open", len(open))
		}
		if got := recordSessionID(t, cs); got != "sess-other" {
			t.Errorf("record session = %q, want the other session kept (the detach compares with %q)", got, sessionID)
		}
	})
}
