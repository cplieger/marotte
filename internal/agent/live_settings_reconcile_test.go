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
	first, err := h.coord.OpenBridge(t.Context(), "c1", "")
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
	first, err := h.coord.OpenBridge(t.Context(), "c1", "")
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
