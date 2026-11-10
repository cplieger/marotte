package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
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

// handoffHub is a chat whose two steers were left unsent by a turn's end, so the run list resends them as the next
// turn. Its store parks that resend between its open and its retire; drained closes once the resend's launch ends.
func handoffHub(t *testing.T) (h *Runtime, cs *testChatStore, ps *pausingStore, drained <-chan struct{}) {
	t.Helper()
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	// Installed before the unsends, whose fact pass lists and launches the resend on its own goroutine.
	ps = newPausingStore(h.coord.chatStore)
	ps.onOpen = true
	h.coord.chatStore = ps
	done := make(chan struct{})
	launch := h.coord.launchEntry
	var once sync.Once
	h.coord.launchEntry = func(ctx context.Context, id marotte.ChatID, l *command.EntryLaunch) (string, error) {
		defer once.Do(func() { close(done) })
		return launch(ctx, id, l)
	}
	for _, k := range []string{"steer-a", "steer-b"} {
		h.steerQueue.RouteSteer("c1", k, k, nil, command.SteerHolder{Held: true, PromptClass: true})
		if _, refuse := h.steerQueue.RouteSteer("c1", k, k, nil, command.SteerHolder{}); refuse != command.SteerRefuseNoTurn {
			t.Fatalf("unsending %s = %q, want no_turn", k, refuse)
		}
	}
	return h, cs, ps, done
}

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

func fenceNow(h *Runtime, chatID marotte.ChatID) command.TurnFence {
	lc := h.coord.turns.testLifecycle(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return command.TurnFence{Epoch: lc.epoch, Seq: lc.nextSeq}
}

// shutdownWithin runs h's Shutdown under a budget of d.
func shutdownWithin(h *Runtime, d time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		_ = h.Shutdown(ctx)
	}()
	return done
}

// A shutdown between a resend's open and its retire leaves each steer exactly one destination, the resend,
// whether the shutdown's drain wait completes or expires. The resend's launch runs on inflight, so the shutdown
// reaches the steers only once its inflight wait gives up on the parked open (a one-second budget).
func TestShutdown_AResendOpenedBeforeTheTakeHasOneDestination(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "completed wait"
		if expired {
			name = "expired wait"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if expired {
					shortenSteerLockWait(t)
				}
				h, cs, ps, drained := handoffHub(t)
				<-ps.paused
				stopped := shutdownWithin(h, time.Second)
				if expired {
					waitFor(t, func() bool { return h.bus.steers.gone("c1") })
				} else {
					// Every other goroutine is durably blocked: the shutdown is parked on the steer lock.
					synctest.Wait()
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
		})
	}
}

// A tab close whose lock wait expires between a resend's open and its retire writes no drop note for the
// steers that resend already carries.
func TestBeginChatTeardown_AResendOpenedBeforeTheCloseKeepsItsSteers(t *testing.T) {
	shortenSteerLockWait(t)
	h, cs, ps, drained := handoffHub(t)
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

// pickHub is a hub whose chat c1 has a live bridge and a waiting user row, and whose launcher parks the first take
// between the list's pick of that row and its open.
func pickHub(t *testing.T) (*Runtime, *testChatStore, *pausingStore) {
	t.Helper()
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	h.coord.turns.testLifecycle("c1")
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "next"})
	ps := newPausingStore(h.coord.chatStore)
	launch := h.coord.launchEntry
	h.coord.launchEntry = func(ctx context.Context, chatID marotte.ChatID, l *command.EntryLaunch) (string, error) {
		ps.park()
		return launch(ctx, chatID, l)
	}
	return h, cs, ps
}

// drainPaused lets the list take the waiting row, parks its launch between pick and open, runs interrupt, releases
// the launch once interrupt re-fenced the chat, and waits for both.
func drainPaused(t *testing.T, h *Runtime, ps *pausingStore, interrupt func() <-chan struct{}) {
	t.Helper()
	before := epochOf(h, "c1")
	lc := h.coord.turns.testLifecycle("c1")
	lc.mu.Lock()
	lc.run.unpauseRows()
	lc.wakeLocked()
	lc.mu.Unlock()
	<-ps.paused
	done := interrupt()
	waitFor(t, func() bool { return epochOf(h, "c1") != before })
	close(ps.release)
	<-done
	waitFor(t, func() bool {
		lc.mu.Lock()
		defer lc.mu.Unlock()
		_, e := lc.run.find("m-q1")
		return lc.ended || e == nil || !e.live()
	})
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

// A shutdown landing between a take and its open starts no new turn, whether its wait for the steer lock completes
// or expires. The launch runs on inflight, so the shutdown re-fences only once its inflight wait gives up on the
// parked open (a one-second budget).
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
			drainPaused(t, h, ps, func() <-chan struct{} { return shutdownWithin(h, time.Second) })
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
