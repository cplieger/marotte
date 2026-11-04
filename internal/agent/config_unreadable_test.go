package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// spawnOf is what generation e started with: its options and the kiro-cli it ran.
func spawnOf(e *fakeEngine) (*marotte.EngineOpts, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opts, e.path
}

// queueOverUnreadable pends a heap move, breaks config.json, raises want with move, and checks the whole spell: both
// reasons queued, no restart (automatic or driven), then one restart onto the repaired document. It returns the
// generation the repair started.
func queueOverUnreadable(t *testing.T, h *Runtime, dir string, src *fakeEngineSource, move func(), want marotte.EngineReason) *fakeEngine {
	t.Helper()
	executingLease(t, h, "wf_1")
	pendRestart(t, h, dir, 1024)
	if got := h.engine.status().Restart.Reasons; !slices.Equal(got, []marotte.EngineReason{reasonHeap}) {
		t.Fatalf("Setup: reasons = %v, want [%s]", got, reasonHeap)
	}

	rewriteConfigByHand(t, dir, `{"engine_heap_mib":`)
	move()
	h.ReconcileSessionSettings(t.Context())

	queued := slices.Sorted(slices.Values([]marotte.EngineReason{reasonHeap, want}))
	if got := h.engine.status().Restart.Reasons; !slices.Equal(got, queued) {
		t.Errorf("reasons while config.json cannot be read = %v, want %v: the heap move pending before the break stays queued", got, queued)
	}
	h.runs.disarmDeadline(t.Context(), "wf_1")
	assertNoRestart(t, src, "config.json cannot be read")

	h.engine.restartMu.Lock()
	h.engine.restart(&restartCause{Kind: restartAuto, Reasons: queued})
	h.engine.restartMu.Unlock()
	if n := src.generations(); n != 1 {
		t.Fatalf("generations after a driven automatic restart = %d, want 1: it restarted onto the defaults", n)
	}
	if e, gen := h.engine.current(); e == nil || gen != 1 {
		t.Errorf("after a refused restart the engine is %v on generation %d, want generation 1 ready", e, gen)
	}
	if got := h.engine.status().Restart.Reasons; !slices.Equal(got, queued) {
		t.Errorf("reasons after a refused restart = %v, want %v kept", got, queued)
	}

	rewriteConfigByHand(t, dir, `{"engine_heap_mib":1024}`)
	h.ReconcileSessionSettings(t.Context())
	assertOneRestart(t, h, src)
	e := src.current()
	if opts, _ := spawnOf(e); opts == nil || opts.HeapMiB != 1024 {
		t.Errorf("generation 2 started on %+v, want the repaired heap 1024", opts)
	}
	if h.engine.restartPending() {
		t.Errorf("reasons after the repair's restart = %v, want none", h.engine.status().Restart.Reasons)
	}
	return e
}

func TestUnreadableConfig_ATelemetryMoveIsQueuedAndAppliedOnceConfigReads(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, dir, src := restartFixture(t)

	e := queueOverUnreadable(t, h, dir, src, func() {
		writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)
	}, reasonTelemetry)

	if opts, _ := spawnOf(e); opts == nil || !opts.DisableTelemetry {
		t.Errorf("generation 2 started on %+v, want telemetry off", opts)
	}
}

func TestUnreadableConfig_AKiroCLIVersionMoveIsQueuedAndAppliedOnceConfigReads(t *testing.T) {
	h, dir, src := restartFixture(t)
	const next = "/fake/v2/kiro-cli"

	e := queueOverUnreadable(t, h, dir, src, func() { src.setCLIPath(next) }, marotte.EngineReasonKiroCLIVersion)

	if _, path := spawnOf(e); path != next {
		t.Errorf("generation 2 ran %q, want the new kiro-cli %q", path, next)
	}
}

// notLiveBackend is a sign-in backend whose account switch needs a new engine.
type notLiveBackend struct{ flowBackend }

func (*notLiveBackend) LiveSwitchable(_, _ signin.Credential) bool { return false }

func TestUnreadableConfig_AnAccountSwitchIsQueuedAndAppliedOnceConfigReads(t *testing.T) {
	vault := openVault(t, t.TempDir(), &notLiveBackend{flowBackend{id: "test"}})
	h, dir := reopenFixture(t, WithSignIn(vault, "test"))
	settleBoot(t, h)
	src := fakeSourceOf(t, h)
	// Registered after the runtime's listener, so a delivery here means the runtime's has returned.
	delivered := make(chan struct{}, 2)
	vault.OnChange(func(signin.Change) { delivered <- struct{}{} })
	signIn := func() {
		t.Helper()
		if _, err := vault.StartSignIn(t.Context(), "test", signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
			t.Fatalf("Setup: StartSignIn: %v", err)
		}
		select {
		case <-delivered:
		case <-time.After(5 * time.Second):
			t.Fatal("Setup: no sign-in change delivered within 5s")
		}
	}
	signIn()
	if _, rerr := vault.Answer(t.Context(), "test"); rerr != nil {
		t.Fatalf("Setup: the first sign-in did not answer the engine: %v", rerr)
	}

	queueOverUnreadable(t, h, dir, src, func() {
		signIn()
		if !vault.AccountPending("test") {
			t.Fatal("Setup: the second sign-in is not pending against the running engine")
		}
	}, marotte.EngineReasonAccountSwitch)

	if vault.AccountPending("test") {
		t.Error("the account is still pending after the repair's restart, want generation 2 on the new account")
	}
}

// lastReadableBody is the document the spawn tests boot on: every value a spawn reads from config.json set away
// from its default.
const lastReadableBody = `{"engine_heap_mib":1024,"debug_logs":true,"agent_ignore_files":[".gitignore"],` +
	`"terminal_command_timeout_ms":300000,"content_collection_enabled":true}`

// lastReadableHub boots on lastReadableBody with one fake as every session and the engine lane, then breaks
// config.json.
func lastReadableHub(t *testing.T) (*Runtime, *fakeEngineSource, *fakeBridge) {
	t.Helper()
	dir := writeSettings(t, lastReadableBody)
	lane := newFakeBridge()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(func() engineSession { return lane }, lane), cs,
		WithConfigDir(dir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	rewriteConfigByHand(t, dir, `{"agent_ignore_files":`)
	return h, fakeSourceOf(t, h), lane
}

// assertStartedOnTheLastReadable checks generation 2 started on lastReadableBody's values, never the defaults.
func assertStartedOnTheLastReadable(t *testing.T, h *Runtime, src *fakeEngineSource, lane *fakeBridge) {
	t.Helper()
	e := src.current()
	if opts, _ := spawnOf(e); opts == nil || opts.HeapMiB != 1024 || !opts.DebugLogs {
		t.Errorf("generation 2 started on %+v, want heap 1024 and debug logs from the last readable config.json", opts)
	}
	if files, ms := e.StartLive(); !slices.Equal(files, []string{".kiroignore", ".gitignore"}) || ms != 300000 {
		t.Errorf("generation 2 was sent ignore files %v and shell timeout %d, want [.kiroignore .gitignore] and 300000", files, ms)
	}
	openChat(t, h, "c2")
	lane.mu.Lock()
	applied := lane.contentCollectionApplied
	lane.mu.Unlock()
	if !applied {
		t.Error("a door on generation 2 asserted content collection off, want the last readable switch on")
	}
	if h.engine.restartPending() {
		t.Errorf("reasons after the spawn = %v, want none: it started on what the last readable document holds",
			h.engine.status().Restart.Reasons)
	}
}

func TestUnreadableConfig_ACrashRespawnStartsOnTheLastReadableSettings(t *testing.T) {
	h, src, lane := lastReadableHub(t)

	src.current().crash()
	waitGenerations(t, src, 2)
	waitReady(t, h.engine, 2)

	assertStartedOnTheLastReadable(t, h, src, lane)
}

func TestUnreadableConfig_AConfirmedRestartWithNothingQueuedStartsOnTheLastReadableSettings(t *testing.T) {
	h, src, lane := lastReadableHub(t)
	if h.engine.restartPending() {
		t.Fatalf("Setup: reasons = %v before the restart, want none", h.engine.status().Restart.Reasons)
	}

	rec := httptest.NewRecorder()
	h.handleEngineRestart(rec, httptest.NewRequest(http.MethodPost, "/api/engine/restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("Restart now = %d, want 202: nothing is queued, so the restart reproduces what runs", rec.Code)
	}
	assertOneRestart(t, h, src)

	assertStartedOnTheLastReadable(t, h, src, lane)
}

// A boot over a file that never read starts on the defaults; a live reason raised then waits for the file too.
func TestUnreadableConfig_ABootOverABrokenFileQueuesALiveReasonUntilConfigReads(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	dir := writeSettings(t, `{"engine_heap_mib":`)
	h := reopenFixtureIn(t, dir, nil)
	settleBoot(t, h)
	src := fakeSourceOf(t, h)
	executingLease(t, h, "wf_1")

	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)
	h.ReconcileSessionSettings(t.Context())
	if got := h.engine.status().Restart.Reasons; !slices.Equal(got, []marotte.EngineReason{reasonTelemetry}) {
		t.Errorf("reasons = %v, want [%s]", got, reasonTelemetry)
	}
	h.runs.disarmDeadline(t.Context(), "wf_1")
	assertNoRestart(t, src, "config.json has never been readable")

	rewriteConfigByHand(t, dir, `{"engine_heap_mib":1024}`)
	h.ReconcileSessionSettings(t.Context())
	assertOneRestart(t, h, src)
	if opts, _ := spawnOf(src.current()); opts == nil || !opts.DisableTelemetry || opts.HeapMiB != 1024 {
		t.Errorf("generation 2 started on %+v, want telemetry off and the repaired heap 1024", opts)
	}
}
