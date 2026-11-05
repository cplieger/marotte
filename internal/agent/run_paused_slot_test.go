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
	"github.com/cplieger/marotte/internal/schedule"
)

// leaseScheduledWithSlot leases workflowID as entry's fire whose next slot comes due slotIn from now.
func leaseScheduledWithSlot(t *testing.T, h *Runtime, br *fakeBridge, entry *schedule.Entry, workflowID string, slotIn time.Duration) {
	t.Helper()
	h.insertBridgeNow(runChatID(workflowID), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), workflowID, "nightly",
		scheduledLaunch(entry.Row(), time.Now().Add(slotIn)), hostSec())
}

// pauseRun delivers a parentless run's `_kiro/workflow/paused` frame through the bounds' handler, with the empty
// chat id every parentless lifecycle frame arrives with.
func pauseRun(t *testing.T, h *Runtime, workflowID, reason string) {
	t.Helper()
	park := h.runs.observePaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	park(t.Context(), "", pausedFrame(t, workflowID, reason))
}

// waitForEndReason polls until workflowID records why it was stopped, "" when nothing did within the window.
func waitForEndReason(h *Runtime, workflowID string, within time.Duration) string {
	stop := time.Now().Add(within)
	for time.Now().Before(stop) {
		if r := h.runs.endReason(workflowID); r != "" {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	return h.runs.endReason(workflowID)
}

// TestScheduledRun_APausedRunIsCancelledAtItsNextSlot pins that a pause does not disarm a scheduled run's slot:
// otherwise the paused run holds its recipe and every later slot is refused as an overlap, with no alert.
func TestScheduledRun_APausedRunIsCancelledAtItsNextSlot(t *testing.T) {
	logs := captureLogs(t)
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	leaseScheduledWithSlot(t, h, br, &entry, id, 150*time.Millisecond)
	h.runs.armDeadline(t.Context(), id)

	pauseRun(t, h, id, "Step requested user input via send_message.")
	if h.runs.bounded(id) {
		t.Error("the paused run still reads as executing; the engine-busy and step-cap readers would count it")
	}

	if got := waitForEndReason(h, id, 5*time.Second); got != runEndOverran {
		t.Fatalf("a run paused past its next slot recorded %q, want %q; calls were %v", got, runEndOverran, br.callLog())
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for the run paused past its slot: %v", br.callLog())
	}
	if out := logs.String(); !strings.Contains(out, logMsgRunOverran) {
		t.Errorf("the slot's cancel did not log %q, which the overran alert keys on: %s", logMsgRunOverran, out)
	}
	if row := awaitEndedRow(t, st); row.LastStatus != schedule.StatusFailed || row.LastReason != reasonOverran {
		t.Errorf("the row reads (%q, %q), want the overran failure", row.LastStatus, row.LastReason)
	}
}

// TestScheduledRun_AResumeTakesTheSlotWatchBack pins that a resume's arm owns the run's clock again: the floor
// gives it minRunBudget, so a slot inside that floor must not cancel it.
func TestScheduledRun_AResumeTakesTheSlotWatchBack(t *testing.T) {
	h, br, _, entry := scheduledHub(t)
	const id = "wf_1"
	leaseScheduledWithSlot(t, h, br, &entry, id, 100*time.Millisecond)
	h.runs.armDeadline(t.Context(), id)
	pauseRun(t, h, id, "Paused by user request")

	h.runs.armDeadline(t.Context(), id)

	if got := waitForEndReason(h, id, 300*time.Millisecond); got != "" {
		t.Errorf("a resumed run was stopped %q at a slot inside its floor; its own deadline is %v out",
			got, minRunBudget)
	}
}

// TestManualRun_APauseStillParksItsClock pins that the slot watch is a scheduled run's only: a manual run's slot
// is a yield while it executes, and a person owns its pause.
func TestManualRun_APauseStillParksItsClock(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), id, "publish",
		launchOrigin{origin: runlease.OriginManual, slotAt: time.Now().Add(50 * time.Millisecond)}, hostSec())
	h.runs.armDeadline(t.Context(), id)

	pauseRun(t, h, id, "Paused by user request")

	if got := waitForEndReason(h, id, 250*time.Millisecond); got != "" {
		t.Errorf("a paused manual run was stopped %q at its recipe's slot", got)
	}
}

// TestBootSweep_WatchesTheSlotOfAScheduledRunLeftPaused pins that a restart cannot drop the slot: a lease is read
// from disk parked and timerless, and a slot that came due while marotte was down cancels the run at once.
func TestBootSweep_WatchesTheSlotOfAScheduledRunLeftPaused(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	const question = "Step requested user input via send_message."
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{
		"workflowId": id, "name": "nightly", "workflowName": "nightly", "status": "paused",
	})
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, id, question)
	leaseScheduledWithSlot(t, h, br, &entry, id, -time.Minute)

	h.runs.BootSweep(t.Context())

	if got := waitForEndReason(h, id, 5*time.Second); got != runEndOverran {
		t.Fatalf("a scheduled run left paused past its slot across a restart recorded %q, want %q; calls were %v",
			got, runEndOverran, br.callLog())
	}
	if row := awaitEndedRow(t, st); row.LastStatus != schedule.StatusFailed || row.LastReason != reasonOverran {
		t.Errorf("the row reads (%q, %q), want the overran failure", row.LastStatus, row.LastReason)
	}
}

// refuseCancelOnce makes br refuse the first cancel and answer every later one from its script.
func refuseCancelOnce(br *fakeBridge) {
	var refused atomic.Bool
	br.mu.Lock()
	defer br.mu.Unlock()
	br.onCallFails = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
		if method == methodKiroWorkflowCancel && refused.CompareAndSwap(false, true) {
			return nil, nil, false, errors.New("owner is live")
		}
		return nil, nil, false, nil
	}
}

// TestScheduledRun_ASlotCancelRefusedOnceIsRetried pins that the slot watch's cancel goes through the refused-cancel
// ladder like a deadline's: the watch's one timer is spent, so without a retry the paused run holds its recipe.
func TestScheduledRun_ASlotCancelRefusedOnceIsRetried(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	setCancelRetryDelay(h.runs, time.Millisecond)
	refuseCancelOnce(br)
	leaseScheduledWithSlot(t, h, br, &entry, id, 100*time.Millisecond)
	h.runs.armDeadline(t.Context(), id)
	pauseRun(t, h, id, "Paused by user request")

	if got := waitForEndReason(h, id, 5*time.Second); got != runEndOverran {
		t.Fatalf("a paused run whose slot cancel was refused once recorded %q, want %q; calls were %v",
			got, runEndOverran, br.callLog())
	}
	if got := callsOf(br, methodKiroWorkflowCancel); got != 2 {
		t.Errorf("the cancel was sent %d times, want 2: the refused one and its retry", got)
	}
	if row := awaitEndedRow(t, st); row.LastStatus != schedule.StatusFailed || row.LastReason != reasonOverran {
		t.Errorf("the row reads (%q, %q), want the overran failure", row.LastStatus, row.LastReason)
	}
}
