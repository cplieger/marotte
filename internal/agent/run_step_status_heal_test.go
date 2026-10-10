package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func healableRun(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: json.RawMessage(`{"workflowId":"wf_1","state":{` +
			`"status":"paused","pauseReason":"` + interruptedPauseReason + `",` +
			`"root":{"nodeId":"root","type":"sequence","status":"paused","children":[` +
			`{"nodeId":"review","type":"step","status":"paused","sessionId":"sess_step"}]}}}`),
	}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	return h, br
}

func TestResumeIfInterrupted_WaitsOutAStepStatusWrite(t *testing.T) {
	h, br := healableRun(t)
	held := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowUpdate: held}
	written := make(chan error, 1)
	go func() { written <- h.runs.setStepStatus(t.Context(), "wf_1", "review", runStepCompleted) }()
	waitForCall(t, br, methodKiroWorkflowUpdate)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	h.runs.resumeIfInterrupted(ctx, runChatID("wf_1"), "wf_1")
	if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		t.Error("the heal resumed the run between the step-status read and its write")
	}
	close(held)
	if err := <-written; err != nil {
		t.Errorf("SetStepStatus = %v, want nil", err)
	}
}

func TestSetStepStatus_WaitsOutAHealsResume(t *testing.T) {
	h, br := healableRun(t)
	held := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowResume: held}
	healed := make(chan struct{})
	go func() {
		defer close(healed)
		h.runs.resumeIfInterrupted(t.Context(), runChatID("wf_1"), "wf_1")
	}()
	waitForCall(t, br, methodKiroWorkflowResume)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := h.runs.setStepStatus(ctx, "wf_1", "review", runStepCompleted)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("SetStepStatus during a heal's resume = %v, want it to wait until its deadline", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowUpdate) {
		t.Error("the step-status write was sent while a resume was moving the run")
	}
	close(held)
	<-healed
}
