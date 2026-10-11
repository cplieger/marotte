package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func TestReconcileSessionSettings_AFailedMCPRenderIsLogged(t *testing.T) {
	logs := captureLogs(t)
	h, configDir := reopenFixture(t, WithKASMCPRenderer(&fakeMCPRender{render: failingRender}))

	writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
	h.ReconcileSessionSettings(t.Context())

	if !strings.Contains(logs.String(), "re-rendering the MCP config failed") {
		t.Errorf("a failed MCP render logged nothing; the operator learns the wait setting did not apply only there:\n%s", logs.String())
	}
}

type fakeMCPRender struct {
	render  func(ctx context.Context, attempt int) (wait bool, err error)
	mu      sync.Mutex
	renders int
	wait    bool
	unknown bool
}

func (r *fakeMCPRender) RenderKASConfig(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renders++
	wait, err := r.render(ctx, r.renders)
	if err != nil {
		return err
	}
	r.wait, r.unknown = wait, false
	return nil
}

func (r *fakeMCPRender) RenderedWaitForReady() (waitForReady, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wait, !r.unknown
}

func (r *fakeMCPRender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.renders
}

var errKASUnwritable = errors.New("mcp.json not writable")

func failingRender(context.Context, int) (bool, error) { return false, errKASUnwritable }

func writesTrue(ctx context.Context, _ int) (bool, error) { return true, ctx.Err() }

const nonDefaultLiveValues = `{"agent_ignore_files":[".gitignore"],"terminal_command_timeout_ms":300000,` +
	`"content_collection_enabled":true,"mcp_wait_for_ready":true,"debug_logs":true,"notify_pr_status":true}`

func TestReconcileSessionSettings_ACancelledCallerStillDeliversTheMoveItFound(t *testing.T) {
	render := &fakeMCPRender{render: writesTrue}
	h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
	writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	h.ReconcileSessionSettings(ctx)

	if got := render.count(); got != 1 {
		t.Errorf("a reconcile whose caller had gone rendered the moved MCP wait %d times, want 1; the baseline records it, so nothing would send it later", got)
	}
}

func TestReconcileSessionSettings_ACancelledCallerSeesNoMoveOverNonDefaultValues(t *testing.T) {
	rec := newLiveRecorder(t)
	h, configDir := reopenFixture(t, rec.options()...)
	rec.openWithStore(t, configDir)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	rewriteConfigByHand(t, configDir, nonDefaultLiveValues)
	h.ReconcileSessionSettings(t.Context())
	before := pushesSeen(fakeOf(first), rec)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	h.ReconcileSessionSettings(ctx)

	if got := pushesSeen(fakeOf(first), rec); !reflect.DeepEqual(got, before) {
		t.Errorf("a cancelled caller over %s pushed %+v after %+v, want nothing: its reads answered defaults", nonDefaultLiveValues, got, before)
	}
}

func TestReconcileSessionSettings_TwoPushesOfOneSurfaceLandInWriteOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var delivered []bool
		release := make(chan struct{})
		h, configDir := reopenFixture(t, WithDebugLogs(func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(delivered) > 0 && delivered[len(delivered)-1]
		}, func(on bool) {
			mu.Lock()
			delivered = append(delivered, on)
			first := len(delivered) == 1
			mu.Unlock()
			if first {
				<-release
			}
		}))
		seen := func() []bool {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(delivered)
		}

		writeSetting(t, configDir, settings.KeyDebugLogs, true)
		go h.ReconcileSessionSettings(context.Background())
		synctest.Wait()
		writeSetting(t, configDir, settings.KeyDebugLogs, false)
		go h.ReconcileSessionSettings(context.Background())
		synctest.Wait()
		if got := seen(); !slices.Equal(got, []bool{true}) {
			t.Errorf("with the debug_logs true push in flight the setter received %v, want [true]: a second push overlapped it", got)
		}
		close(release)
		synctest.Wait()

		if got := seen(); !slices.Equal(got, []bool{true, false}) {
			t.Errorf("debug_logs written true then false delivered %v, want [true false]: the process must end on the newer value", got)
		}
	})
}

func TestReconcileSessionSettings_AFailedMCPRenderIsRetriedWithNoFurtherEdit(t *testing.T) {
	render := &fakeMCPRender{render: func(ctx context.Context, n int) (bool, error) {
		if n == 1 {
			return false, errKASUnwritable
		}
		return writesTrue(ctx, n)
	}}
	h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
	writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
	h.ReconcileSessionSettings(t.Context())

	h.ReconcileSessionSettings(t.Context())

	if got := render.count(); got != 2 {
		t.Fatalf("after a failed render the next reconcile rendered %d times in all, want 2", got)
	}
	writeSetting(t, configDir, settings.KeyDebugLogs, true)
	h.ReconcileSessionSettings(t.Context())
	if got := render.count(); got != 2 {
		t.Errorf("rendered %d times, want 2: a reconcile after the retry landed rendered again", got)
	}
}

func TestReconcileSessionSettings_AnUnreadableDocumentPushesNoDefaults(t *testing.T) {
	rec := newLiveRecorder(t)
	h, configDir := reopenFixture(t, rec.options()...)
	rec.openWithStore(t, configDir)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	rewriteConfigByHand(t, configDir, nonDefaultLiveValues)
	h.ReconcileSessionSettings(t.Context())
	before := pushesSeen(fakeOf(first), rec)

	rewriteConfigByHand(t, configDir, "{half typed")
	h.ReconcileSessionSettings(t.Context())
	rewriteConfigByHand(t, configDir, nonDefaultLiveValues)
	h.ReconcileSessionSettings(t.Context())

	got := pushesSeen(fakeOf(first), rec)
	got.ignore, before.ignore = nil, nil
	if !reflect.DeepEqual(got, before) {
		t.Errorf("config.json broken and restored to the same values pushed %+v after %+v, want nothing", got, before)
	}
}

func TestReconcileSessionSettings_AUtilityTakenForRestartGetsTheContentCollectionItMissedBeforeItStops(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, configDir := reopenFixture(t)
	utility := acquireUtility(t, h)
	utility.setCallErr(marotte.MethodSetConfigOption, errors.New("broken pipe"))
	rewriteConfigByHand(t, configDir, `{"content_collection_enabled":true}`)
	h.ReconcileSessionSettings(t.Context())

	var mu sync.Mutex
	var stoppedAtAssert []bool
	utility.mu.Lock()
	delete(utility.callErrs, marotte.MethodSetConfigOption)
	utility.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodSetConfigOption {
			mu.Lock()
			stoppedAtAssert = append(stoppedAtAssert, stopped(utility))
			mu.Unlock()
		}
		return nil, nil, false
	}
	utility.mu.Unlock()
	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

	h.ReconcileSessionSettings(t.Context())

	if !stopped(utility) {
		t.Fatal("Setup: turning telemetry off did not restart the utility session")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(stoppedAtAssert, []bool{false}) {
		t.Errorf("content-collection asserts on the restarting utility process (stopped at each: %v), want one before it stopped: "+
			"it serves held leases until then, opted in", stoppedAtAssert)
	}
}

func TestReconcileSessionSettings_AnMCPRenderOfAnotherWaitValueIsRetried(t *testing.T) {
	render := &fakeMCPRender{render: func(_ context.Context, n int) (bool, error) { return n > 1, nil }}
	h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
	writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
	h.ReconcileSessionSettings(t.Context())

	h.ReconcileSessionSettings(t.Context())

	if got := render.count(); got != 2 {
		t.Errorf("after a render that wrote waitForReady false the next reconcile rendered %d times in all, want 2", got)
	}
}

// openWithin opens chatID and returns how long the open took on the bubble's clock, failing once it outlasts limit; the
// bubble's tickers keep that clock moving while the open is parked.
func openWithin(t *testing.T, h *Runtime, chatID marotte.ChatID, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	opened := make(chan error, 1)
	go func() {
		_, err := h.coord.openBridge(t.Context(), chatID, "")
		opened <- err
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("OpenBridge %s: %v", chatID, err)
		}
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("OpenBridge %s had not returned after %v", chatID, limit)
		return 0
	}
}

func TestOpenBridge_AnMCPRenderQueuedBehindAHeldStoreWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		storeHeld := make(chan struct{})
		release := sync.OnceFunc(func() { close(storeHeld) })
		render := &fakeMCPRender{render: func(ctx context.Context, n int) (bool, error) {
			if n == 1 {
				select {
				case <-storeHeld:
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
			return true, nil
		}}
		h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
		t.Cleanup(release)
		writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
		logs := captureLogs(t)

		if waited := openWithin(t, h, "c1", 2*ignorePushTimeout); waited > ignorePushTimeout {
			t.Errorf("OpenBridge with the MCP store held returned after %v, want at most the %v push bound", waited, ignorePushTimeout)
		}
		if out := logs.String(); !strings.Contains(out, `"level":"WARN","msg":"settings: the MCP render waited past the bound`) ||
			strings.Contains(out, `"level":"ERROR"`) {
			t.Errorf("a render that gave up at the bound logged:\n%s\nwant one Warn and no Error: it is retried at the next open", out)
		}

		release()
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge once the store is free: %v", err)
		}
		if got := render.count(); got != 2 {
			t.Errorf("rendered %d times over two opens, want 2: the render that gave up is retried at the next open", got)
		}
		if wait, known := render.RenderedWaitForReady(); !known || !wait {
			t.Errorf("after the retry the rendered wait is (%t, %t), want (true, true)", wait, known)
		}
	})
}

func TestOpenBridge_AUtilityRestartWhoseStopHangsWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
		h, _ := reopenFixture(t)
		utility := acquireUtility(t, h)
		teardown := make(chan struct{})
		finish := sync.OnceFunc(func() { close(teardown) })
		t.Cleanup(finish)
		utility.mu.Lock()
		utility.stopGate = teardown
		utility.mu.Unlock()
		writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

		if waited := openWithin(t, h, "c1", 2*ignorePushTimeout); waited > ignorePushTimeout {
			t.Errorf("OpenBridge restarting a utility whose stop hangs returned after %v, want at most the %v push bound",
				waited, ignorePushTimeout)
		}
		if stopped(utility) {
			t.Fatal("Setup: the utility process stopped through a held teardown")
		}

		finish()
		synctest.Wait()
		if !stopped(utility) {
			t.Error("the utility process taken for restart was not stopped once its teardown could finish")
		}
	})
}

func TestOpenBridge_AnMCPRenderAndAUtilityStopBothHeldShareThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
		storeHeld := make(chan struct{})
		releaseStore := sync.OnceFunc(func() { close(storeHeld) })
		render := &fakeMCPRender{render: func(ctx context.Context, _ int) (bool, error) {
			select {
			case <-storeHeld:
			case <-ctx.Done():
				return false, ctx.Err()
			}
			return true, nil
		}}
		h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
		t.Cleanup(releaseStore)
		utility := acquireUtility(t, h)
		teardown := make(chan struct{})
		finish := sync.OnceFunc(func() { close(teardown) })
		t.Cleanup(finish)
		utility.mu.Lock()
		utility.stopGate = teardown
		utility.mu.Unlock()
		writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
		writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

		if waited := openWithin(t, h, "c1", 3*ignorePushTimeout); waited > ignorePushTimeout {
			t.Errorf("OpenBridge with the MCP store held and a utility stop hanging returned after %v, want at most the %v push bound in all",
				waited, ignorePushTimeout)
		}
		if got := render.count(); got != 1 {
			t.Fatalf("Setup: the open rendered %d times, want 1", got)
		}
		if got, want := logs.String(), `"msg":"utility session stop still running; it finishes in the background","waited_ms":0}`; !strings.Contains(got, want) {
			t.Errorf("the render spent the whole bound, so the utility stop got no wait; logs lack %s:\n%s", want, got)
		}
		finish()
		synctest.Wait()
		if !stopped(utility) {
			t.Error("the utility process taken for restart was not stopped once its teardown could finish")
		}
	})
}

// returnsWithin runs fn and returns how long it took on the bubble's clock, failing once it outlasts limit.
func returnsWithin(t *testing.T, name string, limit time.Duration, fn func()) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("%s had not returned after %v", name, limit)
		return 0
	}
}

func TestReconcileSessions_QueuedBehindAHeldProcessSyncWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		render := &fakeMCPRender{render: func(context.Context, int) (bool, error) { return true, nil }}
		h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
		writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
		if !h.processLive.lock.lock(t.Context()) {
			t.Fatal("Setup: could not take the process sync lock")
		}
		unlock := sync.OnceFunc(h.processLive.lock.unlock)
		t.Cleanup(unlock)

		reconcile := func() { h.coord.reconcileSessions(t.Context(), time.Now().Add(ignorePushTimeout)) }
		if waited := returnsWithin(t, "the open's reconcile", 3*ignorePushTimeout, reconcile); waited > ignorePushTimeout {
			t.Errorf("the open's reconcile queued behind a held process sync returned after %v, want at most the %v push bound",
				waited, ignorePushTimeout)
		}
		if got := render.count(); got != 0 {
			t.Fatalf("Setup: the reconcile rendered %d times through a held lock, want 0", got)
		}

		unlock()
		h.coord.reconcileSessions(t.Context(), time.Now().Add(ignorePushTimeout))
		if got := render.count(); got != 1 {
			t.Errorf("rendered %d times once the lock was free, want 1: the sync that gave up is retried at the next use", got)
		}
	})
}

func TestOpenBridge_QueuedBehindAHeldProcessSyncWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := reopenFixture(t)
		if !h.processLive.lock.lock(t.Context()) {
			t.Fatal("Setup: could not take the process sync lock")
		}
		t.Cleanup(h.processLive.lock.unlock)

		if waited := openWithin(t, h, "c1", 3*ignorePushTimeout); waited > ignorePushTimeout {
			t.Errorf("OpenBridge queued behind a held process sync, at its reconcile and its unreadable report, returned after %v, "+
				"want at most the %v push bound in all", waited, ignorePushTimeout)
		}
	})
}

func TestReconcileSessions_ARenderAfterAWaitForTheSyncLockGetsOnlyWhatIsLeftOfTheBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		render := &fakeMCPRender{render: func(ctx context.Context, _ int) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		}}
		h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
		writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)
		if !h.processLive.lock.lock(t.Context()) {
			t.Fatal("Setup: could not take the process sync lock")
		}
		time.AfterFunc(ignorePushTimeout/2, h.processLive.lock.unlock)

		reconcile := func() { h.coord.reconcileSessions(t.Context(), time.Now().Add(ignorePushTimeout)) }
		if waited := returnsWithin(t, "the open's reconcile", 3*ignorePushTimeout, reconcile); waited > ignorePushTimeout {
			t.Errorf("the open's reconcile, half its bound spent waiting for the sync lock, returned after %v, want at most the %v push bound",
				waited, ignorePushTimeout)
		}
		if got := render.count(); got != 1 {
			t.Errorf("Setup: the reconcile rendered %d times, want 1 once the lock was free", got)
		}
	})
}

func TestSyncProcessLive_QueuedBehindAHeldSyncWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		h, _ := reopenFixture(t)
		if !h.processLive.lock.lock(t.Context()) {
			t.Fatal("Setup: could not take the process sync lock")
		}
		t.Cleanup(h.processLive.lock.unlock)

		syncNow := func() { h.syncProcessLive(t.Context()) }
		if waited := returnsWithin(t, "syncProcessLive", 3*ignorePushTimeout, syncNow); waited > ignorePushTimeout {
			t.Errorf("a reconnect's process sync queued behind a held one returned after %v, want at most the %v push bound",
				waited, ignorePushTimeout)
		}
		if got := logs.String(); !strings.Contains(got, syncLockWaitWarn) {
			t.Errorf("a process sync that gave up behind a held lock logged no %q line:\n%s", syncLockWaitWarn, got)
		}
	})
}

const (
	syncLockWaitWarn   = `"msg":"settings: another live-settings sync held the lock past the bound; the MCP render`
	reportLockWaitWarn = `"msg":"settings: another live-settings sync held the lock past the bound; an unreadable agent ignore list`
)

func TestProcessSync_ACallerWhoLeftWhileQueuedLogsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Runtime, context.Context)
		warn string
	}{
		{name: "sync", run: (*Runtime).syncProcessLive, warn: syncLockWaitWarn},
		{name: "unreadable_report", run: (*Runtime).reportUnreadableOwed, warn: reportLockWaitWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			h, _ := reopenFixture(t)
			if !h.processLive.lock.lock(t.Context()) {
				t.Fatal("Setup: could not take the process sync lock")
			}
			t.Cleanup(h.processLive.lock.unlock)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			tc.run(h, ctx)

			if got := logs.String(); strings.Contains(got, tc.warn) {
				t.Errorf("%s queued behind a held lock, whose caller had left, logged the give-up meant for its bound:\n%s", tc.name, got)
			}
		})
	}
}

func TestReportUnreadableOwed_QueuedBehindAHeldProcessSyncWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		h, _ := reopenFixture(t)
		if !h.processLive.lock.lock(t.Context()) {
			t.Fatal("Setup: could not take the process sync lock")
		}
		t.Cleanup(h.processLive.lock.unlock)

		report := func() { h.reportUnreadableOwed(t.Context()) }
		if waited := returnsWithin(t, "reportUnreadableOwed", 3*ignorePushTimeout, report); waited > ignorePushTimeout {
			t.Errorf("reportUnreadableOwed queued behind a held process sync returned after %v, want at most the %v push bound",
				waited, ignorePushTimeout)
		}
		if got := logs.String(); !strings.Contains(got, reportLockWaitWarn) {
			t.Errorf("an unreadable report that gave up behind a held lock logged no %q line:\n%s", reportLockWaitWarn, got)
		}
	})
}

func TestOpenBridge_ReopeningARetiredChatWithTheMCPStoreHeldWaitsOnlyThePushBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		storeHeld := make(chan struct{})
		release := sync.OnceFunc(func() { close(storeHeld) })
		render := &fakeMCPRender{render: func(ctx context.Context, _ int) (bool, error) {
			select {
			case <-storeHeld:
			case <-ctx.Done():
				return false, ctx.Err()
			}
			return true, nil
		}}
		h, configDir := reopenFixture(t, WithKASMCPRenderer(render))
		t.Cleanup(release)
		first, err := h.coord.openBridge(t.Context(), "c1", "")
		if err != nil {
			t.Fatalf("Setup: OpenBridge: %v", err)
		}
		writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")
		writeSetting(t, configDir, settings.KeyMCPWaitForReady, true)

		if waited := openWithin(t, h, "c1", 3*ignorePushTimeout); waited > ignorePushTimeout {
			t.Errorf("OpenBridge reopening a retired chat with the MCP store held returned after %v, want at most the %v push bound in all",
				waited, ignorePushTimeout)
		}
		if !stopped(fakeOf(first)) {
			t.Fatal("Setup: the open kept the bridge spawned under the old profile")
		}

		release()
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge once the store is free: %v", err)
		}
		if wait, known := render.RenderedWaitForReady(); !known || !wait {
			t.Errorf("after the next open the rendered wait is (%t, %t), want (true, true): the render that gave up is retried there", wait, known)
		}
		// The reopen's session load resumes runs through the utility session on its own goroutine; it must finish
		// before the fixture's Shutdown stops that session, or the bubble ends with its forward loop parked.
		synctest.Wait()
	})
}

func TestOpenBridge_AUtilityRestartOnceDrainingLeavesTheStopToShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
		h, _ := reopenFixture(t)
		utility := acquireUtility(t, h)
		teardown := make(chan struct{})
		finish := sync.OnceFunc(func() { close(teardown) })
		t.Cleanup(finish)
		utility.mu.Lock()
		utility.stopGate = teardown
		utility.mu.Unlock()
		writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)
		h.lifecycle.draining.Store(true)

		if waited := openWithin(t, h, "c1", 2*ignorePushTimeout); waited > ignorePushTimeout {
			t.Errorf("OpenBridge restarting a utility whose stop hangs, once draining, returned after %v, want at most the %v push bound",
				waited, ignorePushTimeout)
		}
		if h.utility.peek() == nil {
			t.Fatal("once draining, the restart took the utility session, which Shutdown's own utility stop no longer sees")
		}

		finish()
		h.stopUtilityBridge()
		if !stopped(utility) {
			t.Error("Shutdown's utility stop did not stop the session the restart left to it")
		}
	})
}
