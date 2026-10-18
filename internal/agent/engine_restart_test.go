package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
)

// quietWindow is how long a test watches for a restart that must not happen.
const quietWindow = 50 * time.Millisecond

// restartFixture is a runtime over a config directory whose engine is ready and whose boot work drained.
func restartFixture(t *testing.T) (*Runtime, string, *fakeEngineSource) {
	t.Helper()
	h, dir := reopenFixture(t)
	settleBoot(t, h)
	return h, dir, fakeSourceOf(t, h)
}

// pendRestart moves an engine-door setting, so a restart is pending.
func pendRestart(t *testing.T, h *Runtime, dir string, heapMiB int) {
	t.Helper()
	writeSetting(t, dir, settings.KeyEngineHeapMiB, heapMiB)
	h.ReconcileSessionSettings(t.Context())
}

// assertNoRestart checks the engine stays on generation 1 for the quiet window.
func assertNoRestart(t *testing.T, src *fakeEngineSource, why string) {
	t.Helper()
	time.Sleep(quietWindow)
	if n := src.generations(); n != 1 {
		t.Fatalf("generations = %d while %s, want 1: an automatic restart fired", n, why)
	}
}

// assertOneRestart waits for exactly one restart.
func assertOneRestart(t *testing.T, h *Runtime, src *fakeEngineSource) {
	t.Helper()
	waitGenerations(t, src, 2)
	waitReady(t, h.engine, 2)
	time.Sleep(quietWindow)
	if n := src.generations(); n != 2 {
		t.Errorf("generations = %d, want exactly one restart", n)
	}
}

func executingLease(t *testing.T, h *Runtime, wf string) {
	t.Helper()
	leaseRun(t, h, runlease.Lease{WorkflowID: wf, Deadline: time.Now().Add(time.Hour)})
}

func TestEngineRestart_FiresAtQuiescence(t *testing.T) {
	h, dir, src := restartFixture(t)
	pendRestart(t, h, dir, 1024)
	assertOneRestart(t, h, src)
	if h.engine.restartPending() {
		t.Error("restart still pending on the engine spawned with the new value")
	}
}

func TestEngineRestart_DoesNotFireWhileARunExecutes(t *testing.T) {
	h, dir, src := restartFixture(t)
	executingLease(t, h, "wf_1")
	pendRestart(t, h, dir, 1024)
	assertNoRestart(t, src, "a run executes")
	st := h.engine.status()
	if !st.Restart.Pending || !slices.ContainsFunc(st.Busy.Runs, func(r marotte.EngineBusyRun) bool { return r.WorkflowID == "wf_1" }) {
		t.Errorf("status = %+v, want a pending restart naming the executing run as busy", st)
	}
	h.runs.disarmDeadline(t.Context(), "wf_1")
	assertOneRestart(t, h, src)
}

func TestEngineRestart_PendingClearsWhenTheSettingIsChangedBack(t *testing.T) {
	h, dir, src := restartFixture(t)
	executingLease(t, h, "wf_1")
	pendRestart(t, h, dir, 1024)
	if !h.engine.restartPending() {
		t.Fatal("Setup: no restart pending after an engine-door move")
	}
	pendRestart(t, h, dir, 0)
	if h.engine.restartPending() {
		t.Errorf("restart pending after the setting moved back, reasons %v", h.engine.status().Restart.Reasons)
	}
	h.runs.disarmDeadline(t.Context(), "wf_1")
	assertNoRestart(t, src, "nothing is pending any more")
}

// Every quiescence term holds an automatic restart off, and its own closing edge fires exactly one.
func TestEngineRestart_AutoNeverFiresWhileAnyQuiescenceTermHolds(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold func(t *testing.T, h *Runtime) (release func())
	}{
		{name: "an admitted prompt", hold: func(t *testing.T, h *Runtime) func() {
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("Setup: OpenBridge: %v", err)
			}
			if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
				t.Fatal("Setup: the reservation was refused")
			}
			return func() { h.coord.ReleaseTurnReservation("c1") }
		}},
		{name: "an executing run", hold: func(t *testing.T, h *Runtime) func() {
			executingLease(t, h, "wf_1")
			return func() { h.runs.disarmDeadline(t.Context(), "wf_1") }
		}},
		{name: "an unsettled run mutation", hold: func(_ *testing.T, h *Runtime) func() {
			m := h.runs.mutations.enter("wf_1", mutationDrive, 1)
			return m.unwritten
		}},
		{name: "a session door or engine call in flight", hold: func(t *testing.T, h *Runtime) func() {
			_, _, release, err := h.engine.acquire(t.Context())
			if err != nil {
				t.Fatalf("Setup: acquire: %v", err)
			}
			return release
		}},
		{name: "a released run", hold: func(t *testing.T, h *Runtime) func() {
			executingLease(t, h, "wf_1")
			return func() { h.runs.releaseLease(t.Context(), "wf_1") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, src := restartFixture(t)
			release := tc.hold(t, h)
			pendRestart(t, h, dir, 1024)
			assertNoRestart(t, src, tc.name+" holds")
			release()
			assertOneRestart(t, h, src)
		})
	}
}

// An open turn, not only an admission, holds an automatic restart, and its close is the edge that fires it.
func TestEngineRestart_WaitsForOpenTurns(t *testing.T) {
	h, dir, src := restartFixture(t)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: OpenBridge: %v", err)
	}
	turn := openTestTurn(t, h.coord.turns, "c1")
	pendRestart(t, h, dir, 1024)
	assertNoRestart(t, src, "a chat's turn is open")

	h.coord.turns.finish(turn, marotte.TurnResult{})

	assertOneRestart(t, h, src)
}

func TestEngineRestart_NewTurnsWaitOnTheGateOnlyDuringTheRestart(t *testing.T) {
	h, _, src := restartFixture(t)
	stopped := make(chan struct{})
	release := make(chan struct{})
	src.mu.Lock()
	src.stopHook = func() { close(stopped); <-release }
	src.mu.Unlock()
	if !h.engine.requestRestart(restartCause{Kind: restartConfirmed}) {
		t.Fatal("the confirmed restart was refused")
	}
	<-stopped
	ctx, cancel := context.WithTimeout(t.Context(), quietWindow)
	defer cancel()
	if _, _, _, err := h.engine.acquire(ctx); err == nil {
		t.Error("a door was admitted while the engine restarts, want it to wait on the gate")
	}
	src.mu.Lock()
	src.stopHook = nil
	src.mu.Unlock()
	close(release)
	waitReady(t, h.engine, 2)
	if _, _, rel, err := h.engine.acquire(t.Context()); err != nil {
		t.Errorf("a door after the restart = %v, want it admitted at once", err)
	} else {
		rel()
	}
}

func TestEngineRestart_IdleChatsKeepRecordsAndLoadLazily(t *testing.T) {
	h, _, src := restartFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	recorded := string(first.SessionID())
	h.engine.requestRestart(restartCause{Kind: restartConfirmed})
	waitReady(t, h.engine, 2)
	if sb := h.bridge.mgr.get("c1"); sb != nil && sb.startedPastOpen() && sb.current().Open() {
		t.Error("the idle chat holds an open session on the new engine before anything asked for one")
	}
	again, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the restart: %v", err)
	}
	if so := fakeOf(again).sessionOpts; so == nil || so.SessionID != recorded {
		t.Errorf("the reopen's door = %+v, want a load of the recorded session %q", so, recorded)
	}
	if n := src.generations(); n != 2 {
		t.Errorf("generations = %d, want 2", n)
	}
}

func TestEngineRestart_ConfirmedSnapshotsFirstAndWritesNoCancel(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Fatal("Setup: the reservation was refused")
	}
	var cancels []string
	src := fakeSourceOf(t, h)
	src.mu.Lock()
	src.stopHook = func() { cancels = br.notified(marotte.MethodCancel) }
	src.mu.Unlock()

	h.engine.requestRestart(restartCause{Kind: restartConfirmed, Reasons: []marotte.EngineReason{reasonHeap}})
	waitReady(t, h.engine, 2)
	h.coord.ReleaseTurnReservation("c1")
	if len(cancels) != 0 {
		t.Errorf("session/cancel frames written before the reap = %v, want none", cancels)
	}
	if got := engineInterruptCause(&restartCause{Reasons: []marotte.EngineReason{reasonHeap}}); got != "The agent engine restarted to apply: Agent engine memory limit" {
		t.Errorf("restart cause = %q", got)
	}
}

func TestEngineRestart_ARunPausedOnAPersonIsNotResumed(t *testing.T) {
	h, _, _ := restartFixture(t)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_waiting"})
	if set := h.snapshotInterrupted(1, nil); slices.Contains(set.runs, "wf_waiting") {
		t.Errorf("snapshot = %v, want a paused run waiting on a person left out", set.runs)
	}
}

// countResumes answers every resume on br and flips its inspect reply to running, as KAS's run does once resumed.
func countResumes(t *testing.T, br *fakeBridge, id string) *atomic.Int32 {
	t.Helper()
	var resumes atomic.Int32
	running := inspectReply(t, id, marotte.RunStatusRunning, "")
	br.mu.Lock()
	defer br.mu.Unlock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowResume {
			return nil, nil, false
		}
		resumes.Add(1)
		br.setCallResult(methodKiroWorkflowInspect, running)
		return json.RawMessage(`{}`), nil, true
	}
	return &resumes
}

// awaitResumes waits up to 5 s for want resumes (none: the quiet window), then checks no more follow.
func awaitResumes(t *testing.T, resumes *atomic.Int32, want int32) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); resumes.Load() < want && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(quietWindow)
	if got := resumes.Load(); got != want {
		t.Errorf("_kiro/workflow/resume written %d times, want %d", got, want)
	}
}

func TestEngineRestart_ParentlessRunResumedOnceUnderTheHealBudget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spent int
		want  int32
	}{
		{name: "inside the budget", want: 1},
		{name: "the budget spent", spent: maxAutoHeals},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", stalePauseReason))
			resumes := countResumes(t, br, "wf_1")
			leaseRun(t, h, runlease.Lease{
				WorkflowID: "wf_1", ParentSessionID: "sess_host", Host: hostSec(), Deadline: time.Now().Add(time.Hour),
			})
			for range tc.spent {
				h.runs.claimHeal("wf_1")
			}

			fakeSourceOf(t, h).current().crash()
			waitReady(t, h.engine, 2)

			awaitResumes(t, resumes, tc.want)
		})
	}
}

// After a restart the chat's return (the rehydrate sweep) and the restart pause's own heal both reach the run; the
// re-read under the run's verb lock (controlSerialized) lets only the first write a resume.
func TestEngineRestart_ChatParentedRunGetsExactlyOneResume(t *testing.T) {
	prev := healBaseDelay
	healBaseDelay = time.Millisecond
	t.Cleanup(func() { healBaseDelay = prev })
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.grantLease(t.Context(), id, "publish", agentLaunch(), hostSec())
	h.runs.recordParentSession(t.Context(), id, parkSession)
	br.mu.Lock()
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{"workflowId": id, "status": "paused", "parentSessionId": parkSession})
	br.mu.Unlock()
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, id, stalePauseReason))
	resumes := countResumes(t, br, id)

	heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	heal(t.Context(), parkChat, runNotif(methodWFPaused, map[string]any{"workflowId": id, "pauseReason": stalePauseReason}))
	h.runs.resumeInterruptedRuns(t.Context(), parkChat)

	awaitResumes(t, resumes, 1)
}

// mutationWindow holds a driving verb's write unsettled, as a held invoke or answer reply does.
func mutationWindow(t *testing.T, h *Runtime) *runMutation {
	t.Helper()
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1"})
	_, gen := h.engine.current()
	return h.runs.mutations.enter("wf_1", mutationDrive, gen)
}

func assertWindowHeld(t *testing.T, h *Runtime) {
	t.Helper()
	if h.engine.quiescent() {
		t.Error("quiescent with a driving write unsettled")
	}
	_, gen := h.engine.current()
	if set := h.snapshotInterrupted(gen, nil); !slices.Contains(set.runs, "wf_1") {
		t.Errorf("snapshot = %v, want the run whose write is unsettled", set.runs)
	}
	if st := h.engine.status(); !slices.ContainsFunc(st.Busy.Runs, func(r marotte.EngineBusyRun) bool { return r.WorkflowID == "wf_1" }) {
		t.Errorf("busy runs = %+v, want the run whose write is unsettled", st.Busy.Runs)
	}
}

func TestEngineRestart_InvokeReadBeforeRunStartBlocksQuiescenceAndIsResumed(t *testing.T) {
	h, _, _ := restartFixture(t)
	m := mutationWindow(t, h)
	m.replied(&marotte.RPCResponse{}, nil, laneRef{session: newFakeBridge()}, 3)
	assertWindowHeld(t, h)
}

func TestEngineRestart_AnswerReadBeforeTurnStartBlocksQuiescenceAndIsResumed(t *testing.T) {
	h, _, _ := restartFixture(t)
	mutationWindow(t, h)
	assertWindowHeld(t, h)
}

func TestEngineCrash_MutationWindowsAreInTheSnapshot(t *testing.T) {
	h, _, src := restartFixture(t)
	mutationWindow(t, h)
	src.current().crash()
	waitReady(t, h.engine, 2)
	// The respawn's resume claims one heal for the snapshotted run, once.
	deadline := time.Now().Add(5 * time.Second)
	for healsOf(h, "wf_1") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run in the crash's mutation window was never resumed")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(quietWindow)
	if n := healsOf(h, "wf_1"); n != 1 {
		t.Fatalf("heals after one crash = %d, want exactly one resume", n)
	}
	for range maxAutoHeals - 1 {
		if _, ok := h.runs.claimHeal("wf_1"); !ok {
			t.Fatal("a heal inside the budget was refused")
		}
	}
	if _, ok := h.runs.claimHeal("wf_1"); ok {
		t.Error("a heal past the budget was granted: a run that dies in the same window would resume forever")
	}
}

func healsOf(h *Runtime, wf string) int {
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	return h.runs.bounds.heals[wf]
}

func TestEngineRestart_AnUnsettledPauseIsNotResumed(t *testing.T) {
	h, _, _ := restartFixture(t)
	executingLease(t, h, "wf_1")
	_, gen := h.engine.current()
	h.runs.mutations.enter("wf_1", mutationStop, gen)
	if set := h.snapshotInterrupted(gen, nil); slices.Contains(set.runs, "wf_1") {
		t.Errorf("snapshot = %v, want a run with an unsettled pause left out", set.runs)
	}
}

// The exit hook snapshots on the read loop before any late frame is consumed.
func TestEngineExit_SnapshotIsTakenBeforeLateFrames(t *testing.T) {
	h, _, _ := restartFixture(t)
	_, gen := h.engine.current()
	r := genRouter{r: h.router, gen: gen}
	r.Route(&bridge.FrameHead{Method: methodWFRunStart, WorkflowID: "wf_1"})
	h.engine.onExit(gen)
	r.Route(&bridge.FrameHead{Method: methodWFPaused, WorkflowID: "wf_1"})
	h.engine.mu.Lock()
	set := h.engine.pending[gen]
	h.engine.mu.Unlock()
	if !slices.Contains(set.runs, "wf_1") {
		t.Errorf("resume set = %v, want the run started before the exit", set.runs)
	}
}
