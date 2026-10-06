package agent

// The refill, the backstop and the expiry classification. Fixtures stage deadline and stretch by
// hand: NextDeadline floors at minRunBudget, too long to observe.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// stagedStretch gives a run a lease, a deadline and an open stretch from stretchStart, bypassing
// armDeadline, which opens the stretch at its own now.
func stagedStretch(t *testing.T, rs *Runs, workflowID string, deadline, stretchStart time.Time) {
	t.Helper()
	leased(t, rs, workflowID)
	if err := rs.leaseStore().SetDeadline(t.Context(), workflowID, deadline); err != nil {
		t.Fatalf("stage the deadline: %v", err)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.armedAt == nil {
		rs.bounds.armedAt = map[string]time.Time{}
	}
	rs.bounds.armedAt[workflowID] = stretchStart
}

// TestRefillDeadline_RollsTheWindowForwardSoALongRunSurvives pins that a run 90 minutes deep gets a full
// window from progress, past where a start-anchored ceiling would fire.
func TestRefillDeadline_RollsTheWindowForwardSoALongRunSurvives(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	stretchStart := time.Now().Add(-90 * time.Minute)
	// Two minutes from expiring.
	stagedStretch(t, h, id, time.Now().Add(2*time.Minute), stretchStart)

	progressAt := time.Now()
	h.refillDeadline(t.Context(), id)

	l, ok := h.lease(id)
	if !ok || !l.Bounded() {
		t.Fatal("the refill left the run unbounded")
	}
	if budget := l.Deadline.Sub(progressAt); budget < runIdleWindow-time.Second || budget > runIdleWindow+time.Second {
		t.Errorf("the refilled deadline is %v out, want a full idle window %v measured from the "+
			"progress", budget.Round(time.Millisecond), runIdleWindow)
	}
	// A run this deep must not be bounded by when it started.
	if retired := stretchStart.Add(time.Hour); !l.Deadline.After(retired) {
		t.Errorf("the refilled deadline %v is not past %v, one hour into the run's own executing "+
			"stretch — a run still making progress must not be cancelled for being long",
			l.Deadline, retired)
	}
}

// TestRefillDeadline_RefusesAPausedRun pins that a refill re-arming a parked run would cancel one waiting on a person.
func TestRefillDeadline_RefusesAPausedRun(t *testing.T) {
	h := &Runs{}
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
	h := &Runs{}
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
	h := &Runs{}
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
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, id)

	h.runs.armDeadline(t.Context(), id)
	armed, _ := h.runs.lease(id)
	h.runs.mu.Lock()
	stale := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()

	// Near-expiry, so the refill clears the throttle; the stretch stays open.
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

// TestRefillDeadline_CannotMoveTheDeadlinePastTheBackstop pins that the first refill clamps at the backstop and
// every later one recomputes that instant. Staged 30 minutes deep so a now-anchored version differs.
func TestRefillDeadline_CannotMoveTheDeadlinePastTheBackstop(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	// The backstop comes due in 7 minutes: inside the window, above minRunBudget.
	const deep, left = 30 * time.Minute, 37 * time.Minute
	stretchStart := time.Now().Add(-deep)
	stagedStretch(t, h, id, time.Now().Add(2*time.Minute), stretchStart)
	h.mu.Lock()
	h.bounds.executed = map[string]time.Duration{id: runBackstop - left}
	h.mu.Unlock()

	h.refillDeadline(t.Context(), id)
	clamped, _ := h.lease(id)
	if budget := clamped.Deadline.Sub(stretchStart); budget > left+time.Second {
		t.Fatalf("the refill granted %v against %v of backstop left; a run that keeps making "+
			"progress must not outlive its absolute budget", budget.Round(time.Second), left)
	}
	if budget := clamped.Deadline.Sub(stretchStart); budget < left-time.Second {
		t.Fatalf("the refill granted only %v of the %v of backstop left",
			budget.Round(time.Second), left)
	}

	// Every refill recomputes the same instant.
	for range 5 {
		h.refillDeadline(t.Context(), id)
		after, _ := h.lease(id)
		if !after.Deadline.Equal(clamped.Deadline) {
			t.Fatalf("a later refill moved the clamped deadline from %v to %v, so a runaway loop "+
				"could refresh its way past the backstop forever", clamped.Deadline, after.Deadline)
		}
	}
}

// TestRunBackstop_MeasuresExecutingTimeNotWallTime pins that anchored on executing stretches, not
// Lease.StartedAt, which would cancel a run parked on a person.
func TestRunBackstop_MeasuresExecutingTimeNotWallTime(t *testing.T) {
	t.Run("stretches accumulate across pauses", func(t *testing.T) {
		h := &Runs{}
		const id = "wf_1"
		leased(t, h, id)

		// Two unequal paused stretches together exceeding the backstop, derived from runBackstop so the
		// fixture stays spent when the constant moves.
		firstStretch := runBackstop / 3
		secondStretch := runBackstop - firstStretch + time.Hour
		wantBanked := firstStretch + secondStretch
		for _, spent := range []time.Duration{firstStretch, secondStretch} {
			h.armDeadline(t.Context(), id)
			h.mu.Lock()
			h.bounds.armedAt[id] = time.Now().Add(-spent)
			h.mu.Unlock()
			if !h.disarmDeadline(t.Context(), id) {
				t.Fatalf("the pause after a %v stretch reported holding no deadline", spent)
			}
		}

		h.mu.Lock()
		banked := h.bounds.executed[id]
		open := len(h.bounds.armedAt)
		h.mu.Unlock()
		if banked < wantBanked || banked > wantBanked+time.Minute {
			t.Errorf("the run banked %v of executing time across two stretches, want ~%v",
				banked.Round(time.Second), wantBanked)
		}
		if open != 0 {
			t.Errorf("%d stretches left open after the park; a stretch banked twice would "+
				"double-charge the backstop", open)
		}

		// The next arm's deadline is already past; claim first so the callback refuses.
		if !h.claimTermination(id) {
			t.Fatal("the parked run already held a termination claim")
		}
		before := time.Now()
		h.armDeadline(t.Context(), id)
		armed, _ := h.lease(id)
		if !armed.Deadline.Before(before) {
			t.Errorf("a run that has executed %v resumed with %v of budget, want a deadline "+
				"already past — its backstop is spent, so the run is over",
				banked.Round(time.Hour), armed.Deadline.Sub(before).Round(time.Second))
		}

		// Later progress cannot lift it: each lap clears the throttle and recomputes the spent instant.
		for lap := range 3 {
			stored, _ := h.lease(id)
			aged := stored.Deadline.Add(-refillGranularity - time.Second)
			if err := h.leaseStore().SetDeadline(t.Context(), id, aged); err != nil {
				t.Fatalf("lap %d: age the stored deadline: %v", lap, err)
			}
			h.refillDeadline(t.Context(), id)
			after, _ := h.lease(id)
			if !after.Deadline.Equal(armed.Deadline) {
				t.Fatalf("lap %d: a refill moved the deadline of a run whose backstop is spent, "+
					"from %v to %v", lap, armed.Deadline, after.Deadline)
			}
		}
	})

	t.Run("a run ending clears its accounting", func(t *testing.T) {
		h := &Runs{}
		const id = "wf_1"
		leased(t, h, id)
		h.armDeadline(t.Context(), id)
		h.mu.Lock()
		h.bounds.executed = map[string]time.Duration{id: runBackstop}
		h.mu.Unlock()

		h.forgetBounds(t.Context(), id)

		h.mu.Lock()
		banked, open := h.bounds.executed[id], len(h.bounds.armedAt)
		h.mu.Unlock()
		if banked != 0 || open != 0 {
			t.Errorf("a terminal run left %v banked and %d stretches open; a workflow id KAS "+
				"reuses would inherit a spent backstop and be cancelled minutes after it started",
				banked, open)
		}

		// A reused id gets a full window, not the floor.
		leased(t, h, id)
		before := time.Now()
		h.armDeadline(t.Context(), id)
		l, _ := h.lease(id)
		if budget := l.Deadline.Sub(before); budget < runIdleWindow-time.Second {
			t.Errorf("a reused workflow id got %v of budget, want a full idle window %v",
				budget.Round(time.Second), runIdleWindow)
		}
	})

	t.Run("wall time spent parked burns none of it", func(t *testing.T) {
		h := &Runs{}
		const id = "wf_1"
		// Granted longer ago than the backstop, barely executed. Derived: a shorter literal passes both implementations.
		staleBy := runBackstop + time.Hour
		l := runlease.Lease{
			StartedAt:  time.Now().Add(-staleBy),
			WorkflowID: id,
			Recipe:     "publish",
			Origin:     runlease.OriginManual,
		}
		if err := h.leaseStore().Put(t.Context(), &l); err != nil {
			t.Fatalf("stage the old lease: %v", err)
		}

		before := time.Now()
		h.armDeadline(t.Context(), id)

		armed, ok := h.lease(id)
		if !ok || !armed.Bounded() {
			t.Fatal("the resumed run took no deadline")
		}
		if budget := armed.Deadline.Sub(before); budget < runIdleWindow-time.Second {
			t.Errorf("a run parked for %v resumed with %v of budget, want a full idle "+
				"window %v; anchoring the backstop on StartedAt would cancel it minutes after "+
				"the person answered", staleBy, budget.Round(time.Second), runIdleWindow)
		}
	})
}

// TestCancelExpiredRun_TellsAStallFromASpentBackstop pins that two messages for opposite failures; logMsgRunStalled is the one worth an alert rule.
func TestCancelExpiredRun_TellsAStallFromASpentBackstop(t *testing.T) {
	// The backstop case carries the rest of its budget in an open stretch, where its deadline fires.
	const openStretch = 10 * time.Minute
	for name, tc := range map[string]struct {
		banked     time.Duration
		stretchAge time.Duration
		want       string
		unwant     string
		wantKey    string
		wantReason string
	}{
		"a run that stopped producing is a stall": {
			banked: 0, stretchAge: 0,
			want: logMsgRunStalled, unwant: logMsgRunBackstop, wantKey: "idle_window", wantReason: runEndStalled,
		},
		"a run that spent its whole budget is not": {
			banked: runBackstop - openStretch, stretchAge: openStretch,
			want: logMsgRunBackstop, unwant: logMsgRunStalled, wantKey: "backstop", wantReason: runEndOverran,
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			h, _, br := newTestHub()
			const id = "wf_1"
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
			h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
			leased(t, h.runs, id)
			h.runs.armDeadline(t.Context(), id)
			if tc.banked != 0 || tc.stretchAge != 0 {
				h.runs.mu.Lock()
				h.runs.bounds.executed = map[string]time.Duration{id: tc.banked}
				h.runs.bounds.armedAt[id] = time.Now().Add(-tc.stretchAge)
				h.runs.mu.Unlock()
			}
			l, _ := h.runs.lease(id)

			h.runs.cancelExpired(id, l.Deadline)

			out := logs.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("the expiry did not log %q: %s", tc.want, out)
			}
			if strings.Contains(out, tc.unwant) {
				t.Errorf("the expiry also logged %q, which describes the opposite failure: %s",
					tc.unwant, out)
			}
			// captureLogs renders JSON, so the attribute is a quoted key.
			if attr := strconv.Quote(tc.wantKey) + ":"; !strings.Contains(out, attr) {
				t.Errorf("the line carries no %s attribute, so it does not say which bound came "+
					"due or what its size was: %s", attr, out)
			}
			if got := h.runs.endReason(id); got != tc.wantReason {
				t.Errorf("endReason = %q, want %q: the row is what tells a stall from a spent budget", got, tc.wantReason)
			}
		})
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
		h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
		st, err := runlease.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("round %d: NewStore: %v", round, err)
		}
		h.runs.leases = st
		h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
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
