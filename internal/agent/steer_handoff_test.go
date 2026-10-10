package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// pausingStore parks the first armed call of one kind until release closes.
type pausingStore struct {
	bridgeChatRecords
	paused  chan struct{}
	release chan struct{}
	once    sync.Once
	onOpen  bool
	onPick  bool
}

func newPausingStore(inner bridgeChatRecords) *pausingStore {
	return &pausingStore{bridgeChatRecords: inner, paused: make(chan struct{}), release: make(chan struct{})}
}

func (s *pausingStore) park() {
	s.once.Do(func() {
		close(s.paused)
		<-s.release
	})
}

// WriteCounters pauses after an open's lifecycle section, the seam between a
// resend's open and its retire.
func (s *pausingStore) WriteCounters(ctx context.Context, chatID marotte.ChatID) error {
	err := s.bridgeChatRecords.WriteCounters(ctx, chatID)
	if s.onOpen {
		s.park()
	}
	return err
}

// Mutate pauses after the drain's pick of a queued row, before its open.
func (s *pausingStore) Mutate(ctx context.Context, id marotte.ChatID, fn func(c *marotte.Chat, exists bool) bool) (string, error) {
	v, err := s.bridgeChatRecords.Mutate(ctx, id, fn)
	if s.onPick {
		s.park()
	}
	return v, err
}

// handoffHub is a hub whose chat c1 has a live bridge and two unread user steers.
func handoffHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	for _, k := range []string{"steer-a", "steer-b"} {
		h.steerQueue.RouteSteer("c1", k, k, command.SteerHolder{Held: true, PromptClass: true})
		if _, refuse := h.steerQueue.RouteSteer("c1", k, k, command.SteerHolder{}); refuse != command.SteerRefuseNoTurn {
			t.Fatalf("unsending %s = %q, want no_turn", k, refuse)
		}
	}
	return h, cs, br
}

// steerEntries maps each steer entry's id to its payloads, in log order.
func steerEntries(t *testing.T, cs *testChatStore, chatID marotte.ChatID) map[string][]marotte.EntrySteer {
	t.Helper()
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	out := make(map[string][]marotte.EntrySteer)
	for _, e := range entries {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var s marotte.EntrySteer
		if err := json.Unmarshal(e.Payload, &s); err != nil {
			t.Fatalf("decode %s: %v", e.ID, err)
		}
		out[e.ID] = append(out[e.ID], s)
	}
	return out
}

func shortenSteerLockWait(t *testing.T) {
	t.Helper()
	orig := command.ResendBridgeWait
	command.ResendBridgeWait = 50 * time.Millisecond
	t.Cleanup(func() { command.ResendBridgeWait = orig })
}

func epochOf(h *Runtime, chatID marotte.ChatID) uint64 {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return 0
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.epoch
}

// fenceNow names the chat's newest turn, the fence a close of it would carry.
func fenceNow(h *Runtime, chatID marotte.ChatID) command.TurnFence {
	lc := h.coord.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return command.TurnFence{Epoch: lc.epoch, Seq: lc.nextSeq}
}

func shutdownAsync(t *testing.T, h *Runtime) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	}()
	return done
}

// A shutdown between a resend's open and its retire leaves each steer exactly one destination, the resend,
// whether the shutdown's drain wait completes or expires.
func TestShutdown_AResendOpenedBeforeTheTakeHasOneDestination(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "completed wait"
		if expired {
			name = "expired wait"
		}
		t.Run(name, func(t *testing.T) {
			if expired {
				shortenSteerLockWait(t)
			}
			h, cs, _ := handoffHub(t)
			ps := newPausingStore(h.coord.chatStore)
			ps.onOpen = true
			h.coord.chatStore = ps

			fence := fenceNow(h, "c1")
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				h.dispatcher.DrainAfterClose(context.Background(), "c1",
					command.CloseFacts{Outcome: marotte.TurnOutcomeCompleted, Fence: fence}, command.EndFacts{})
			}()
			<-ps.paused
			stopped := shutdownAsync(t, h)
			if expired {
				waitFor(t, func() bool { return h.bus.steers.gone("c1") })
			} else {
				awaitLockWaiters(t, h.steerQueue, 2)
			}
			close(ps.release)
			<-drained
			<-stopped

			if p := resendPrompt(t, cs); p == nil || !slices.Equal(p.Resends, []string{"steer-a", "steer-b"}) {
				t.Fatalf("resend prompt = %+v, want both steers named", p)
			}
			got := steerEntries(t, cs, "c1")
			for _, k := range []string{"steer-a", "steer-b"} {
				if e := got[k]; len(e) != 0 {
					t.Errorf("entries for %s = %+v, want none: the resend prompt carries them", k, e)
				}
			}
			if rows := queuedRows(t, cs, "c1"); len(rows) != 0 {
				t.Errorf("queue = %+v, want no Held row for steers a prompt already carried", rows)
			}
		})
	}
}

// A tab close whose lock wait expires between a resend's open and its retire writes no drop note for the
// steers that resend already carries.
func TestBeginChatTeardown_AResendOpenedBeforeTheCloseKeepsItsSteers(t *testing.T) {
	shortenSteerLockWait(t)
	h, cs, _ := handoffHub(t)
	ps := newPausingStore(h.coord.chatStore)
	ps.onOpen = true
	h.coord.chatStore = ps

	fence := fenceNow(h, "c1")
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		h.dispatcher.DrainAfterClose(context.Background(), "c1",
			command.CloseFacts{Outcome: marotte.TurnOutcomeCompleted, Fence: fence}, command.EndFacts{})
	}()
	<-ps.paused
	h.BeginChatTeardown("c1", true)
	close(ps.release)
	<-drained

	if p := resendPrompt(t, cs); p == nil || !slices.Equal(p.Resends, []string{"steer-a", "steer-b"}) {
		t.Fatalf("resend prompt = %+v, want both steers named", p)
	}
	got := steerEntries(t, cs, "c1")
	for _, k := range []string{"steer-a", "steer-b"} {
		if e := got[k]; len(e) != 0 {
			t.Errorf("entries for %s = %+v, want none: the resend prompt carries them", k, e)
		}
	}
}

// pickHub is a hub whose chat c1 has a live bridge, a queued user row, and a
// store that parks the drain between its pick of that row and its open.
func pickHub(t *testing.T) (*Runtime, *testChatStore, *pausingStore) {
	t.Helper()
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "next"})
	ps := newPausingStore(h.coord.chatStore)
	ps.onPick = true
	h.coord.chatStore = ps
	return h, cs, ps
}

// drainPaused parks the drain between pick and open, runs interrupt, releases the drain once interrupt
// re-fenced the chat, and waits for both.
func drainPaused(t *testing.T, h *Runtime, ps *pausingStore, interrupt func() <-chan struct{}) {
	t.Helper()
	fence := fenceNow(h, "c1")
	before := fence.Epoch
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		h.dispatcher.DrainAfterClose(context.Background(), "c1",
			command.CloseFacts{Outcome: marotte.TurnOutcomeCompleted, Fence: fence}, command.EndFacts{})
	}()
	<-ps.paused
	done := interrupt()
	waitFor(t, func() bool { return epochOf(h, "c1") != before })
	close(ps.release)
	<-drained
	<-done
}

func assertNotOpened(t *testing.T, cs *testChatStore) {
	t.Helper()
	if ids := promptIDs(t, cs, "c1"); slices.Contains(ids, "m-q1") {
		t.Errorf("turns opened = %v: the picked row opened after the chat was fenced", ids)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-q1" {
		t.Errorf("queue = %+v, want the picked row still queued", rows)
	}
}

// A shutdown landing between a drain's pick and its open starts no new turn, whether
// its wait for the drain completes or expires.
func TestShutdown_ARowPickedBeforeTheOpenNeverOpens(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "completed wait"
		if expired {
			name = "expired wait"
		}
		t.Run(name, func(t *testing.T) {
			if expired {
				shortenSteerLockWait(t)
			}
			h, cs, ps := pickHub(t)
			drainPaused(t, h, ps, func() <-chan struct{} { return shutdownAsync(t, h) })
			assertNotOpened(t, cs)
		})
	}
}

// A teardown landing between a drain's pick and its open starts no new turn, for a
// close (the record survives) and for a delete.
func TestTeardown_ARowPickedBeforeTheOpenNeverOpens(t *testing.T) {
	for _, keep := range []bool{true, false} {
		name := "close"
		if !keep {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			h, cs, ps := pickHub(t)
			t.Cleanup(func() { shutdownHub(t, h) })
			drainPaused(t, h, ps, func() <-chan struct{} {
				done := make(chan struct{})
				go func() {
					defer close(done)
					h.BeginChatTeardown("c1", keep)
				}()
				return done
			})
			assertNotOpened(t, cs)
		})
	}
}

// A turn opening between pick and open supersedes the drain: its fence no longer holds, so the open is refused and the row stays.
func TestDrain_ATurnOpeningBetweenThePickAndTheOpenSupersedesIt(t *testing.T) {
	h, cs, ps := pickHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	fence := fenceNow(h, "c1")
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		h.dispatcher.DrainAfterClose(context.Background(), "c1",
			command.CloseFacts{Outcome: marotte.TurnOutcomeCompleted, Fence: fence}, command.EndFacts{})
	}()
	<-ps.paused
	h.translateACPEvent("c1", newTurnStartMsg())
	close(ps.release)
	<-drained
	h.translateACPEvent("c1", newTurnEndMsg("end_turn"))

	assertNotOpened(t, cs)
}

// A close's fence refuses its drain's reservation once a newer turn has opened.
func TestTryReserveTurnFenced_ANewerTurnRefusesTheOldClose(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	fence := fenceNow(h, "c1")
	id, _ := h.stagePromptTurn(t, "c1")
	endTurn(t, h, "c1", id)
	h.coord.ReleaseTurn("c1", id)
	waitFor(t, func() bool { return !h.coord.turns.live("c1") })

	if h.coord.TryReserveTurnFenced("c1", marotte.TurnSourcePrompt, fence) {
		t.Error("an older close's drain reserved a chat a newer turn opened on")
	}
	if !h.coord.TryReserveTurnFenced("c1", marotte.TurnSourcePrompt, fenceNow(h, "c1")) {
		t.Error("the newest close's drain was refused an idle chat")
	}
}
