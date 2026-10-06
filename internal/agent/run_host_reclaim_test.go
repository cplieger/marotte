package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// ownedElsewhereRefusal is the error KAS answers a load or mutation with while another
// live process's stamp names the run.
func ownedElsewhereRefusal() *marotte.RPCError {
	return &marotte.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data: json.RawMessage(`{"details":"Workflow 'wf_1' appears to be running in another process ` +
			`(owner pid 42, liveness verdict: live); refusing to load it here. ` +
			`Retry after that process releases it or its run goes stale."}`),
	}
}

// claimedCarrier refuses `method` while the run is stamped by another process.
type claimedCarrier struct {
	*fakeBridge
	held    func() bool
	method  string
	answers []bool
	mu      sync.Mutex
}

func (c *claimedCarrier) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if method != c.method {
		return c.fakeBridge.Call(ctx, method, params)
	}
	refused := c.held()
	c.mu.Lock()
	c.answers = append(c.answers, refused)
	c.mu.Unlock()
	if refused {
		// The real bridge's shape: the reply and an error wrapping its RPCError.
		rpcErr := ownedElsewhereRefusal()
		return &marotte.RPCResponse{Error: rpcErr}, fmt.Errorf("ACP error %d: %w", rpcErr.Code, rpcErr)
	}
	return c.fakeBridge.Call(ctx, method, params)
}

func (c *claimedCarrier) sent() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]bool(nil), c.answers...)
}

// restartSweptRun builds a run killed mid-step, its carrier live here and its stamp naming the utility
// session; the fake refuses until that session stops.
func restartSweptRun(t *testing.T, method string) (*Runtime, *fakeBridge, *claimedCarrier) {
	t.Helper()
	h, _, br := newTestHub()
	addressableStep(t, h, br, "wf_1", "review")
	if h.runs.utility().session.liveID() == "" {
		t.Fatal("Setup: the warming read did not start the utility session")
	}
	carrier := &claimedCarrier{fakeBridge: newFakeBridge(), method: method}
	carrier.held = func() bool { return h.runs.utility().session.liveID() != "" }
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: carrier, state: bridgeIdle})
	return h, br, carrier
}

// A run the utility session's startup sweep stamped is still driven: the verb stops that session and resends.
func TestRunVerbs_AStampTheUtilitySessionHoldsIsReleased(t *testing.T) {
	cases := []struct {
		verb   func(t *testing.T, h *Runtime, utility *fakeBridge) error
		name   string
		method string
	}{
		{
			name: "Resume", method: methodKiroWorkflowResume,
			verb: func(t *testing.T, h *Runtime, _ *fakeBridge) error { return h.runs.Resume(t.Context(), "wf_1") },
		},
		{
			name: "SetStepStatus", method: methodKiroWorkflowUpdate,
			verb: func(t *testing.T, h *Runtime, _ *fakeBridge) error {
				return h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted)
			},
		},
		{
			name: "the chat-open heal", method: methodKiroWorkflowResume,
			verb: func(t *testing.T, h *Runtime, utility *fakeBridge) error {
				utility.setCallResult(methodKiroWorkflowInspect,
					inspectReply(t, "wf_1", marotte.RunStatusPaused, stalePauseReason))
				h.runs.resumeIfInterrupted(t.Context(), runChatID("wf_1"), "wf_1")
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, utility, carrier := restartSweptRun(t, tc.method)

			if err := tc.verb(t, h, utility); err != nil {
				t.Fatalf("%s on a run the utility session holds = %v, want nil", tc.name, err)
			}
			if got := carrier.sent(); len(got) != 2 || !got[0] || got[1] {
				t.Errorf("%s calls (true = refused) = %v, want one refused and one that landed", tc.method, got)
			}
			if id := h.runs.utility().session.liveID(); id != "" {
				t.Errorf("utility session = %q after the reclaim, want it stopped", id)
			}
		})
	}
}

// The utility may already be stopped by a concurrent verb; this one still resends.
func TestResume_AUtilitySessionAnotherVerbStoppedStillResends(t *testing.T) {
	h, _, carrier := restartSweptRun(t, methodKiroWorkflowResume)
	h.runs.utility().session.Stop()
	refusedOnce := false
	carrier.held = func() bool {
		was := refusedOnce
		refusedOnce = true
		return !was
	}

	if err := h.runs.Resume(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Resume = %v, want nil once the stopped session's stamp clears", err)
	}
	if got, want := carrier.sent(), []bool{true, false}; !slices.Equal(got, want) {
		t.Errorf("resume calls (true = refused) = %v, want %v", got, want)
	}
}

// KAS reads the stamp as live until the stopped process leaves the table, so the verb is resent until it lands.
func TestResume_AStampThatOutlivesTheStopBrieflyIsReleased(t *testing.T) {
	h, _, carrier := restartSweptRun(t, methodKiroWorkflowResume)
	lingering := 2
	carrier.held = func() bool {
		if h.runs.utility().session.liveID() != "" {
			return true
		}
		lingering--
		return lingering >= 0
	}

	if err := h.runs.Resume(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Resume = %v, want nil once the stopped session's stamp clears", err)
	}
	if got, want := carrier.sent(), []bool{true, true, true, false}; !slices.Equal(got, want) {
		t.Errorf("resume calls (true = refused) = %v, want %v", got, want)
	}
}

// A stamp that never clears ends at the grace with KAS's refusal.
func TestResume_AStampThatNeverClearsEndsInTheRefusal(t *testing.T) {
	grace := reclaimGrace
	reclaimGrace = 3 * reclaimPoll
	t.Cleanup(func() { reclaimGrace = grace })
	h, _, carrier := restartSweptRun(t, methodKiroWorkflowResume)
	carrier.held = func() bool { return true }

	err := h.runs.Resume(t.Context(), "wf_1")

	if !errors.Is(err, workflow.ErrOwnedElsewhere) {
		t.Errorf("Resume = %v, want KAS's ownership refusal once the grace ended", err)
	}
	if got := carrier.sent(); len(got) < 3 || len(got) > 6 {
		t.Errorf("resume calls = %d, want the first, the one after the stop, and a bounded few more", len(got))
	}
}

// A chat-open heal losing KAS's claim to a concurrent Resume is not a failed heal.
func TestResumeIfInterrupted_LosingTheClaimToAnotherResumeIsNotAFailure(t *testing.T) {
	logs := captureLogs(t)
	h, _, utility := newTestHub()
	addressableStep(t, h, utility, "wf_1", "review")
	utility.setCallResult(methodKiroWorkflowInspect,
		inspectReply(t, "wf_1", marotte.RunStatusPaused, stalePauseReason))
	carrier := newFakeBridge()
	carrier.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowResume: {
		Code: -32603, Message: "Internal error",
		Data: json.RawMessage(`{"details":"Workflow 'wf_1' was just claimed by another process; ` +
			`refusing to load it here. Retry if that process does not end up driving it."}`),
	}}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: carrier, state: bridgeIdle})

	h.runs.resumeIfInterrupted(t.Context(), runChatID("wf_1"), "wf_1")

	if !slices.Contains(carrier.callLog(), methodKiroWorkflowResume) {
		t.Fatalf("the heal sent no resume, so its outcome is untested: %v", carrier.callLog())
	}
	if out := logs.String(); strings.Contains(out, `"msg":"rehydrate: resume failed"`) ||
		!strings.Contains(out, `"msg":"rehydrate: another resume of this run is in flight`) {
		t.Errorf("logs = %s, want the lost claim reported as another resume in flight, not a failure", out)
	}
}
