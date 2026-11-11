package agent

// The refill and the expiry classification. Fixtures stage the deadline by hand: NextDeadline floors at
// minRunBudget, too long to observe.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// TestRefillDeadline_ProgressPastADayAndAHalfKeepsAFullWindow pins that executing time alone never ends a run:
// only the idle window, the slot and a cancel do.
func TestRefillDeadline_ProgressPastADayAndAHalfKeepsAFullWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := boundsRuns(t)
		const id = "wf_1"
		const progressEvery = 10 * time.Minute
		leased(t, h, id)
		h.armDeadline(t.Context(), id)

		for ran := progressEvery; ran <= 37*time.Hour; ran += progressEvery {
			time.Sleep(progressEvery)
			h.refillDeadline(t.Context(), id)
			l, ok := h.lease(id)
			if !ok || !l.Bounded() {
				t.Fatalf("%v of progress in, the refill left the run unbounded", ran)
			}
			if budget := time.Until(l.Deadline); budget < runIdleWindow-refillGranularity {
				t.Fatalf("%v of progress in, a refill granted %v, want a full idle window %v",
					ran, budget, runIdleWindow)
			}
		}
		if got := h.endReason(id); got != "" {
			t.Errorf("endReason = %q, want none: the run is still making progress", got)
		}
	})
}

// TestRefillDeadline_RefusesAPausedRun pins that a refill re-arming a parked run would cancel one waiting on a person.
func TestRefillDeadline_RefusesAPausedRun(t *testing.T) {
	h := boundsRuns(t)
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	if !h.disarmDeadline(t.Context(), id) { // the pause
		t.Fatal("the pause reported holding no deadline")
	}

	h.refillDeadline(t.Context(), id)

	if l, _ := h.lease(id); l.Bounded() {
		t.Errorf("a refill re-armed a PARKED run with deadline %v, so a run held on a person's "+
			"answer would be cancelled for having been held", l.Deadline)
	}
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 0 {
		t.Errorf("a refill left %d timers on a parked run", timers)
	}
}

// TestRefillDeadline_RefusesARunWithNoLease pins that a TUI run has no lease or cancel path here.
func TestRefillDeadline_RefusesARunWithNoLease(t *testing.T) {
	h := boundsRuns(t)
	h.refillDeadline(t.Context(), "wf_tui")
	if h.bounded("wf_tui") {
		t.Error("a refill bounded a run with no lease")
	}
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 0 {
		t.Errorf("a leaseless run left %d timers behind", timers)
	}
	// The empty id a frame with no workflow block decodes to.
	h.refillDeadline(t.Context(), "")
}

// TestRefillDeadline_ThrottlesAWriteItWouldBarelyMove pins the granularity's disk saving, and that a
// skip keeps the same timer.
func TestRefillDeadline_ThrottlesAWriteItWouldBarelyMove(t *testing.T) {
	h := boundsRuns(t)
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	armed, _ := h.lease(id)
	h.mu.Lock()
	first := h.bounds.timers[id]
	h.mu.Unlock()
	if first == nil {
		t.Fatal("the arm installed no timer, so there is nothing for a refill to preserve")
	}

	// Each would move the deadline by microseconds.
	h.refillDeadline(t.Context(), id)
	h.refillDeadline(t.Context(), id)

	after, _ := h.lease(id)
	if !after.Deadline.Equal(armed.Deadline) {
		t.Errorf("a throttled refill moved the deadline from %v to %v, so every tool call costs "+
			"a whole-file fsynced rewrite of runs.json", armed.Deadline, after.Deadline)
	}
	h.mu.Lock()
	second := h.bounds.timers[id]
	h.mu.Unlock()
	if second != first {
		t.Error("a throttled refill swapped the timer, so the arm's own callback was stopped " +
			"and replaced for a deadline that did not move")
	}
}

// TestRefillDeadline_SwapsTheTimerForTheNewDeadline pins that the callback compares its captured deadline with
// the lease, so an unswapped timer always refuses. Driven by Reset, which keeps the captured deadline.
func TestRefillDeadline_SwapsTheTimerForTheNewDeadline(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeReady))
	leased(t, h.runs, id)

	h.runs.armDeadline(t.Context(), id)
	armed, _ := h.runs.lease(id)
	h.runs.mu.Lock()
	stale := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()

	// Near-expiry, so the refill clears the throttle.
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	h.runs.refillDeadline(t.Context(), id)

	live, _ := h.runs.lease(id)
	if !live.Deadline.After(armed.Deadline.Add(-time.Second)) {
		t.Fatalf("the refill did not land: deadline %v", live.Deadline)
	}
	h.runs.mu.Lock()
	timer := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()
	if timer == nil {
		t.Fatal("the refill left no timer, so nothing can stop the run")
	}
	if timer == stale {
		t.Fatal("the refill wrote the store and kept the old timer, whose captured deadline the " +
			"lease no longer holds — that callback can only ever refuse")
	}
	// Stop is false for an already-stopped timer, proving the replaced one was stopped.
	if stale.Stop() {
		t.Error("the replaced timer was left live, so a refilling run accumulates one pending " +
			"callback per progress frame")
	}

	timer.Reset(time.Millisecond)
	stop := time.Now().Add(5 * time.Second)
	for h.runs.endReason(id) == "" {
		if time.Now().After(stop) {
			t.Fatalf("the installed timer was armed for a deadline the lease no longer holds "+
				"(lease says %v), so the refilled run is bounded by a record nothing enforces",
				live.Deadline)
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("the fired timer recorded %q, want %q", got, runEndStalled)
	}
}

// TestCancelExpiredRun_AStallSaysWhichBoundCameDue pins the stall's log line, attribute, failure series and row reason.
func TestCancelExpiredRun_AStallSaysWhichBoundCameDue(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeReady))
	h.runs.grantLease(t.Context(), id, "publish", scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}), hostSec())
	h.runs.recordParentSession(t.Context(), id, testLaunchSession)
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)
	before := failureCounts(t)

	h.runs.cancelExpired(id, l.Deadline)

	wantFailureDeltas(t, before, map[string]float64{"scheduled/stalled": 1})
	out := logs.String()
	if !strings.Contains(out, logMsgRunStalled) {
		t.Errorf("the expiry did not log %q: %s", logMsgRunStalled, out)
	}
	// captureLogs renders JSON, so the attribute is a quoted key.
	if !strings.Contains(out, `"idle_window":`) {
		t.Errorf("the line carries no idle_window attribute, so it does not say which bound came due: %s", out)
	}
	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("endReason = %q, want %q", got, runEndStalled)
	}
}

// TestRefillDeadline_ConcurrentRefillsLeaveALiveTimerForTheStoredDeadline pins that concurrent stampers must leave
// the timer armed for the stored deadline. Observed under one hold, on a disk-backed store to widen the window.
func TestRefillDeadline_ConcurrentRefillsLeaveALiveTimerForTheStoredDeadline(t *testing.T) {
	const rounds, refills = 6, 4
	for round := range rounds {
		h, _, br := newTestHub()
		id := "wf_" + strconv.Itoa(round)
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
		h.insertBridgeNow(runChatID(id), testBridge(br, bridgeReady))
		st, err := runlease.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("round %d: NewStore: %v", round, err)
		}
		h.runs.leases = st
		h.runs.grantLease(t.Context(), id, "publish", manualLaunch(), hostSec())
		h.runs.armDeadline(t.Context(), id)
		// Near-expiry, so a refill genuinely lands.
		if sErr := st.SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); sErr != nil {
			t.Fatalf("round %d: stage a near-expiry deadline: %v", round, sErr)
		}

		release, halt := make(chan struct{}), make(chan struct{})
		torn := make(chan time.Time, 1)
		var wg, obs sync.WaitGroup
		obs.Go(func() {
			for {
				select {
				case <-halt:
					return
				default:
				}
				// One observation of both halves, read through the store: h.runs.lease would retake the mutex.
				h.runs.mu.Lock()
				_, hasTimer := h.runs.bounds.timers[id]
				l, held := st.Get(id)
				h.runs.mu.Unlock()
				if held && l.Bounded() && !hasTimer && len(torn) == 0 {
					torn <- l.Deadline
				}
			}
		})
		for range refills {
			wg.Go(func() {
				<-release
				h.runs.refillDeadline(t.Context(), id)
			})
		}
		close(release)
		wg.Wait()
		close(halt)
		obs.Wait()

		if len(torn) > 0 {
			t.Fatalf("round %d: the lease was bounded for deadline %v while no timer existed, so "+
				"the eligibility check, the store and the timer install are not one transaction — "+
				"two refills can leave the lease carrying one deadline and the surviving timer "+
				"armed for another", round, <-torn)
		}

		l, ok := h.runs.lease(id)
		if !ok || !l.Bounded() {
			t.Fatalf("round %d: the refills left the run unbounded", round)
		}
		h.runs.mu.Lock()
		timer := h.runs.bounds.timers[id]
		timers := len(h.runs.bounds.timers)
		h.runs.mu.Unlock()
		if timer == nil {
			t.Fatalf("round %d: the refills left no timer, so nothing can stop the run", round)
		}
		if timers != 1 {
			t.Fatalf("round %d: %d refills left %d timers, want 1", round, refills, timers)
		}

		// The survivor is armed for what the lease holds.
		timer.Reset(time.Millisecond)
		stop := time.Now().Add(2 * time.Second)
		for h.runs.endReason(id) == "" {
			if time.Now().After(stop) {
				t.Fatalf("round %d: the surviving timer was armed for a deadline the lease no "+
					"longer holds (lease says %v), so the run reads as bounded with no callback "+
					"that can act on it", round, l.Deadline)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// TestHealProgress_ACompletedNodeRefillsTheIdleWindow pins that one wrapper covers both run populations, and a
// completed node refills all three budgets.
func TestHealProgress_ACompletedNodeRefillsTheIdleWindow(t *testing.T) {
	h, _, _ := newTestHub()
	const id = "wf_1"
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	// Near-expiry, so the refill is visible past the throttle.
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	var forwarded bool
	progress := h.runs.healProgress(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
		forwarded = true
	})
	progress(t.Context(), "c1", pausedFrame(t, id, ""))

	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("a completed node left %v of budget, want a full idle window %v; a run "+
			"finishing nodes must not be cancelled as stalled", budget.Round(time.Second), runIdleWindow)
	}
	if !forwarded {
		t.Error("the wrapper swallowed the frame instead of passing it to the translator")
	}
}

// TestRunMadeProgress_IsTheDoorTranslateUses pins the exported surface; called per tool-call frame,
// so a run marotte is not bounding must cost one map read.
func TestRunMadeProgress_IsTheDoorTranslateUses(t *testing.T) {
	h, _, _ := newTestHub()
	const id = "wf_1"
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	h.runs.RunMadeProgress(id)

	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("the progress door left %v of budget, want a full idle window %v",
			budget.Round(time.Second), runIdleWindow)
	}

	// Not bounded here: a TUI run, or one whose lease is gone.
	h.runs.RunMadeProgress("wf_never_leased")
	if h.runs.bounded("wf_never_leased") {
		t.Error("the progress door bounded a run with no lease")
	}
}
