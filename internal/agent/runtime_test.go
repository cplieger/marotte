package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
)

func TestShutdownCompletesWithoutHanging(t *testing.T) {
	h, _, _ := newTestHub()
	done := make(chan struct{})
	go func() {
		// Unbounded: Shutdown must return on its own.
		_ = h.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Shutdown hung")
	}
}

// hangingBridge's Call blocks until Stop, so Shutdown must stop bridges before inflight.Wait.
type hangingBridge struct {
	*fakeBridge

	released   chan struct{}
	once       sync.Once
	stopCalled atomic.Bool
}

func newHangingBridge() *hangingBridge {
	return &hangingBridge{
		fakeBridge: newFakeBridge(),
		released:   make(chan struct{}),
	}
}

func (b *hangingBridge) Call(_ context.Context, _ string, _ any) (*marotte.RPCResponse, error) {
	<-b.released
	return &marotte.RPCResponse{}, nil
}

func (b *hangingBridge) Stop() {
	b.stopCalled.Store(true)
	b.once.Do(func() { close(b.released) })
	b.fakeBridge.Stop()
}

func TestShutdown_StopsBridgesBeforeWaitingOnInflight(t *testing.T) {
	// A Call that returns only when its bridge stops.
	cs := newTestChatStore()
	hb := newHangingBridge()
	factory := func() ACPBridge { return hb }
	h := New(t.Context(), "/tmp/work", factory, cs)
	cs.wire(h)

	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	// Registered directly: this tests Shutdown ordering.
	sb := &sharedBridge{bridge: hb}
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = sb
	h.bridge.mgr.mu.Unlock()

	// An in-flight prompt awaiting the Call.
	h.lifecycle.inflight.Add(1)
	callDone := make(chan struct{})
	go func() {
		defer h.lifecycle.inflight.Done()
		_, _ = hb.Call(t.Context(), "session/prompt", nil)
		close(callDone)
	}()

	done := make(chan struct{})
	go func() {
		// Unbounded: Shutdown must return on its own.
		_ = h.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown deadlocked waiting for in-flight Call")
	}
	select {
	case <-callDone:
	case <-time.After(1 * time.Second):
		t.Error("Call never returned despite Stop")
	}
	if !hb.stopCalled.Load() {
		t.Error("Stop was not called on the bridge")
	}
}

// barrierBridge's Stop returns once every bridge in its group entered Stop, so a serial shutdown never fills it.
type barrierBridge struct {
	*fakeBridge

	entered *sync.WaitGroup
	all     chan struct{}
	timeout chan struct{}
}

func (b *barrierBridge) Stop() {
	b.entered.Done()
	select {
	case <-b.all:
	case <-time.After(5 * time.Second):
		close(b.timeout)
	}
	b.fakeBridge.Stop()
}

// Stops must run side by side, or N bridges cost N graces.
func TestShutdown_StopsBridgesConcurrently(t *testing.T) {
	h, _, _ := newTestHub()
	var entered sync.WaitGroup
	all := make(chan struct{})
	ids := []marotte.ChatID{"c1", "c2", "c3"}
	entered.Add(len(ids))
	timeouts := make([]chan struct{}, 0, len(ids))
	h.bridge.mgr.mu.Lock()
	for _, id := range ids {
		tc := make(chan struct{})
		timeouts = append(timeouts, tc)
		h.bridge.mgr.bridges[id] = &sharedBridge{bridge: &barrierBridge{fakeBridge: newFakeBridge(), entered: &entered, all: all, timeout: tc}}
	}
	h.bridge.mgr.mu.Unlock()
	go func() { entered.Wait(); close(all) }()

	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for i, tc := range timeouts {
		select {
		case <-tc:
			t.Errorf("bridge %s waited 5s in Stop for its siblings; Shutdown stopped the bridges one at a time", ids[i])
		default:
		}
	}
}

// TestInterfaceSatisfaction breaks the build if the shared fakes drift from their interfaces.
func TestInterfaceSatisfaction(_ *testing.T) {
	var _ chatRecords = (*testChatStore)(nil)
	var _ ACPBridge = (*fakeBridge)(nil)
}

// TestShutdown_BoundsAWedgedHandler pins that a handler that never decrements inflight must not hold Shutdown, which
// runs synchronously inside webhttp.Run's pre-drain hook.
func TestShutdown_BoundsAWedgedHandler(t *testing.T) {
	h, _, _ := newTestHub()

	// A handler that never returns.
	h.lifecycle.inflight.Add(1)
	t.Cleanup(h.lifecycle.inflight.Done)

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	err := h.Shutdown(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Shutdown reported success while a handler was still in flight")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not carry context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "in-flight handlers") {
		t.Errorf("error %q does not name the wait that expired", err)
	}
	if elapsed > 20*budget {
		t.Errorf("Shutdown took %v on a %v budget", elapsed, budget)
	}
}

// stallingStopBridge's Stop blocks until released, like kiro-cli in its SIGTERM grace.
type stallingStopBridge struct {
	*fakeBridge

	release chan struct{}
}

func (b *stallingStopBridge) Stop() {
	<-b.release
	b.fakeBridge.Stop()
}

// The utility bridge's Stop is bounded by ctx too.
func TestShutdown_BoundsAStallingUtilityStop(t *testing.T) {
	h, _, _ := newTestHub()
	br := &stallingStopBridge{fakeBridge: newFakeBridge(), release: make(chan struct{})}
	t.Cleanup(func() { close(br.release) })
	s := &utilitySession{shutdownCtx: t.Context(), started: true, bridge: br}
	h.utility = &utilityLease{rt: &utilityRuntime{session: s, textgen: newUtilityAgent(s)}}

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "utility bridge teardown") {
			t.Errorf("Shutdown = %v, want an error naming the utility bridge teardown", err)
		}
	case <-time.After(20 * budget):
		t.Fatalf("Shutdown still running %v into a %v budget: the utility Stop is unbounded", 20*budget, budget)
	}
}

// TestShutdown_WaitsForARunningSweep pins that closing lifecycle.done does not say the loop left.
func TestShutdown_WaitsForARunningSweep(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	refs := func(context.Context) (map[string]struct{}, bool) {
		once.Do(func() { close(entered) })
		<-release
		return nil, false
	}
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(),
		func() ACPBridge { return newFakeBridge() }, cs,
		WithSessionReaper(kirosession.New(t.TempDir(), testReaperWorkDir), refs))
	cs.wire(h)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the orphan sweep never ran, so the fixture is holding nothing")
	}

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := h.Shutdown(ctx); err == nil {
		t.Error("Shutdown reported success while the orphan sweep was still running")
	} else if !strings.Contains(err.Error(), "background loops") {
		t.Errorf("error %q does not name the background-loop wait", err)
	}

	// Released, the loop returns: the control.
	close(release)
	shutdownHub(t, h)
}

// TestSweepSessionsLoop_WaitsForTheListenerToBind pins that the reaper roots at $KIRO_HOME, so a misconfigured boot
// could reap the real instance's sessions before App.Run binds. Shutdown waits on the loop group, so zero
// calls afterwards proves the gate held.
func TestSweepSessionsLoop_WaitsForTheListenerToBind(t *testing.T) {
	newGatedHub := func(t *testing.T, gate <-chan struct{}, refs func(context.Context) (map[string]struct{}, bool)) *Runtime {
		t.Helper()
		cs := newTestChatStore()
		h := New(context.Background(), t.TempDir(),
			func() ACPBridge { return newFakeBridge() }, cs,
			WithSessionReaper(kirosession.New(t.TempDir(), testReaperWorkDir), refs),
			WithSessionSweepGate(gate))
		cs.wire(h)
		return h
	}
	// Non-empty and complete, so the sweep reaches Reaper.Sweep.
	keepList := func(context.Context) (map[string]struct{}, bool) {
		return map[string]struct{}{"sess_live": {}}, true
	}

	t.Run("a gate that never closes reaps nothing", func(t *testing.T) {
		var calls atomic.Int32
		h := newGatedHub(t, make(chan struct{}), func(ctx context.Context) (map[string]struct{}, bool) {
			calls.Add(1)
			return keepList(ctx)
		})

		shutdownHub(t, h)

		if got := calls.Load(); got != 0 {
			t.Errorf("the keep-list was read %d time(s) with the listener unbound, want 0: "+
				"a boot that cannot bind is not the owner of its config dir and must not "+
				"reap another live process's session trees", got)
		}
	})

	t.Run("a closed gate releases the sweep", func(t *testing.T) {
		gate := make(chan struct{})
		asked := make(chan struct{}, 1)
		h := newGatedHub(t, gate, func(ctx context.Context) (map[string]struct{}, bool) {
			select {
			case asked <- struct{}{}:
			default:
			}
			return keepList(ctx)
		})
		t.Cleanup(func() { shutdownHub(t, h) })

		close(gate)

		select {
		case <-asked:
		case <-time.After(5 * time.Second):
			t.Fatal("the sweep never ran after the listener bound, so the gate is a " +
				"dead end and nothing reclaims KAS session state at all")
		}
	})
}

// TestShutdown_JoinsTheTickerLoops pins that with the reaper unwired the group holds exactly the two ticker loops.
func TestShutdown_JoinsTheTickerLoops(t *testing.T) {
	h, _, _ := newTestHub()

	waited := make(chan struct{})
	go func() {
		defer close(waited)
		h.lifecycle.loops.Wait()
	}()
	select {
	case <-waited:
		t.Fatal("the loop group is empty while the runtime runs: New's background loops are unjoinable")
	case <-time.After(100 * time.Millisecond):
	}

	shutdownHub(t, h)

	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Error("Shutdown returned with a background loop still running")
	}
}

// TestShutdown_WaitsForARunningMCPNotifier holds the notifier inside its callback (the environment.md
// generator): an unjoined loop here produces no other symptom.
func TestShutdown_WaitsForARunningMCPNotifier(t *testing.T) {
	h := newHubWithMCPConfig(nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.SetMCPOnChange(func() {
		once.Do(func() { close(entered) })
		<-release
	})
	// Any mutation signals the notifier, which debounces 100ms.
	h.mcpRegistry.RecordConnected(t.Context(), "a", marotte.MCPSource{}, nil, nil, nil, nil)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the notifier callback never ran, so the fixture is holding nothing")
	}

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := h.Shutdown(ctx); err == nil {
		t.Error("Shutdown reported success while the MCP notifier was still in its callback")
	} else if !strings.Contains(err.Error(), "background loops") {
		t.Errorf("error %q does not name the background-loop wait", err)
	}

	// Released, the loop joins: the control.
	close(release)
	shutdownHub(t, h)
}

type lifetimeWatchingBridge struct {
	*fakeBridge

	lifetime  context.Context
	stopErr   error
	sawLive   atomic.Bool
	stopCount atomic.Int32
}

func (b *lifetimeWatchingBridge) Stop() {
	b.stopErr = b.lifetime.Err()
	b.sawLive.Store(b.stopErr == nil)
	b.stopCount.Add(1)
	b.fakeBridge.Stop()
}

// TestShutdown_CancelsTheLifetimeAfterDrainingBridges pins that cancelling before the drain left a bridge dying on its
// own to reach the death closer with a dead context. close(lifecycle.done) stays at step 0.
func TestShutdown_CancelsTheLifetimeAfterDrainingBridges(t *testing.T) {
	cs := newTestChatStore()
	watcher := &lifetimeWatchingBridge{fakeBridge: newFakeBridge()}
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return watcher }, cs)
	watcher.lifetime = h.lifecycle.shutdownCtx
	cs.wire(h)

	sb := &sharedBridge{bridge: watcher}
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = sb
	h.bridge.mgr.mu.Unlock()

	shutdownHub(t, h)

	if watcher.stopCount.Load() == 0 {
		t.Fatal("the bridge was never stopped, so the ordering was not exercised")
	}
	if !watcher.sawLive.Load() {
		t.Errorf("the lifetime was already %v when the bridge was stopped; a bridge dying in that "+
			"window reaches the death closer with a dead context on an untracked goroutine",
			watcher.stopErr)
	}
	// The cancel still happens.
	if h.lifecycle.shutdownCtx.Err() == nil {
		t.Error("the lifetime is still live after Shutdown returned")
	}
}

// A notification about no chat (a pull request's CI flip) reaches the page as well as the push,
// so a reader with the app open sees it the way they see a turn's.
func TestNotify_AChatlessNoticeReachesThePageAndThePush(t *testing.T) {
	h, fp := newRunPushHub(t)
	n := marotte.NotificationPayload{
		Key:  "pr:github:github.com:a/b#7",
		Kind: marotte.PushKindPRStatus, Title: "a/b #7", Body: "Checks passed · Fix it",
	}
	h.Notify(t.Context(), &n)
	if got := awaitRunPush(t, fp); got.subject != n.PushSubject || got.body != n.Body {
		t.Errorf("Notify pushed %+v, want %q under %+v", got, n.Body, n.PushSubject)
	}
	frames := eventsOfType(h, marotte.EventNotification)
	if len(frames) != 1 {
		t.Fatalf("Notify broadcast %d notification frames, want 1", len(frames))
	}
	var got marotte.NotificationPayload
	if err := json.Unmarshal(frames[0].Payload, &got); err != nil {
		t.Fatalf("decoding the frame: %v", err)
	}
	if got != n {
		t.Errorf("the page's frame = %+v, want %+v", got, n)
	}
}
