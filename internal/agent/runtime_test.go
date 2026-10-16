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

// newHangingEngine is a source whose engine lane parks session/prompt until the engine stops, so Shutdown must stop
// the engine before inflight.Wait.
func newHangingEngine() (src *fakeEngineSource, stopCalled *atomic.Bool) {
	released := make(chan struct{})
	lane := newFakeBridge()
	lane.blockOn = map[string]chan struct{}{marotte.MethodPrompt: released}
	src = fakeEngineOf(func() engineSession { return newFakeBridge() }, lane)
	stopCalled = new(atomic.Bool)
	var once sync.Once
	src.stopHook = func() {
		stopCalled.Store(true)
		once.Do(func() { close(released) })
	}
	return src, stopCalled
}

func TestShutdown_StopsTheEngineBeforeWaitingOnInflight(t *testing.T) {
	cs := newTestChatStore()
	src, stopCalled := newHangingEngine()
	h := newRuntimeOn(t.Context(), "/tmp/work", src, cs)
	cs.wire(h)
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}

	// An in-flight call awaiting the engine.
	h.lifecycle.inflight.Add(1)
	callDone := make(chan struct{})
	go func() {
		defer h.lifecycle.inflight.Done()
		_, _ = src.current().Call(t.Context(), marotte.MethodPrompt, nil)
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
		t.Fatal("Shutdown deadlocked waiting for an in-flight call")
	}
	select {
	case <-callDone:
	case <-time.After(1 * time.Second):
		t.Error("the call never returned despite the engine stop")
	}
	if !stopCalled.Load() {
		t.Error("Stop was not called on the engine")
	}
}

// TestInterfaceSatisfaction breaks the build if the shared fakes drift from their interfaces.
func TestInterfaceSatisfaction(_ *testing.T) {
	var _ chatRecords = (*testChatStore)(nil)
	var _ engineSession = (*fakeBridge)(nil)
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

// stallingStopEngine's Stop blocks until release closes, like kiro-cli in its SIGTERM grace.
func stallingStopEngine(release <-chan struct{}) *fakeEngineSource {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.stopHook = func() { <-release }
	return src
}

// The one engine stop is bounded by ctx.
func TestShutdown_BoundsAStallingEngineStop(t *testing.T) {
	release := make(chan struct{})
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), stallingStopEngine(release), cs)
	cs.wire(h)
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	t.Cleanup(func() { close(release) })

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "engine teardown") {
			t.Errorf("Shutdown = %v, want an error naming the engine teardown", err)
		}
	case <-time.After(20 * budget):
		t.Fatalf("Shutdown still running %v into a %v budget: the engine stop is unbounded", 20*budget, budget)
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
	h := newRuntimeOn(context.Background(), t.TempDir(),
		fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs,
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
		h := newRuntimeOn(context.Background(), t.TempDir(),
			fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs,
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
	h := newHubWithMCPConfig(t, nil)

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

// lifetimeWatchingEngine records whether the runtime's lifetime was still live when the engine was stopped.
type lifetimeWatchingEngine struct {
	lifetime  context.Context
	stopErr   error
	sawLive   atomic.Bool
	stopCount atomic.Int32
}

func (w *lifetimeWatchingEngine) stop() {
	w.stopErr = w.lifetime.Err()
	w.sawLive.Store(w.stopErr == nil)
	w.stopCount.Add(1)
}

// TestShutdown_CancelsTheLifetimeAfterStoppingTheEngine pins that cancelling before the drain and the engine stop
// left a session ending in that window to reach the death closer with a dead context. close(lifecycle.done) stays
// at step 0.
func TestShutdown_CancelsTheLifetimeAfterStoppingTheEngine(t *testing.T) {
	cs := newTestChatStore()
	watcher := &lifetimeWatchingEngine{}
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.stopHook = watcher.stop
	h := newRuntimeOn(t.Context(), t.TempDir(), src, cs)
	watcher.lifetime = h.lifecycle.shutdownCtx
	cs.wire(h)
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle})

	shutdownHub(t, h)

	if watcher.stopCount.Load() == 0 {
		t.Fatal("the engine was never stopped, so the ordering was not exercised")
	}
	if !watcher.sawLive.Load() {
		t.Errorf("the lifetime was already %v when the engine was stopped; a session ending in that "+
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
