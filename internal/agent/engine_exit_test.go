package agent

import (
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// An engine end reaches every session it hosted: each chat's open turn closes, its pending ask is retired and the
// terminals it created are released, each through that session's own forward tail.
func TestEngineExit_ClosesEveryHostedSessionsTurnAndAsks(t *testing.T) {
	h, _ := reopenFixture(t)
	settleBoot(t, h)
	if _, err := h.chatStore.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true }); err != nil {
		t.Fatalf("Setup: seed c2: %v", err)
	}
	chats := []marotte.ChatID{"c1", "c2"}
	for i, chatID := range chats {
		sb, err := h.coord.openBridge(t.Context(), chatID, "")
		if err != nil {
			t.Fatalf("Setup: OpenBridge(%s): %v", chatID, err)
		}
		session := sb.current()
		h.stagePromptTurn(t, chatID)
		h.bus.PendingPermsAdd(int64(10+i), marotte.NewEvent(marotte.EventPermissionNeeded, chatID,
			marotte.PermissionNeededPayload{}), session)
		term := newAgentTerminal(&exec.Cmd{}, chatID, 64)
		term.origin = session
		h.agentTerms.mu.Lock()
		h.agentTerms.terms["term-"+string(chatID)] = term
		h.agentTerms.mu.Unlock()
	}

	fakeSourceOf(t, h).current().crash()

	for _, chatID := range chats {
		deadline := time.Now().Add(5 * time.Second)
		for !exitRetired(h, chatID) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := h.coord.turns.openTurnIDs(chatID); len(got) != 0 {
			t.Errorf("%s: %d turns still open after the engine ended", chatID, len(got))
		}
		if got := h.bus.pendingPerms.list(chatID); len(got) != 0 {
			t.Errorf("%s: %d asks still pending after the engine ended", chatID, len(got))
		}
		h.agentTerms.mu.Lock()
		_, live := h.agentTerms.terms["term-"+string(chatID)]
		h.agentTerms.mu.Unlock()
		if live {
			t.Errorf("%s: its session's terminal is still registered after the engine ended", chatID)
		}
	}
}

func exitRetired(h *Runtime, chatID marotte.ChatID) bool {
	h.agentTerms.mu.Lock()
	_, live := h.agentTerms.terms["term-"+string(chatID)]
	h.agentTerms.mu.Unlock()
	return !live && len(h.coord.turns.openTurnIDs(chatID)) == 0 && len(h.bus.pendingPerms.list(chatID)) == 0
}

// loadEndingFixture is c1 holding session sess-old on a runtime whose first n chat session doors end the engine under
// the load, as an engine exit during session/load does.
func loadEndingFixture(t *testing.T, n int32) (*Runtime, func() []*marotte.SessionOpts) {
	t.Helper()
	var h *Runtime
	var ended atomic.Int32
	var mu sync.Mutex
	var doors []*fakeBridge
	// The owner keeps the ended engine ready for a while after its end, so a retry that does not wait for the next
	// generation reaches the ended one.
	retired := make(chan struct{})
	var retireOnce sync.Once
	h = reopenFixtureIn(t, t.TempDir(), func(br *fakeBridge) {
		mu.Lock()
		doors = append(doors, br)
		mu.Unlock()
		if ended.Add(1) <= n {
			br.loadErr = &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true}
			fakeSourceOf(t, h).current().crash()
			retireOnce.Do(func() { time.AfterFunc(200*time.Millisecond, func() { close(retired) }) })
		}
	})
	settleBoot(t, h)
	src := fakeSourceOf(t, h)
	src.mu.Lock()
	src.exitGate = retired
	src.mu.Unlock()
	if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-old")
		return true
	}); err != nil {
		t.Fatalf("Setup: record c1's session: %v", err)
	}
	return h, func() []*marotte.SessionOpts {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*marotte.SessionOpts, 0, len(doors))
		for _, br := range doors {
			if o := br.lastSessionOpts(); o != nil {
				out = append(out, o)
			}
		}
		return out
	}
}

// An engine that ends under a chat's session/load leaves the session on disk: the load runs again on the next
// engine, and the chat keeps its conversation instead of falling back to a new session.
func TestEngineExit_ALoadItEndsLoadsAgainOnTheNextEngine(t *testing.T) {
	h, doorOpts := loadEndingFixture(t, 1)

	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge(c1) after the engine ended under its load: %v", err)
	}

	if got := fakeOf(sb).lastSessionOpts(); got == nil || got.SessionID != "sess-old" {
		t.Errorf("the chat's session door = %+v, want a load of sess-old", got)
	}
	if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.ACPSessionID != "sess-old" {
		t.Errorf("the record's session = %q, want sess-old kept", rec.ACPSessionID)
	}
	for _, o := range doorOpts() {
		if o.SessionID == "" {
			t.Errorf("a session/new was opened for the chat: %+v", o)
		}
	}
	if got := fakeSourceOf(t, h).generations(); got < 2 {
		t.Errorf("engine generations = %d, want the load on a second engine", got)
	}
}

// An engine that ends under the load and under its retry fails the open and keeps the chat's session, so the next
// open loads it once an engine stays up.
func TestEngineExit_ALoadEndedTwiceKeepsTheChatsSession(t *testing.T) {
	h, doorOpts := loadEndingFixture(t, 2)

	_, err := h.coord.openBridge(t.Context(), "c1", "")

	if !errors.Is(err, marotte.ErrBridgeExited) {
		t.Fatalf("OpenBridge(c1) = %v, want the engine's end", err)
	}
	if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.ACPSessionID != "sess-old" {
		t.Errorf("the record's session = %q, want sess-old kept", rec.ACPSessionID)
	}
	for _, o := range doorOpts() {
		if o.SessionID == "" {
			t.Errorf("a session/new was opened for the chat: %+v", o)
		}
	}
	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("the next OpenBridge(c1): %v", err)
	}
	if got := fakeOf(sb).lastSessionOpts(); got == nil || got.SessionID != "sess-old" {
		t.Errorf("the next session door = %+v, want a load of sess-old", got)
	}
}
