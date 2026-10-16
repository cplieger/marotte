package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// mutationFixture is a registry whose run clock answers covered and executing per run, and whose ceiling inspect
// answers inspectErr; settles counts every settle edge.
type mutationFixture struct {
	m          *runMutations
	covered    map[string]bool
	executing  map[string]bool
	inspectErr error
	inspects   atomic.Int32
	settles    atomic.Int32
	mu         sync.Mutex
}

func newMutationFixture(ceiling time.Duration) *mutationFixture {
	f := &mutationFixture{covered: map[string]bool{}, executing: map[string]bool{}}
	m := newRunMutations()
	m.ceiling, m.retryMax = ceiling, 4*ceiling
	m.covered = func(wf string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.covered[wf] }
	m.executing = func(wf string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.executing[wf] }
	m.inspect = func(context.Context, string) error {
		f.inspects.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.inspectErr
	}
	m.onSettle(func() { f.settles.Add(1) })
	f.m = m
	return f
}

func (f *mutationFixture) set(field map[string]bool, wf string, v bool) {
	f.mu.Lock()
	field[wf] = v
	f.mu.Unlock()
}

// waitEmpty waits for the registry to drain, failing after d.
func waitEmpty(t *testing.T, m *runMutations, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !m.empty() {
		if time.Now().After(deadline) {
			t.Fatal("the mutation never settled")
		}
		time.Sleep(time.Millisecond)
	}
}

var (
	ownerLane = laneRef{session: newFakeBridge()}
	otherLane = laneRef{session: newFakeBridge()}
)

func TestRunMutations_UnwrittenAndRefusedSettleAtOnce(t *testing.T) {
	f := newMutationFixture(time.Hour)
	f.m.enter("wf_1", mutationDrive, 1).unwritten()
	if !f.m.empty() {
		t.Error("an unwritten verb left an entry")
	}
	f.m.enter("wf_1", mutationDrive, 1).replied(&marotte.RPCResponse{Error: &marotte.RPCError{Code: -32603}}, nil, ownerLane, 4)
	if !f.m.empty() {
		t.Error("an in-band refusal left an entry: it changed nothing")
	}
	if got := f.settles.Load(); got != 2 {
		t.Errorf("settle edges = %d, want 2", got)
	}
}

func TestRunMutations_ARunStartBeforeTheReplySettlesAtTheReplysDrain(t *testing.T) {
	f := newMutationFixture(time.Hour)
	e := f.m.enter("wf_1", mutationDrive, 1)
	// run_start was consumed at position 3, and the run clock now covers the run, before the reply lands at 5.
	f.m.observeFrame(ownerLane, 3, "wf_1")
	f.set(f.covered, "wf_1", true)
	e.replied(&marotte.RPCResponse{}, nil, ownerLane, 5)
	if f.m.empty() {
		t.Fatal("the entry settled before the reply's position was consumed")
	}
	f.m.observeFrame(ownerLane, 5, "")
	if !f.m.empty() {
		t.Error("the entry did not settle once the reply's position was consumed with the run covered")
	}
}

func TestRunMutations_ALaterLifecycleFrameSettles(t *testing.T) {
	f := newMutationFixture(time.Hour)
	f.m.enter("wf_1", mutationDrive, 1).replied(&marotte.RPCResponse{}, nil, ownerLane, 5)
	f.m.observeFrame(ownerLane, 5, "")
	if f.m.empty() {
		t.Fatal("the drained reply settled with the run not covered")
	}
	f.m.observeFrame(ownerLane, 6, "wf_1")
	if !f.m.empty() {
		t.Error("a lifecycle frame after the reply did not settle the entry")
	}
}

// The reply and the run's frames share the owner's lane: a lifecycle frame at or before the reply's position predates
// the write, and a frame on another lane is not ordered against it at all.
func TestRunMutations_AStaleOwnerLaneFrameDoesNotSettle(t *testing.T) {
	f := newMutationFixture(time.Hour)
	f.m.enter("wf_1", mutationDrive, 1).replied(&marotte.RPCResponse{}, nil, ownerLane, 5)
	f.m.observeFrame(ownerLane, 4, "wf_1")
	f.m.observeFrame(otherLane, 9, "wf_1")
	if f.m.empty() {
		t.Fatal("a lifecycle frame that predates the write, or rode another lane, settled the entry")
	}
	f.m.observeFrame(ownerLane, 6, "wf_1")
	if !f.m.empty() {
		t.Error("the owner lane's next lifecycle frame did not settle the entry")
	}
}

func TestRunMutations_TheCeilingInspectSettles(t *testing.T) {
	f := newMutationFixture(5 * time.Millisecond)
	f.m.enter("wf_1", mutationDrive, 1).replied(nil, errors.New("reply lost"), ownerLane, 0)
	waitEmpty(t, f.m, 2*time.Second)
	if got := f.inspects.Load(); got != 1 {
		t.Errorf("inspects = %d, want 1: the ceiling decides an entry no frame settled", got)
	}
}

func TestRunMutations_AFailedInspectKeepsTheEntryAndRetries(t *testing.T) {
	f := newMutationFixture(5 * time.Millisecond)
	f.mu.Lock()
	f.inspectErr = errors.New("engine busy")
	f.mu.Unlock()
	f.m.enter("wf_1", mutationDrive, 1).replied(nil, errors.New("reply lost"), ownerLane, 0)
	deadline := time.Now().Add(2 * time.Second)
	for f.inspects.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("inspects = %d, want a retry after the failed one", f.inspects.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if f.m.empty() {
		t.Fatal("a failed inspect settled the entry")
	}
	f.mu.Lock()
	f.inspectErr = nil
	f.mu.Unlock()
	waitEmpty(t, f.m, 2*time.Second)
}

func TestRunMutations_DropGenerationMakesLateRepliesNoOps(t *testing.T) {
	f := newMutationFixture(time.Hour)
	e := f.m.enter("wf_1", mutationDrive, 1)
	keep := f.m.enter("wf_2", mutationDrive, 2)
	f.m.dropGeneration(1)
	if drive, _ := f.m.snapshot(1); len(drive) != 0 {
		t.Errorf("generation 1 snapshot after its drop = %v, want empty", drive)
	}
	e.replied(&marotte.RPCResponse{}, nil, ownerLane, 3)
	if drive, _ := f.m.snapshot(2); len(drive) != 1 || drive[0] != "wf_2" {
		t.Errorf("generation 2 snapshot = %v, want [wf_2]: a drop clears only its generation", drive)
	}
	keep.unwritten()
	if !f.m.empty() {
		t.Error("a late reply on a dropped generation re-entered the registry")
	}
}

// Every verb that drives or stops a run is in the registry while KAS holds its reply.
func TestRunMutations_EveryDrivingVerbEnters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		verb   func(t *testing.T, h *Runtime) error
	}{
		{name: "resume", method: methodKiroWorkflowResume, verb: func(t *testing.T, h *Runtime) error {
			host, err := h.runs.ensureResident(t.Context(), "wf_1")
			if err != nil {
				return err
			}
			_, err = host.call(t.Context(), h.runs, "wf_1", methodKiroWorkflowResume, map[string]any{keyWorkflowID: "wf_1"})
			return err
		}},
		{name: "retry", method: methodKiroWorkflowRetry, verb: func(t *testing.T, h *Runtime) error {
			return driveOnHost(t, h, methodKiroWorkflowRetry)
		}},
		{name: "step status, Continue and repeat", method: methodKiroWorkflowUpdate, verb: func(t *testing.T, h *Runtime) error {
			return driveOnHost(t, h, methodKiroWorkflowUpdate)
		}},
		{name: "answer and message", method: marotte.MethodPrompt, verb: func(t *testing.T, h *Runtime) error {
			return driveOnHost(t, h, marotte.MethodPrompt)
		}},
		{name: "pause", method: methodKiroWorkflowPause, verb: func(t *testing.T, h *Runtime) error {
			err := h.runs.stopVerb(t.Context(), "wf_1", methodKiroWorkflowPause, map[string]any{keyWorkflowID: "wf_1"})
			return err
		}},
		{name: "cancel", method: methodKiroWorkflowCancel, verb: func(t *testing.T, h *Runtime) error {
			err := h.runs.stopVerb(t.Context(), "wf_1", methodKiroWorkflowCancel, map[string]any{keyWorkflowID: "wf_1"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := seedChatParentedRun(t, true)
			gate := make(chan struct{})
			br.mu.Lock()
			if br.blockOn == nil {
				br.blockOn = map[string]chan struct{}{}
			}
			br.blockOn[tc.method] = gate
			br.mu.Unlock()
			done := make(chan error, 1)
			go func() { done <- tc.verb(t, h) }()
			waitForCall(t, br, tc.method)
			if h.runs.mutations.empty() {
				t.Errorf("%s: no mutation entry while KAS holds the reply", tc.name)
			}
			close(gate)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}

// A verb written on a host session settles from that session's own forward: the lifecycle frame its lane reads
// past the reply decides it, with no wait for the ceiling's inspect.
func TestRunMutations_AHostSessionVerbSettlesFromItsLanesFrames(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	if err := driveOnHost(t, h, methodKiroWorkflowRetry); err != nil {
		t.Fatalf("Setup: retry on the host session: %v", err)
	}
	if h.runs.mutations.empty() {
		t.Fatal("Setup: the retry settled before any frame was read, so the lane cannot be what settles it")
	}

	br.deliver(runNotif(methodWFNodeStart, map[string]any{
		keyWorkflowID: "wf_1", "nodeId": "review", "nodePath": []string{"wf_1", "review"}, "type": stepNodeType,
	}))

	deadline := time.Now().Add(5 * time.Second)
	for !h.runs.mutations.empty() {
		if time.Now().After(deadline) {
			t.Fatal("the retry is still unsettled after its host session read a later frame for the run")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// driveOnHost writes method on the run's resident launching session, as each driving verb does.
func driveOnHost(t *testing.T, h *Runtime, method string) error {
	host, err := h.runs.ensureResident(t.Context(), "wf_1")
	if err != nil {
		return err
	}
	_, err = host.call(t.Context(), h.runs, "wf_1", method, map[string]any{keyWorkflowID: "wf_1"})
	return err
}
