package agent

// An agent-launched run KAS reconciles to paused on a read is parked, not stalled, and its chat's return resumes it.

import (
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

const (
	parkChat    marotte.ChatID = "c1"
	parkSession string         = "sess_owned"
)

// agentRunOnChat is an agent-launched run hosted by parkChat's session, one step open, its chat recorded
// with the launching session.
func agentRunOnChat(t *testing.T, id string, opts ...Option) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub(opts...)
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	br.sessionID = parkSession
	h.bridge.mgr.insert(parkChat, &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.useLog(newRunLog(t.TempDir()))
	h.coord.runs = h.runs.log
	if _, err := cs.Mutate(t.Context(), parkChat, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession(parkSession)
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	step := &translate.RunStep{RunID: id, NodePath: workflow.PathKey([]string{id, "review"}), SessionID: "step-session-a"}
	if _, _, err := h.runs.log.open(t.Context(), step, parkChat); err != nil {
		t.Fatalf("open the step: %v", err)
	}
	return h, br
}

// closeReviewStep completes agentRunOnChat's step, leaving the run with no open turn.
func closeReviewStep(t *testing.T, h *Runtime, id string) {
	t.Helper()
	if _, _, err := h.runs.log.closeNode(t.Context(), id, workflow.PathKey([]string{id, "review"}), "completed", ""); err != nil {
		t.Fatalf("close the step: %v", err)
	}
}

func agentLaunch() launchOrigin {
	return launchOrigin{origin: runlease.OriginAgent, chatID: string(parkChat)}
}

func wantParked(t *testing.T, h *Runtime, br *fakeBridge, id string) {
	t.Helper()
	if got := h.runs.endReason(id); got != "" {
		t.Errorf("endReason = %q, want none: a parked run is not stopped", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a parked run: %v", br.callLog())
	}
	l, held := h.runs.lease(id)
	if !held || l.Bounded() {
		t.Errorf("lease held = %t, bounded = %t; want the run kept and its deadline disarmed", held, l.Bounded())
	}
	h.runs.mu.Lock()
	_, timer := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()
	if timer {
		t.Error("a parked run kept a deadline timer")
	}
}

// A resume landing while the expiry reads KAS takes the run back: the park is refused and the window
// refilled, since the timer that fired was the run's only one.
func TestCancelExpired_AResumeDuringTheVerdictKeepsTheRunBounded(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	closeReviewStep(t, h, id)
	deadline := stagedExpiry(t, h.runs, id, agentLaunch())
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, nil, false
		}
		h.runs.armDeadline(t.Context(), id)
		return inspectPaused(t, id, stalePauseReason), nil, true
	}
	br.mu.Unlock()

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != "" {
		t.Errorf("endReason = %q, want none", got)
	}
	if l, _ := h.runs.lease(id); !l.Deadline.After(time.Now()) {
		t.Errorf("deadline = %v, want a refilled window ahead of now", l.Deadline)
	}
	h.runs.mu.Lock()
	_, timer := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()
	if !timer {
		t.Error("the resumed run has no deadline timer")
	}
}

// KAS paused the run where marotte never saw the frame: the expiry parks it rather than call it stalled.
func TestCancelExpired_ARunKASReportsPausedIsParked(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	closeReviewStep(t, h, id)
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, stalePauseReason)
	deadline := stagedExpiry(t, h.runs, id, agentLaunch())

	h.runs.cancelExpired(id, deadline)

	wantParked(t, h, br, id)
}

// A park must not disarm a deadline a resume re-stamped after this expiry was armed.
func TestCancelExpired_AParkLeavesADeadlineArmedSinceAlone(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	closeReviewStep(t, h, id)
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, stalePauseReason)
	stale := stagedExpiry(t, h.runs, id, agentLaunch())
	fresh := time.Now().Add(runIdleWindow)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, fresh); err != nil {
		t.Fatalf("re-stamp the deadline: %v", err)
	}

	h.runs.cancelExpired(id, stale)

	if l, _ := h.runs.lease(id); !l.Deadline.Equal(fresh) {
		t.Errorf("deadline = %v, want the re-stamped %v kept", l.Deadline, fresh)
	}
}

// A resumed run that then opens no step before its fresh window runs out is a stall, not still parked.
func TestCancelExpired_AResumedRunWithNoOpenStepIsAStall(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	h.runs.grantLease(t.Context(), id, "publish", agentLaunch(), hostSec())
	h.runs.armDeadline(t.Context(), id)
	h.coord.closeHostedRuns(t.Context(), parkChat, marotte.StopReasonInterrupted)
	br.mu.Lock()
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{"workflowId": id, "status": "paused", "parentSessionId": parkSession})
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, stalePauseReason)
	br.callResults[methodKiroWorkflowResume] = json.RawMessage(`{}`)
	br.mu.Unlock()
	h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	if l, _ := h.runs.lease(id); !l.Bounded() {
		t.Fatal("Setup: the resume did not re-arm the run")
	}
	br.mu.Lock()
	br.callResults[methodKiroWorkflowInspect] = inspectReply(t, id, marotte.RunStatusRunning, "")
	br.mu.Unlock()
	expired := time.Now().Add(-time.Second)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, expired); err != nil {
		t.Fatalf("stage an expired deadline: %v", err)
	}

	h.runs.cancelExpired(id, expired)

	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("endReason = %q, want %q: the resumed run is executing again", got, runEndStalled)
	}
}

// fastOrphanResume shortens the re-read delay; tests using it stay off t.Parallel.
func fastOrphanResume(t *testing.T) {
	t.Helper()
	prev := orphanResumeDelay
	orphanResumeDelay = time.Millisecond
	t.Cleanup(func() { orphanResumeDelay = prev })
}

// listCount answers the run list from a counter, returning statusAt(n) on the nth list.
func listCount(t *testing.T, br *fakeBridge, id string, statusAt func(n int32) string) {
	t.Helper()
	var lists atomic.Int32
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowList {
			return nil, nil, false
		}
		n := lists.Add(1)
		return kasRuns(t, map[string]any{"workflowId": id, "status": statusAt(n), "parentSessionId": parkSession}), nil, true
	}
	br.mu.Unlock()
}

// A chat back inside KAS's stale window finds its orphan still listed running; the sweep reads again.
func TestResumeInterruptedRuns_ReReadsAnOrphanKASStillListsRunning(t *testing.T) {
	fastOrphanResume(t)
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	defer shutdownHub(t, h)
	h.coord.closeHostedRuns(t.Context(), parkChat, marotte.StopReasonInterrupted)
	br.mu.Lock()
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, stalePauseReason)
	br.callResults[methodKiroWorkflowResume] = json.RawMessage(`{}`)
	br.mu.Unlock()
	listCount(t, br, id, func(n int32) string {
		if n == 1 {
			return "running"
		}
		return "paused"
	})

	h.runs.resumeInterruptedRuns(t.Context(), parkChat)

	stop := time.Now().Add(5 * time.Second)
	for !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		if time.Now().After(stop) {
			t.Fatalf("the orphan was never resumed once KAS reconciled it; calls were %v", br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}
