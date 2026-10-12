package agent

// An agent-launched run whose owning process died is parked, not stalled: KAS reconciles it to paused
// on a read and emits no frame, and its chat's return resumes it. A parentless one is still stopped.

import (
	"context"
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

// agentRunOnChat is an agent-launched run hosted by parkChat's bridge, one step open, its chat recorded
// with the launching session.
func agentRunOnChat(t *testing.T, id string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
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
	step := &translate.RunStep{RunID: id, NodePath: workflow.PathKey([]string{id, "review"}), NodeID: "review", SessionID: "step-session-a"}
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

// The incident's shape: the chat's kiro-cli exits on its own mid-step, KAS still lists the run running
// (its owner's beat has not lapsed), and the window had already run out when the loss was recorded.
func TestCancelExpired_ARunWhoseHostEndedIsParkedNotStopped(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	br.callResults[methodKiroWorkflowInspect] = inspectReply(t, id, marotte.RunStatusRunning, "")
	deadline := stagedExpiry(t, h.runs, id, agentLaunch())
	// The expiry fires between the loss being recorded and its park.
	h.runs.log.hostEnded = nil
	h.coord.closeHostedRuns(t.Context(), parkChat, br, marotte.StopReasonInterrupted)

	h.runs.cancelExpired(id, deadline)

	wantParked(t, h, br, id)
}

// A host's end parks its chat's run at once, so a backstop shorter than the idle window cannot stop it
// while it waits for the chat.
func TestCloseHostedRuns_ParksAnAgentRunAtItsHostsEnd(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	br.callResults[methodKiroWorkflowInspect] = inspectReply(t, id, marotte.RunStatusRunning, "")
	h.runs.grantLease(t.Context(), id, "publish", agentLaunch())
	h.runs.mu.Lock()
	h.runs.bounds.executed = map[string]time.Duration{id: runBackstop - time.Minute}
	h.runs.mu.Unlock()
	h.runs.armDeadline(t.Context(), id)
	armed, _ := h.runs.lease(id)

	h.coord.closeHostedRuns(t.Context(), parkChat, br, marotte.StopReasonInterrupted)
	h.runs.mu.Lock()
	h.runs.bounds.executed[id] = runBackstop
	h.runs.mu.Unlock()
	h.runs.cancelExpired(id, armed.Deadline)

	wantParked(t, h, br, id)
}

// replaceCarrier models forwardAt's order: the dead bridge leaves the manager before its closer runs,
// and a new generation for the chat is registered in that gap.
func replaceCarrier(t *testing.T, h *Runtime, dead *fakeBridge) *fakeBridge {
	t.Helper()
	if !h.bridge.mgr.removeIfBridge(parkChat, dead) {
		t.Fatal("Setup: the dead bridge was not the chat's")
	}
	next := newFakeBridge()
	h.bridge.mgr.insert(parkChat, &sharedBridge{bridge: next, state: bridgeIdle})
	return next
}

// Each removal that runs ahead of its death closer marks the removed host's runs lost before a
// replacement can register, so the replacement's resume sweep cannot read them as live.
func TestBridgeRemoval_MarksItsRunsHostLostAheadOfTheCloser(t *testing.T) {
	for name, remove := range map[string]func(t *testing.T, h *Runtime, br *fakeBridge){
		"it ends itself": func(t *testing.T, h *Runtime, br *fakeBridge) {
			if !h.bridge.mgr.removeIfBridge(parkChat, br) {
				t.Fatal("Setup: the bridge was not the chat's")
			}
		},
		"an identity change retires it": func(t *testing.T, h *Runtime, _ *fakeBridge) {
			if closed, _ := h.bridge.mgr.retireChatBridges(); len(closed) != 1 {
				t.Fatalf("Setup: retired %d bridges, want 1", len(closed))
			}
		},
		"its next open closes it retired": func(t *testing.T, h *Runtime, _ *fakeBridge) {
			sb := h.bridge.mgr.get(parkChat)
			sb.mu.Lock()
			sb.retire = true
			sb.mu.Unlock()
			if _, stopped := h.bridge.mgr.closeIfRetired(parkChat, sb); !stopped {
				t.Fatal("Setup: the retired bridge was not closed")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			const id = "wf_1"
			h, br := agentRunOnChat(t, id)
			closeReviewStep(t, h, id)
			h.runs.grantLease(t.Context(), id, "publish", agentLaunch())
			h.runs.armDeadline(t.Context(), id)

			remove(t, h, br)

			if !h.runs.hostLost(id) {
				t.Error("the removed host's run reads live until its closer runs")
			}
		})
	}
}

// Every death path names the process that ended, so the park takes the run that process armed: a bridge
// that exits on its own (forwardAt), one marotte closes (closeBridge) and one an identity change retires.
func TestBridgeDeath_ParksTheAgentRunItCarried(t *testing.T) {
	for name, kill := range map[string]func(t *testing.T, h *Runtime, br *fakeBridge){
		"it ends itself": func(t *testing.T, h *Runtime, br *fakeBridge) {
			h.coord.goForward(parkChat, br)
			br.endStream()
			stop := time.Now().Add(5 * time.Second)
			for h.runs.bounded("wf_1") {
				if time.Now().After(stop) {
					t.Fatal("the death closer never parked the run")
				}
				time.Sleep(time.Millisecond)
			}
		},
		"marotte closes it": func(t *testing.T, h *Runtime, _ *fakeBridge) {
			h.coord.closeBridge(t.Context(), parkChat, marotte.TurnOutcomeInterrupted)
		},
		"an identity change retires it between steps": func(t *testing.T, h *Runtime, _ *fakeBridge) {
			closeReviewStep(t, h, "wf_1")
			h.coord.retireBridges("test")
		},
	} {
		t.Run(name, func(t *testing.T) {
			const id = "wf_1"
			h, br := agentRunOnChat(t, id)
			h.runs.grantLease(t.Context(), id, "publish", agentLaunch())
			h.runs.armDeadline(t.Context(), id)

			kill(t, h, br)

			wantParked(t, h, br, id)
		})
	}
}

// A replacement generation can resume the run before the old host's closer runs, or between its mark
// and its park; that park must leave the resumed run's clock alone.
func TestCloseHostedRuns_AnOldHostsParkLeavesAReplacementsResumeArmed(t *testing.T) {
	for name, closeAround := range map[string]func(t *testing.T, h *Runtime, dead *fakeBridge, resume func()){
		"the resume lands before the closer": func(t *testing.T, h *Runtime, dead *fakeBridge, resume func()) {
			resume()
			h.coord.closeHostedRuns(t.Context(), parkChat, dead, marotte.StopReasonInterrupted)
		},
		"the resume lands between the mark and the park": func(t *testing.T, h *Runtime, dead *fakeBridge, resume func()) {
			park := h.runs.log.hostEnded
			h.runs.log.hostEnded = func(ctx context.Context, d ACPBridge, ids []string) {
				resume()
				park(ctx, d, ids)
			}
			h.coord.closeHostedRuns(t.Context(), parkChat, dead, marotte.StopReasonInterrupted)
		},
	} {
		t.Run(name, func(t *testing.T) {
			const id = "wf_1"
			h, br := agentRunOnChat(t, id)
			h.runs.grantLease(t.Context(), id, "publish", agentLaunch())
			h.runs.armDeadline(t.Context(), id)
			replaceCarrier(t, h, br)

			// The resume's arm, as resumeIfInterrupted makes it once the replacement took the verb.
			closeAround(t, h, br, func() { h.runs.armDeadline(t.Context(), id) })

			if l, _ := h.runs.lease(id); !l.Bounded() {
				t.Error("the old host's park disarmed the run its replacement resumed")
			}
			h.runs.mu.Lock()
			_, timer := h.runs.bounds.timers[id]
			h.runs.mu.Unlock()
			if !timer {
				t.Error("the resumed run has no deadline timer")
			}
			if h.runs.hostLost(id) {
				t.Error("the old host's end reads as the resumed run's host loss")
			}
		})
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

// Nothing resumes a parentless run whose own carrier died, so its window still stops it, paused in
// KAS or not.
func TestCancelExpired_AParentlessRunWhoseHostEndedIsAStall(t *testing.T) {
	for name, status := range map[string]marotte.RunStatus{
		"KAS lists it running":     marotte.RunStatusRunning,
		"KAS reconciled it paused": marotte.RunStatusPaused,
	} {
		t.Run(name, func(t *testing.T) {
			const id = "wf_1"
			h, br := stepFixture(t, id)
			h.runs.useLog(h.runs.log)
			h.coord.runs = h.runs.log
			br.callResults[methodKiroWorkflowInspect] = inspectReply(t, id, status, stalePauseReason)
			openStep(t, h, id, []string{"root", "a"}, "step-session-a")
			deadline := stagedExpiry(t, h.runs, id, manualLaunch())
			h.coord.closeHostedRuns(t.Context(), runChatID(id), br, marotte.StopReasonInterrupted)

			h.runs.cancelExpired(id, deadline)

			if got := h.runs.endReason(id); got != runEndStalled {
				t.Errorf("endReason = %q, want %q", got, runEndStalled)
			}
			if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("no cancel went out for a parentless orphan; calls were %v", br.callLog())
			}
		})
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

// The carrier's return resumes the parked run and gives it a fresh clock.
func TestResumeInterruptedRuns_ResumesAParkedRunWhenItsCarrierReturns(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	br.callResults[methodKiroWorkflowInspect] = inspectReply(t, id, marotte.RunStatusRunning, "")
	deadline := stagedExpiry(t, h.runs, id, agentLaunch())
	h.coord.closeHostedRuns(t.Context(), parkChat, br, marotte.StopReasonInterrupted)
	h.runs.cancelExpired(id, deadline)
	if l, _ := h.runs.lease(id); l.Bounded() {
		t.Fatal("Setup: the run was not parked")
	}
	br.mu.Lock()
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{"workflowId": id, "status": "paused", "parentSessionId": parkSession})
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, stalePauseReason)
	br.callResults[methodKiroWorkflowResume] = json.RawMessage(`{}`)
	br.mu.Unlock()

	h.runs.resumeInterruptedRuns(t.Context(), parkChat)

	if !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		t.Errorf("the returned carrier did not resume its parked run; calls were %v", br.callLog())
	}
	if l, _ := h.runs.lease(id); !l.Bounded() {
		t.Error("the resumed run has no deadline")
	}
}

// A resumed run that then opens no step before its fresh window runs out is a stall, not still parked.
func TestCancelExpired_AResumedRunWithNoOpenStepIsAStall(t *testing.T) {
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	h.runs.grantLease(t.Context(), id, "publish", agentLaunch())
	h.runs.armDeadline(t.Context(), id)
	h.coord.closeHostedRuns(t.Context(), parkChat, br, marotte.StopReasonInterrupted)
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
		t.Errorf("endReason = %q, want %q: the resume ended the host loss", got, runEndStalled)
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
func listCount(t *testing.T, br *fakeBridge, id string, statusAt func(n int32) string) *atomic.Int32 {
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
	return &lists
}

// A carrier back inside KAS's stale window finds its orphan still listed running; the sweep reads again.
func TestResumeInterruptedRuns_ReReadsAnOrphanKASStillListsRunning(t *testing.T) {
	fastOrphanResume(t)
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	defer shutdownHub(t, h)
	h.coord.closeHostedRuns(t.Context(), parkChat, br, marotte.StopReasonInterrupted)
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

// forwardAt's order with a fast replacement: the dead bridge leaves the manager, the replacement's
// resume sweep finds the orphan still listed running, and only then does the old closer park it. The
// re-read still resumes it on the replacement once KAS reconciles it paused.
func TestResumeInterruptedRuns_AReplacementSweepAheadOfTheCloserStillResumes(t *testing.T) {
	fastOrphanResume(t)
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	defer shutdownHub(t, h)
	h.runs.grantLease(t.Context(), id, "publish", agentLaunch())
	h.runs.armDeadline(t.Context(), id)
	br.mu.Lock()
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, stalePauseReason)
	br.mu.Unlock()
	parked := make(chan struct{})
	ctx := t.Context()
	listCount(t, br, id, func(n int32) string {
		if n == 1 {
			return "running"
		}
		select {
		case <-parked:
		case <-ctx.Done():
		}
		return "paused"
	})
	next := replaceCarrier(t, h, br)
	next.mu.Lock()
	next.callResults = map[string]json.RawMessage{methodKiroWorkflowResume: json.RawMessage(`{}`)}
	next.mu.Unlock()

	h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	h.coord.closeHostedRuns(t.Context(), parkChat, br, marotte.StopReasonInterrupted)
	if l, _ := h.runs.lease(id); l.Bounded() {
		t.Fatal("Setup: the old closer did not park the run")
	}
	close(parked)

	stop := time.Now().Add(5 * time.Second)
	for !h.runs.bounded(id) {
		if time.Now().After(stop) {
			t.Fatalf("the parked run was never resumed on its replacement; its calls were %v", next.callLog())
		}
		time.Sleep(time.Millisecond)
	}
	if !slices.Contains(next.callLog(), methodKiroWorkflowResume) {
		t.Errorf("the resume did not go to the replacement; its calls were %v", next.callLog())
	}
}

// A running run whose host lives is executing: no second sweep is scheduled for it.
func TestResumeInterruptedRuns_ALiveHostsRunningRunGetsNoReRead(t *testing.T) {
	fastOrphanResume(t)
	const id = "wf_1"
	h, br := agentRunOnChat(t, id)
	defer shutdownHub(t, h)
	lists := listCount(t, br, id, func(int32) string { return "running" })

	h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	// Many times the re-read delay, so a scheduled sweep would have listed again.
	time.Sleep(50 * time.Millisecond)

	if got := lists.Load(); got != 1 {
		t.Errorf("run list read %d times, want 1: a live host's running run is not an orphan", got)
	}
}
