package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/signin"
)

func TestEnsureResident_DrivingVerbsLoadTheHostSessionFirst(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		verb   func(t *testing.T, h *Runtime) error
	}{
		{name: "resume", method: methodKiroWorkflowResume, verb: func(t *testing.T, h *Runtime) error {
			return h.runs.resume(t.Context(), "wf_1", callerPage)
		}},
		{name: "step status, Continue, answer and message", method: methodKiroWorkflowUpdate, verb: func(t *testing.T, h *Runtime) error {
			return driveOnHost(t, h, methodKiroWorkflowUpdate)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			leaseParentless(t, h, "wf_1", "preset-at-launch")
			br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", ""))
			if err := tc.verb(t, h); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			calls := br.callLog()
			if !slices.Contains(calls, tc.method) {
				t.Fatalf("calls = %v, want %s", calls, tc.method)
			}
			if opts := br.lastSessionOpts(); opts == nil || opts.Role != marotte.SessionRoleHost || opts.SessionID != testLaunchSession {
				t.Errorf("door before the verb = %+v, want a host load of the launching session", opts)
			}
		})
	}
}

func TestReadVerbsNeedNoHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		verb func(t *testing.T, h *Runtime)
	}{
		{name: "inspect", verb: func(t *testing.T, h *Runtime) { h.runs.inspect(t.Context(), "wf_1") }},
		{name: "list", verb: func(t *testing.T, h *Runtime) { _, _ = h.runs.listRaw(t.Context()) }},
		{name: "pause", verb: func(t *testing.T, h *Runtime) {
			_ = h.runs.stopVerb(t.Context(), "wf_1", methodKiroWorkflowPause, map[string]any{keyWorkflowID: "wf_1"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			leaseParentless(t, h, "wf_1", "preset-at-launch")
			before := br.startCount()
			tc.verb(t, h)
			if got := br.startCount() - before; got != 0 {
				t.Errorf("%s opened %d session doors, want none", tc.name, got)
			}
			if h.bridge.mgr.get(runChatID("wf_1")) != nil {
				t.Errorf("%s made the run's host session resident", tc.name)
			}
		})
	}
}

// stepCloseRequested reports whether the engine lane was asked to close sessionID.
func stepCloseRequested(br *fakeBridge, sessionID string) bool {
	br.mu.Lock()
	defer br.mu.Unlock()
	p := br.lastParams[marotte.MethodSessionClose]
	return p != nil && p[marotte.KeySessionID] == sessionID
}

func TestStepTranscript_TerminalRunsStepSessionIsClosedAfterReplay(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "done")

	if _, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build"); err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !stepCloseRequested(br, "sess_pass0") {
		if time.Now().After(deadline) {
			t.Fatalf("no session/close for the terminal run's step session; calls were %v", br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStepTranscript_LiveRunsStepSessionIsNeverClosed(t *testing.T) {
	h, _, br := newTestHub()
	armStepReplay(br, "sess_pass0", "still going")
	br.mu.Lock()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: json.RawMessage(strings.ReplaceAll(stepInspect, `"completed"`, `"running"`)),
	}
	br.mu.Unlock()

	if _, err := h.Runs().stepTranscript(t.Context(), "wf_1", "wf_1:loop:iter-0:build"); err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	shutdownHub(t, h)
	if slices.Contains(br.callLog(), marotte.MethodSessionClose) {
		t.Error("a live run's step session was closed: close quiesces the session and would abort its step")
	}
}

// reloadMarked reports whether chatID's resident session is marked to reload at its next idle open.
func reloadMarked(h *Runtime, chatID marotte.ChatID) bool {
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.reload
}

func TestSettings_SessionDoorMoveReloadsSessionsAndNeverRestarts(t *testing.T) {
	h, dir, src := restartFixture(t)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeSetting(t, dir, settings.KeyMemoryMode, "learn")
	h.ReconcileSessionSettings(t.Context())
	if !reloadMarked(h, "c1") {
		t.Error("a session-door move left the resident session unmarked")
	}
	if h.engine.restartPending() {
		t.Errorf("restart pending after a session-door move, reasons %v", h.engine.status().Restart.Reasons)
	}
	assertNoRestart(t, src, "only a session door moved")
}

func TestSettings_EngineDoorMoveMakesRestartPending(t *testing.T) {
	h, dir, src := restartFixture(t)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	executingLease(t, h, "wf_1")
	pendRestart(t, h, dir, 1024)
	if st := h.engine.status(); !st.Restart.Pending || !slices.Equal(st.Restart.Reasons, []marotte.EngineReason{reasonHeap}) {
		t.Errorf("status = %+v, want a restart pending for %s", st.Restart, reasonHeap)
	}
	if reloadMarked(h, "c1") {
		t.Error("an engine-door move marked a session for reload; only the restart applies it")
	}
	assertNoRestart(t, src, "a run executes")
}

func TestSignIn_FirstSignInMarksResidentSessionsForReload(t *testing.T) {
	h, _, _ := restartFixture(t)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	h.onSignInChange(signin.Change{From: signin.StatusSignedIn, To: signin.StatusSignedIn})
	if reloadMarked(h, "c1") {
		t.Error("a change between two signed-in states marked the session for reload")
	}
	h.onSignInChange(signin.Change{From: signin.StatusSignedOut, To: signin.StatusSignedIn})
	if !reloadMarked(h, "c1") {
		t.Error("the first sign-in left the resident session unmarked, so its model picker stays empty")
	}
}

// flowBackend is a declBackend under id whose device flow is approved as soon as it begins.
type flowBackend struct {
	declBackend
	id signin.BackendID
}

func (b *flowBackend) Declaration() signin.Declaration {
	return signin.Declaration{Backend: b.id, Flows: []signin.FlowKind{signin.FlowDevice}}
}

func (*flowBackend) Begin(context.Context, signin.FlowParams) (signin.DeviceStart, error) {
	return signin.DeviceStart{}, nil
}

func (*flowBackend) Poll(context.Context, signin.DeviceStart) (signin.Credential, error) {
	return signin.Credential{Data: json.RawMessage(`{}`)}, nil
}

func TestSignIn_AnotherBackendsSignInLeavesTheEngineSessionsAlone(t *testing.T) {
	vault := openVault(t, t.TempDir(), &flowBackend{id: "test"}, &flowBackend{id: "other"})
	h, _ := reopenFixture(t, WithSignIn(vault, "test"))
	settleBoot(t, h)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// Registered after the runtime's listener, so a delivery here means the runtime's has returned.
	delivered := make(chan signin.BackendID, 2)
	vault.OnChange(func(c signin.Change) { delivered <- c.Backend })
	signIn := func(id signin.BackendID) {
		t.Helper()
		if _, err := vault.StartSignIn(t.Context(), id, signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
			t.Fatalf("StartSignIn(%s): %v", id, err)
		}
		select {
		case got := <-delivered:
			if got != id {
				t.Fatalf("delivered change for %q, want %q", got, id)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no change delivered for %q within 5s", id)
		}
	}

	signIn("other")
	if reloadMarked(h, "c1") {
		t.Error("another backend's sign-in marked the engine's session for reload")
	}
	signIn("test")
	if !reloadMarked(h, "c1") {
		t.Error("the engine backend's first sign-in left the resident session unmarked")
	}
}

// A host load's door runs on the runtime's lifetime: a caller whose context ends stops waiting and the load goes on,
// and a caller arriving during the load joins it rather than opening a second door.
func TestEnsureResident_ACallerWaitsOnlyItsOwnContextAndALaterCallerJoinsTheLoad(t *testing.T) {
	gate := make(chan struct{})
	var doors atomic.Int32
	h, _ := reopenFixtureWith(t, func(br *fakeBridge) {
		doors.Add(1)
		br.startGate = gate
	})
	settleBoot(t, h)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: "sess_host", Host: hostSec()})

	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.runs.ensureResident(short, "wf_1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller whose context ended = %v, want its own deadline", err)
	}
	joined := make(chan error, 1)
	go func() {
		_, err := h.runs.ensureResident(t.Context(), "wf_1")
		joined <- err
	}()
	close(gate)

	select {
	case err := <-joined:
		if err != nil {
			t.Fatalf("the joining caller = %v, want the load's session", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the joining caller did not return within 5s of the load finishing")
	}
	if got := doors.Load(); got != 1 {
		t.Errorf("host doors = %d, want 1: the abandoned wait and the join share one load", got)
	}
	if h.runs.residentRunHost("wf_1") == nil {
		t.Error("the load the first caller abandoned did not leave the host session resident")
	}
}
