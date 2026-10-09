package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/translate"
)

// leased grants a manual lease: a lease-less run is unbounded, so a fixture forgetting it passes vacuously.
func leased(t *testing.T, h *Runs, workflowID string) {
	t.Helper()
	h.grantLease(t.Context(), workflowID, "publish", manualLaunch())
}

// undurableLeaseStore returns a store whose every write fails (its parent is a regular file: ENOTDIR at any uid); memory is untouched.
func undurableLeaseStore(t *testing.T) *runlease.Store {
	t.Helper()
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the unwritable store: %v", err)
	}
	st, _ := runlease.NewStore(notADir) // the error is diagnostic; the store is usable
	return st
}

// TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline covers the wiring only; runlease.NextDeadline owns the arithmetic.
func TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline(t *testing.T) {
	for name, tc := range map[string]struct {
		slotIn time.Duration
		// spent is the only input that can make the backstop the tightest.
		spent  time.Duration
		wantIn time.Duration
	}{
		"no slot: the idle window is the whole bound":  {0, 0, runIdleWindow},
		"a slot inside the window wins":                {10 * time.Minute, 0, 10 * time.Minute},
		"a slot beyond the window loses":               {24 * time.Hour, 0, runIdleWindow},
		"a slot inside the floor is floored up":        {30 * time.Second, 0, minRunBudget},
		"a slot already gone is floored, not honoured": {-time.Minute, 0, minRunBudget},
		// The remainder is above minRunBudget deliberately: below it the floor answers.
		"a nearly-spent backstop wins over the window": {0, runBackstop - 7*time.Minute, 7 * time.Minute},
		// Deliberately in the PAST: the floor may not lift a spent backstop.
		"a spent backstop is honoured, not floored": {0, runBackstop + time.Hour, -time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			h := &Runs{}
			const id = "wf_1"
			o := manualLaunch()
			if tc.slotIn != 0 {
				o = scheduledLaunch("sched-1", time.Now().Add(tc.slotIn))
			}
			h.grantLease(t.Context(), id, "publish", o)
			if tc.spent != 0 {
				h.bounds.executed = map[string]time.Duration{id: tc.spent}
			}
			// The spent backstop's timer fires at once; claiming first makes it refuse, so the stamped value is readable.
			if !h.claimTermination(id) {
				t.Fatal("the fresh run already held a termination claim")
			}

			before := time.Now()
			h.armDeadline(t.Context(), id)
			// A window rather than an equality: the arm reads its own clock.
			inWindow := func(what string) {
				t.Helper()
				l, ok := h.lease(id)
				if !ok || !l.Bounded() {
					t.Fatalf("%s: the run holds no deadline, so nothing bounds it", what)
				}
				if got := l.Deadline.Sub(before); got < tc.wantIn-time.Second || got > tc.wantIn+time.Second {
					t.Errorf("%s: deadline is %v out, want ~%v", what, got.Round(time.Second), tc.wantIn)
				}
			}
			inWindow("the arm")

			// A refill recomputes the granted bound and writes only past refillGranularity, hence the ageing.
			aged, _ := h.lease(id)
			if err := h.leaseStore().SetDeadline(t.Context(), id,
				aged.Deadline.Add(-refillGranularity-time.Second)); err != nil {
				t.Fatalf("age the stored deadline: %v", err)
			}
			h.refillDeadline(t.Context(), id)
			inWindow("after a refill")
		})
	}
}

// TestArmRunDeadline_ConcurrentArmsLeaveALiveTimerForTheStoredDeadline asserts the invariant that a bounded
// lease always has a timer: observed under one hold, then the survivor fired. Disk-backed to widen the window.
func TestArmRunDeadline_ConcurrentArmsLeaveALiveTimerForTheStoredDeadline(t *testing.T) {
	const rounds, arms = 6, 4
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
				// One observation of both halves, through the store: h.runs.lease would retake the mutex.
				h.runs.mu.Lock()
				_, hasTimer := h.runs.bounds.timers[id]
				l, held := st.Get(id)
				h.runs.mu.Unlock()
				if held && l.Bounded() && !hasTimer && len(torn) == 0 {
					torn <- l.Deadline
				}
			}
		})
		for range arms {
			wg.Go(func() {
				<-release
				h.runs.armDeadline(t.Context(), id)
			})
		}
		close(release)
		wg.Wait()
		close(halt)
		obs.Wait()

		if len(torn) > 0 {
			t.Fatalf("round %d: the lease was bounded for deadline %v while no timer existed, so "+
				"the check, the store and the timer install are not one transaction — two arms can "+
				"leave the lease carrying one deadline and the surviving timer armed for another",
				round, <-torn)
		}

		l, ok := h.runs.lease(id)
		if !ok || !l.Bounded() {
			t.Fatalf("round %d: no arm recorded a deadline", round)
		}
		h.runs.mu.Lock()
		timer := h.runs.bounds.timers[id]
		timers := len(h.runs.bounds.timers)
		h.runs.mu.Unlock()
		if timer == nil {
			t.Fatalf("round %d: the arms left no timer, so nothing can ever stop the run", round)
		}
		if timers != 1 {
			t.Fatalf("round %d: %d arms left %d timers, want 1", round, arms, timers)
		}

		// And the survivor is armed for what the lease holds.
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
		if got := h.runs.endReason(id); got != runEndStalled {
			t.Fatalf("round %d: the fired timer recorded %q, want %q", round, got, runEndStalled)
		}
	}
}

// TestArmRunDeadline_KeepsBoundingWhenOnlyDurabilityFails pins that SetDeadline reports only the persist, so
// refusing on its error leaves a timer-less bounded lease.
func TestArmRunDeadline_KeepsBoundingWhenOnlyDurabilityFails(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	h.runs.leases = undurableLeaseStore(t)

	h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
	if _, held := h.runs.lease(id); !held {
		t.Fatal("a persist failure lost the lease from memory as well")
	}

	h.runs.armDeadline(t.Context(), id)

	l, _ := h.runs.lease(id)
	if !l.Bounded() {
		t.Fatal("the run took no deadline at all")
	}
	h.runs.mu.Lock()
	timer := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()
	if timer == nil {
		t.Fatal("a run whose deadline could not be persisted got no timer, so it reads as " +
			"bounded and nothing can ever stop it")
	}
	// A live callback rather than a map entry: fire it and watch the run end.
	timer.Reset(time.Millisecond)
	stop := time.Now().Add(5 * time.Second)
	for h.runs.endReason(id) == "" {
		if time.Now().After(stop) {
			t.Fatal("the installed timer's callback could not act on the run")
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("recorded %q, want %q", got, runEndStalled)
	}
	// The log line is the whole compensation: a restart loses the clock.
	const wantLine = "a run's deadline is not durable, so it will not survive a restart; this process still bounds the run"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("a run whose deadline could not be persisted was bounded silently; want a "+
			"line reading %q. Got: %s", wantLine, out)
	}
	if out := logs.String(); !strings.Contains(out, `"workflow_id":"`+id+`"`) {
		t.Errorf("the durability line does not name the run it is about: %s", out)
	}
}

// TestDisarmRunDeadline_ParksInMemoryWhenTheParkCannotBePersisted is the disarm's half of that split.
func TestDisarmRunDeadline_ParksInMemoryWhenTheParkCannotBePersisted(t *testing.T) {
	logs := captureLogs(t)
	h := &Runs{leases: undurableLeaseStore(t)}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	if !h.disarmDeadline(t.Context(), id) {
		t.Fatal("the disarm reported holding no deadline, so the run stays bounded with no timer")
	}
	if l, _ := h.lease(id); l.Bounded() {
		t.Errorf("the parked lease still carries deadline %v", l.Deadline)
	}
	const wantLine = "could not park a run's deadline"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("a park that lost its durability said nothing; want a line reading %q. Got: %s",
			wantLine, out)
	}
}

// TestCancelExpiredRun_ReportsAScheduleRowItCouldNotWrite pins that a failed row write is logged (a schedule
// deleted while its run executes).
func TestCancelExpiredRun_ReportsAScheduleRowItCouldNotWrite(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// An EMPTY schedule store: the lease names a schedule that is no longer there.
	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	h.runs.schedules = st

	h.runs.grantLease(t.Context(), id, "nightly", scheduledLaunch("sched-gone", time.Now().Add(30*time.Second)))
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)

	h.runs.cancelExpired(id, l.Deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q; the run is cancelled whatever the row does", got, runEndOverran)
	}
	const wantLine = "could not record the schedule's outcome"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("the schedule row could not be written and nothing said so; want a line reading "+
			"%q. Got: %s", wantLine, out)
	}
	if out := logs.String(); !strings.Contains(out, `"schedule_id":"sched-gone"`) {
		t.Errorf("the failed-outcome line does not name the schedule it is about: %s", out)
	}
}

// TestArmRunDeadline_IsIdempotent pins that the earliest arm wins, or a run extends its own budget.
func TestArmRunDeadline_IsIdempotent(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	first, _ := h.lease(id)
	h.armDeadline(t.Context(), id)
	h.armDeadline(t.Context(), id)
	after, _ := h.lease(id)

	if !after.Deadline.Equal(first.Deadline) {
		t.Errorf("a second arm moved the deadline from %v to %v, so a run emitting frames "+
			"extends its own budget", first.Deadline, after.Deadline)
	}
	// One timer, whatever the arm count: a second live timer means a second callback.
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 1 {
		t.Errorf("three arms left %d timers, want 1", timers)
	}
}

// TestArmRunDeadline_RefusesARunWithNoLease pins that a TUI run has no cancel path marotte owns.
func TestArmRunDeadline_RefusesARunWithNoLease(t *testing.T) {
	h := &Runs{}
	h.armDeadline(t.Context(), "wf_tui")
	if h.bounded("wf_tui") {
		t.Error("a run with no lease was bounded")
	}
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 0 {
		t.Errorf("a leaseless run left %d timers behind", timers)
	}
}

// TestDisarmRunDeadline_ParksTheLeaseAndStopsTheTimer pins that a stale lease deadline makes the next re-arm skip the run forever.
func TestDisarmRunDeadline_ParksTheLeaseAndStopsTheTimer(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	h.mu.Lock()
	timer := h.bounds.timers[id]
	h.mu.Unlock()
	if timer == nil {
		t.Fatal("the arm installed no timer, so nothing can ever stop the run")
	}

	if !h.disarmDeadline(t.Context(), id) {
		t.Fatal("the disarm reported holding no deadline")
	}
	if l, _ := h.lease(id); l.Bounded() {
		t.Errorf("the parked lease still carries deadline %v", l.Deadline)
	}
	// Stop is false for a stopped timer, proving the first stop landed.
	if timer.Stop() {
		t.Error("the timer was still live after its run was parked")
	}
	if h.disarmDeadline(t.Context(), id) {
		t.Error("a parked run reported holding a deadline")
	}
	if h.disarmDeadline(t.Context(), "wf_never_armed") {
		t.Error("an unleased run reported holding a deadline")
	}
	if h.disarmDeadline(t.Context(), "") {
		t.Error("the empty workflow id reported holding a deadline")
	}
}

// TestRunDeadline_ResumeGetsAFreshBudgetRatherThanARemainder asserts a full idle window from the resume,
// which "later than the first deadline" cannot.
func TestRunDeadline_ResumeGetsAFreshBudgetRatherThanARemainder(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	first, _ := h.lease(id)

	if !h.disarmDeadline(t.Context(), id) { // the pause
		t.Fatal("the pause reported holding no deadline")
	}
	// A parked lease carries NO deadline, so there is nothing left to subtract from.
	if parked, _ := h.lease(id); parked.Bounded() {
		t.Fatalf("the parked lease still carries deadline %v, so a resume could compute a "+
			"remainder from it", parked.Deadline)
	}

	resumedAt := time.Now()
	h.armDeadline(t.Context(), id) // the resume

	second, _ := h.lease(id)
	if !second.Bounded() {
		t.Fatal("the resumed run took no deadline")
	}
	// Tight enough to exclude any leftover of the first arm's budget.
	if budget := second.Deadline.Sub(resumedAt); budget < runIdleWindow-time.Second || budget > runIdleWindow+time.Second {
		t.Errorf("the resumed run got %v of budget, want a full %v measured from the resume; "+
			"a resumed run must not inherit the remainder of the clock it parked with",
			budget.Round(time.Millisecond), runIdleWindow)
	}
	if !second.Deadline.After(first.Deadline) {
		t.Errorf("the resume kept deadline %v (first was %v)", second.Deadline, first.Deadline)
	}
}

// TestCancelExpiredRun_ASupersededTimerDoesNothing pins that a callback in flight across a pause and resume
// arrives with the old deadline.
func TestCancelExpiredRun_ASupersededTimerDoesNothing(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	stale, _ := h.lease(id)

	h.disarmDeadline(t.Context(), id) // the pause
	h.armDeadline(t.Context(), id)    // the resume
	live, _ := h.lease(id)
	if live.Deadline.Equal(stale.Deadline) {
		t.Fatal("the resume reused the same deadline, so the guard cannot distinguish them")
	}

	// The stale timer's callback, arriving now.
	if h.claimExpiredDeadline(id, stale.Deadline) {
		t.Fatal("a superseded timer claimed the resumed run; it would be cancelled after the " +
			"old deadline's remainder")
	}
	if !h.bounded(id) {
		t.Error("the resumed run lost its deadline to the superseded callback, so nothing bounds it")
	}

	// The CURRENT deadline is the one that may act, and only once.
	if !h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("the live timer's own callback was refused")
	}
	if h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("the deadline was claimed twice")
	}
	// A released lease: a pending timer must not resurrect a cancel.
	h.releaseLease(t.Context(), id)
	if h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("a released run was claimed by its pending timer")
	}
}

// TestRunDeadline_FiresAndCancelsAtTheDeadline drives the real timer; every other case calls the callback directly.
func TestRunDeadline_FiresAndCancelsAtTheDeadline(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, id)

	deadline := time.Now().Add(20 * time.Millisecond)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, deadline); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	// Staged by hand: NextDeadline floors at minRunBudget, too long to observe.
	h.runs.mu.Lock()
	h.runs.setTimerLocked(id, deadline)
	h.runs.mu.Unlock()

	// A deadline-bounded poll, which cannot flake into a false pass.
	stop := time.Now().Add(5 * time.Second)
	for h.runs.endReason(id) == "" {
		if time.Now().After(stop) {
			t.Fatalf("the deadline never fired: bounded=%v calls=%v", h.runs.bounded(id), br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("the expired run recorded %q, want %q", got, runEndStalled)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for the expired run: %v", br.callLog())
	}
}

// TestCancelExpiredRun_AFlooredSlotStillReportsAsTheScheduleBound pins that the floor can push the deadline past
// SlotAt, so equality would skip the schedule row.
func TestCancelExpiredRun_AFlooredSlotStillReportsAsTheScheduleBound(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://nightly", Enabled: true,
		Spec: schedule.Spec{Freq: schedule.FreqDaily, Hour: 2},
	}
	if pErr := st.Put(t.Context(), &entry); pErr != nil {
		t.Fatalf("Put schedule: %v", pErr)
	}
	h.runs.schedules = st

	// A slot INSIDE the floor, so the armed deadline lands later than SlotAt.
	h.runs.grantLease(t.Context(), id, "nightly", scheduledLaunch("sched-1", time.Now().Add(30*time.Second)))
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)
	if !l.Deadline.After(l.SlotAt) {
		t.Fatalf("the fixture did not produce a floor-adjusted deadline: slot %v, deadline %v",
			l.SlotAt, l.Deadline)
	}

	h.runs.cancelExpired(id, l.Deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q", got, runEndOverran)
	}
	if out := logs.String(); !strings.Contains(out, logMsgRunOverran) {
		t.Errorf("the callback did not log the schedule bound (%q); a floor-adjusted slot is "+
			"still the slot, and the ceiling message would page the operator about the wrong "+
			"thing. Got: %s", logMsgRunOverran, out)
	}
	if out := logs.String(); strings.Contains(out, logMsgRunStalled) {
		t.Errorf("the callback reported a stall for a run cancelled at its schedule "+
			"bound: %s", out)
	}
	// The row, which is the half a reader actually sees.
	rows := st.List()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows", len(rows))
	}
	if rows[0].LastStatus != schedule.StatusFailed || rows[0].LastReason != reasonOverran {
		t.Errorf("the schedule row reads (%q, %q), want the overran failure; without it the row still "+
			"says `started` while the schedule has silently stopped producing", rows[0].LastStatus, rows[0].LastReason)
	}
}

// TestCancelExpiredRun_AManualRunYieldingToASlotIsNotAScheduleFailure pins that an alert rule reads logMsgRunOverran
// as a schedule failing, so a manual run yielding must not log it.
func TestCancelExpiredRun_AManualRunYieldingToASlotIsNotAScheduleFailure(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// A manual lease carrying a slot, and no schedule id: no row asked for this run.
	h.runs.grantLease(t.Context(), id, "publish",
		launchOrigin{origin: runlease.OriginManual, slotAt: time.Now().Add(10 * time.Minute)})
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)

	h.runs.cancelExpired(id, l.Deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q; the run did run past its bound", got, runEndOverran)
	}
	out := logs.String()
	if !strings.Contains(out, logMsgRunYieldedToSlot) {
		t.Errorf("the manual run's cancellation was not reported as yielding to its slot: %s", out)
	}
	if strings.Contains(out, logMsgRunOverran) {
		t.Errorf("a manual run yielding to a slot logged the schedule-failure message, which a "+
			"deployment alert rule keys on: %s", out)
	}
	if strings.Contains(out, logMsgRunStalled) {
		t.Errorf("the stall message was logged for a run cancelled at its slot: %s", out)
	}
}

// TestClaimRunTermination_IsTakenOnce pins that user cancel, schedule, clock and turn cap race; one wins.
func TestClaimRunTermination_IsTakenOnce(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	if !h.claimTermination("wf_1") {
		t.Fatal("the first claim failed")
	}
	if h.claimTermination("wf_1") {
		t.Error("a second caller also took the claim; both would cancel and record")
	}
	// Independent per run: one run terminating must not stop another's cancel.
	if !h.claimTermination("wf_2") {
		t.Error("an unrelated run could not be terminated")
	}
	if h.claimTermination("") {
		t.Error("the empty workflow id took a claim")
	}
}

// TestClaimRunTermination_UserCancelBeatsALaterBound pins that a user cancel records nothing, so a later bound must not record over it.
func TestClaimRunTermination_UserCancelBeatsALaterBound(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	// The user's cancel, arriving first and recording nothing.
	if !h.claimTermination(id) {
		t.Fatal("the user's cancel could not claim the run")
	}
	h.recordEnd(id, "")

	// The deadline's stored value still matches, so only the claim can stop it writing.
	l, _ := h.lease(id)
	if h.claimExpiredDeadline(id, l.Deadline) {
		t.Fatal("the deadline claimed a run the user had already cancelled")
	}

	if got := h.endReason(id); got != "" {
		t.Errorf("the row reads %q for a user cancel; the absence IS the third value", got)
	}
}

// TestClaimRunTermination_OrphanSweepAndScheduleDeadlineCannotBothRecord pins that the first reason stands.
func TestClaimRunTermination_OrphanSweepAndScheduleDeadlineCannotBothRecord(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	const id = "wf_1"

	// The orphan sweep gets there first.
	if !h.claimTermination(id) {
		t.Fatal("the orphan sweep could not claim the run")
	}
	h.recordEnd(id, runEndOrphaned)

	// The schedule deadline, arriving on the same run.
	if h.claimTermination(id) {
		t.Fatal("the schedule deadline claimed a run the orphan sweep was already ending")
	}
	if got := h.endReason(id); got != runEndOrphaned {
		t.Errorf("endReason = %q, want the first reason %q", got, runEndOrphaned)
	}
}

// TestReleaseRunTermination_ReopensAFailedCancel pins that a held claim after a failed cancel mutes the Cancel button.
func TestReleaseRunTermination_ReopensAFailedCancel(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	if !h.claimTermination("wf_1") {
		t.Fatal("the first claim failed")
	}
	h.releaseTermination("wf_1")
	if !h.claimTermination("wf_1") {
		t.Error("the run stayed claimed after its cancel failed, so nothing can stop it")
	}
}

// TestForgetRunBounds_ClearsTheClaimOnATerminalRun pins that the claim map holds only runs terminating now.
func TestForgetRunBounds_ClearsTheClaimOnATerminalRun(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	h.claimTermination(id)

	h.forgetBounds(t.Context(), id)

	h.mu.Lock()
	claims := len(h.bounds.terminating)
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if claims != 0 {
		t.Errorf("the terminal frame left %d claims behind", claims)
	}
	if timers != 0 {
		t.Errorf("the terminal frame left %d timers behind", timers)
	}
	// And the lease.
	if _, held := h.lease(id); held {
		t.Error("the terminal frame left the lease behind, so the recipe still reads as busy")
	}
}

// refillingBus refills from inside the teardown through settleAsksForRun's broadcast, its slowest step.
type refillingBus struct {
	rs      *Runs
	id      string
	refills int
}

func (b *refillingBus) Broadcast(ctx context.Context, _ marotte.ServerEvent) {
	b.refills++
	b.rs.refillDeadline(ctx, b.id)
}

// TestForgetRunBounds_ARefillInsideTheTeardownLeavesNoTimer pins the order: lease first, so the timer
// clear is final (bounds.timers has no eviction).
func TestForgetRunBounds_ARefillInsideTheTeardownLeavesNoTimer(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	bus := &refillingBus{rs: h, id: id}
	h.bus = bus
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	// Near-expiry, so a mid-teardown refill really installs a timer.
	if err := h.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}
	// One unanswered ask, or the teardown broadcasts nothing.
	if !h.asks.Add(askOf("c1", id, "a1", "review")) {
		t.Fatal("the ask was not recorded, so nothing in the teardown broadcasts")
	}

	h.forgetBounds(t.Context(), id)

	if bus.refills == 0 {
		t.Fatal("the teardown broadcast nothing, so no refill was driven inside it")
	}
	h.mu.Lock()
	_, mine := h.bounds.timers[id]
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if mine || timers != 0 {
		t.Errorf("the teardown left %d timers behind (this run's: %v); a refill filed one after "+
			"the clear, and bounds.timers has no eviction — so that entry outlives the container",
			timers, mine)
	}
	if _, held := h.lease(id); held {
		t.Error("the teardown left the lease behind, so a later frame could bound the run again")
	}
}

// TestClearRunEnd_RestoresARetriedRunToUnbounded pins that retry reuses the workflow id, so the old reason and claim must go.
func TestClearRunEnd_RestoresARetriedRunToUnbounded(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	const id = "wf_1"

	h.claimTermination(id)
	h.recordEnd(id, runEndOverran)
	h.recordEnd("wf_other", runEndOrphaned)

	h.clearEnd(id)

	if got := h.endReason(id); got != "" {
		t.Errorf("the retried run still reads %q, so its row renders as aborted", got)
	}
	if !h.claimTermination(id) {
		t.Error("the retried run kept its termination claim, so no bound can ever stop it")
	}
	// The queue loses the entry too.
	h.mu.Lock()
	order := slices.Clone(h.bounds.order)
	h.mu.Unlock()
	if slices.Contains(order, id) {
		t.Errorf("the eviction queue still names the cleared run: %v", order)
	}
	// A neighbour is untouched.
	if got := h.endReason("wf_other"); got != runEndOrphaned {
		t.Errorf("clearing one run's reason changed another's to %q", got)
	}
}

// TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun pins that a user cancel holds a claim with no reason.
func TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	const id = "wf_1"

	h.claimTermination(id) // the user's cancel
	h.clearEnd(id)         // the retry

	if !h.claimTermination(id) {
		t.Error("a user-cancelled run stayed claimed through its retry, so nothing bounds it")
	}
}

// TestRearmRetriedRun_GivesAFreshClock pins that an aborted run without a terminal frame still carries its launch deadline.
func TestRearmRetriedRun_GivesAFreshClock(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	before, _ := h.lease(id)

	h.rearmRetried(t.Context(), id, "publish")

	after, held := h.lease(id)
	if !held || !after.Bounded() {
		t.Fatal("the retried run holds no deadline at all")
	}
	if after.Deadline.Equal(before.Deadline) {
		t.Error("the retry kept the previous deadline, so its clock is the old one's remainder")
	}
}

// TestRearmRetriedRun_MintsALeaseForARunWhoseTerminalFrameReleasedIt pins that the minted lease carries the recipe
// name the single-run rule needs.
func TestRearmRetriedRun_MintsALeaseForARunWhoseTerminalFrameReleasedIt(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"

	h.rearmRetried(t.Context(), id, "nightly")

	l, held := h.lease(id)
	if !held {
		t.Fatal("a retry after a terminal frame got no lease, so nothing bounds the re-driven run")
	}
	if !l.Bounded() {
		t.Error("the re-minted lease carries no deadline")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly; a nameless lease cannot be recognised by the "+
			"single-run rule as the run holding its own recipe", l.Recipe)
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual: a retried parentless run is the user's own and "+
			"must stay sweepable", l.Origin)
	}
	if !l.SlotAt.IsZero() {
		t.Errorf("SlotAt = %v; the run list reports a name, not a launch source, so no slot is "+
			"resolvable for a re-hosted run", l.SlotAt)
	}
	if l.Unattended {
		t.Error("a retried run was marked unattended; the user clicked Retry and can answer")
	}
}

// TestRunStartLaunch_ClassifiesByTheCarrier pins that a `run_start` up a chat's bridge is agent-launched; lease
// absence cannot decide, since a retry grants its lease late.
func TestRunStartLaunch_ClassifiesByTheCarrier(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		chatID     marotte.ChatID
		want       runlease.Origin
		wantChatID string
	}{
		"a run bridge's frame, dispatched with no chat id": {"", runlease.OriginManual, ""},
		"the synthetic run chat id":                        {runChatID("wf_1"), runlease.OriginManual, ""},
		"a real chat id, so the chat's agent asked":        {"c-abc123", runlease.OriginAgent, "c-abc123"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runStartLaunch(tc.chatID)
			if got.origin != tc.want {
				t.Errorf("runStartLaunch(%q).origin = %q, want %q", tc.chatID, got.origin, tc.want)
			}
			if got.chatID != tc.wantChatID {
				t.Errorf("runStartLaunch(%q).chatID = %q, want %q", tc.chatID, got.chatID, tc.wantChatID)
			}
		})
	}
}

// TestObserveRunStart_AParentlessFrameMintsASweepableLease pins that an unsweepable parentless run blocks every later launch of its recipe.
func TestObserveRunStart_AParentlessFrameMintsASweepableLease(t *testing.T) {
	h, _, _ := newTestHub()
	const id = "wf_retry"

	h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": id, "workflowName": "nightly",
	}))

	l, held := h.runs.lease(id)
	if !held {
		t.Fatal("a parentless run_start minted no lease")
	}
	if l.Origin == runlease.OriginAgent {
		t.Error("a parentless run was leased as agent-origin, which excludes it from the orphan " +
			"sweep for good: a restart would leave its paused row blocking the recipe forever")
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual", l.Origin)
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want the frame's own workflowName", l.Recipe)
	}
	if !l.Bounded() {
		t.Error("the minted lease was not armed")
	}

	// A chat's own agent run stays agent.
	h.runs.observeStart(t.Context(), "c-abc", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	if l, _ := h.runs.lease("wf_agent"); l.Origin != runlease.OriginAgent {
		t.Errorf("a chat-parented run was leased as %q, want agent", l.Origin)
	}
}

// TestRunEndReason_DistinguishesABoundFromAUserCancel pins that both land on `aborted`, so only the recorded reason tells them apart.
func TestRunEndReason_DistinguishesABoundFromAUserCancel(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	if got := h.endReason("wf_user_cancelled"); got != "" {
		t.Errorf("a run nothing recorded reported %q; a user cancel must read as empty", got)
	}
	h.recordEnd("wf_overran", runEndOverran)
	h.recordEnd("wf_orphan", runEndOrphaned)

	if got := h.endReason("wf_overran"); got != runEndOverran {
		t.Errorf("endReason(overran) = %q, want %q", got, runEndOverran)
	}
	if got := h.endReason("wf_orphan"); got != runEndOrphaned {
		t.Errorf("endReason(orphaned) = %q, want %q", got, runEndOrphaned)
	}
	// A shared map must not answer for a key it does not hold.
	if got := h.endReason("wf_user_cancelled"); got != "" {
		t.Errorf("the reason leaked to an unrecorded run: %q", got)
	}
}

// TestRecordRunEnd_IsBounded pins that the record outlives its run, so FIFO eviction is the only bound.
func TestRecordRunEnd_IsBounded(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	for i := range maxRunEndReasons + 10 {
		h.recordEnd("wf_"+strconv.Itoa(i), runEndOverran)
	}
	h.mu.Lock()
	got := len(h.bounds.reasons)
	order := len(h.bounds.order)
	h.mu.Unlock()

	// Exactly the cap: evicting early shrinks the history a row reads.
	if got != maxRunEndReasons {
		t.Errorf("kept %d reasons, want exactly the cap %d", got, maxRunEndReasons)
	}
	if order != got {
		t.Errorf("the eviction queue (%d) and the map (%d) disagree", order, got)
	}
	// Oldest first.
	if h.endReason("wf_0") != "" {
		t.Error("the oldest reason survived eviction")
	}
	if h.endReason("wf_"+strconv.Itoa(maxRunEndReasons+9)) != runEndOverran {
		t.Error("the newest reason was evicted")
	}
}

// TestRecordRunEnd_RewriteDoesNotDoubleQueue pins that a second record must not enqueue twice.
func TestRecordRunEnd_RewriteDoesNotDoubleQueue(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	h.recordEnd("wf_1", runEndOverran)
	h.recordEnd("wf_1", runEndOrphaned)

	h.mu.Lock()
	order := len(h.bounds.order)
	h.mu.Unlock()
	if order != 1 {
		t.Errorf("the eviction queue holds %d entries for one run, want 1", order)
	}
	if got := h.endReason("wf_1"); got != runEndOrphaned {
		t.Errorf("endReason = %q, want the latest reason %q", got, runEndOrphaned)
	}
	// An empty reason is not recorded.
	h.recordEnd("wf_2", "")
	if got := h.endReason("wf_2"); got != "" {
		t.Errorf("an empty reason was recorded as %q", got)
	}
}

// noopRunTranslator satisfies the one translate role observeStart reaches.
type noopRunTranslator struct{}

func (noopRunTranslator) HandleRunStart(context.Context, marotte.ChatID, *marotte.RPCResponse)    {}
func (noopRunTranslator) HandleRunComplete(context.Context, marotte.ChatID, *marotte.RPCResponse) {}
func (noopRunTranslator) RecordRunSteps(json.RawMessage)                                          {}
func (noopRunTranslator) ForgetRunSteps(string)                                                   {}
func (noopRunTranslator) SessionNotifyAsk(*marotte.RPCResponse) (marotte.RunInputNeededPayload, bool) {
	return marotte.RunInputNeededPayload{}, false
}

// recordingRunTranslator logs which runs had their step sessions forgotten, observed through the role
// (the registry is internal/translate's).
type recordingRunTranslator struct {
	noopRunTranslator
	forgotten []string
}

func (r *recordingRunTranslator) ForgetRunSteps(workflowID string) {
	r.forgotten = append(r.forgotten, workflowID)
}

// TestObserveComplete_ForgetsStepSessionsOnlyOnATerminalStatus pins that `paused` is the ordinary frame for a step
// parked on a question, and wiping the registry then leaves its next ask unattributed.
func TestObserveComplete_ForgetsStepSessionsOnlyOnATerminalStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status string
		forget bool
	}{
		{"completed", true},
		{"failed", true},
		{"aborted", true},
		{"cancelled", true},
		// The run is still resumable, so its step sessions must survive.
		{"paused", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			rec := &recordingRunTranslator{}
			rs := &Runs{translate: rec}

			rs.observeComplete(t.Context(), "c-1", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "status": tc.status,
			}))

			want := []string(nil)
			if tc.forget {
				want = []string{"wf_1"}
			}
			if !slices.Equal(rec.forgotten, want) {
				t.Errorf("forgotten = %v after status %q, want %v", rec.forgotten, tc.status, want)
			}
		})
	}
}

// TestObserveComplete_ClearsAStepsPendingDecisionOnlyWhenTheRunEnds pins that the tracker feeds the connect replay,
// so only the server can stop a dead run's ask being re-offered; `paused` keeps it.
func TestObserveComplete_ClearsAStepsPendingDecisionOnlyWhenTheRunEnds(t *testing.T) {
	for _, tc := range []struct {
		status   string
		survives bool
	}{
		{"completed", false},
		{"paused", true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, _, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			const runID = "wf_1"
			const launching marotte.ChatID = "c-parent"
			// Filed as translate files a step's question: under the launching chat, run stamped on the payload.
			h.bus.pendingPerms.Add(7, marotte.NewEvent(marotte.EventUserInputNeeded, launching,
				marotte.UserInputNeededPayload{RequestID: 7, RunID: runID, NodeID: "review"}))

			h.runs.observeComplete(t.Context(), launching, runNotif(methodWFRunComplete, map[string]any{
				"workflowId": runID, "status": tc.status,
			}))

			replayed := len(h.bus.pendingPerms.List("")) == 1
			if replayed != tc.survives {
				t.Errorf("the step's question replayable = %v after status %q, want %v",
					replayed, tc.status, tc.survives)
			}
		})
	}
}

// TestDecodeLifecycleFrame pins that an undecodable frame yields no workflow id rather than a run named "".
func TestDecodeLifecycleFrame(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params     string
		wantID     string
		wantStatus marotte.RunStatus
	}{
		"a run_start frame":        {`{"workflowId":"wf_1","workflowName":"x"}`, "wf_1", ""},
		"a terminal frame":         {`{"workflowId":"wf_1","status":"completed"}`, "wf_1", "completed"},
		"a frame with no id":       {`{"status":"completed"}`, "", "completed"},
		"malformed params":         {`{"workflowId":`, "", ""},
		"params that are not JSON": {`not json`, "", ""},
		"an array":                 {`["wf_1"]`, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := &marotte.RPCResponse{Params: json.RawMessage(tc.params)}
			got := decodeLifecycleFrame(msg)
			if got.WorkflowID != tc.wantID {
				t.Errorf("WorkflowID = %q, want %q", got.WorkflowID, tc.wantID)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
	// Both shapes are "no run".
	if got := workflowIDOfFrame(nil); got != "" {
		t.Errorf("workflowIDOfFrame(nil) = %q, want empty", got)
	}
	if got := workflowIDOfFrame(&marotte.RPCResponse{}); got != "" {
		t.Errorf("workflowIDOfFrame(empty) = %q, want empty", got)
	}
}

// TestRunBoundConstants_HoldTheirRelationships pins the constants' relationships, each derived from
// outside this file. A user-raisable backstop is none, so they stay constants.
func TestRunBoundConstants_HoldTheirRelationships(t *testing.T) {
	t.Parallel()

	// A window below the floor would never be what NextDeadline returns.
	if runIdleWindow <= minRunBudget {
		t.Errorf("runIdleWindow = %v, not above the floor %v; the floor would swallow it",
			runIdleWindow, minRunBudget)
	}
	// KAS's StreamIdleTimeoutError fires at 300s and stream_error_retry re-issues silently; the window must exceed it.
	if kasStreamIdle := 300 * time.Second; runIdleWindow <= kasStreamIdle {
		t.Errorf("runIdleWindow = %v, not longer than KAS's own stream idle timeout %v; a "+
			"stalled STREAM is KAS's to retry, and this window is about a stalled RUN",
			runIdleWindow, kasStreamIdle)
	}
	// The unattended floor must answer inside the tightest slot budget, or the run is cut before its row says why.
	if unattendedApprovalBudget >= minRunBudget {
		t.Errorf("unattendedApprovalBudget = %v, not below the floor %v; the tightest slot "+
			"cuts a scheduled run before the floor records the approval it needed",
			unattendedApprovalBudget, minRunBudget)
	}
	// The backstop must be the loosest bound.
	if runBackstop <= runIdleWindow {
		t.Errorf("runBackstop = %v, not above the idle window %v; it would fire first and no "+
			"run could ever stall", runBackstop, runIdleWindow)
	}
	// A granularity at or above the window means no refill ever lands.
	if refillGranularity >= runIdleWindow {
		t.Errorf("refillGranularity = %v, not below the idle window %v; no refill could ever "+
			"clear the throttle", refillGranularity, runIdleWindow)
	}
}

// TestObserveComplete_ClosesTheStepTurnOnlyOnATerminalStatus pins that the terminal frame is the only closer of a
// step whose node_complete never came; `paused` must not close it.
func TestObserveComplete_ClosesTheStepTurnOnlyOnATerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		status    string
		stillOpen bool
	}{
		{"completed", false},
		{"failed", false},
		// Closing here would take the turn from a run about to keep folding into it.
		{"paused", true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h := newBudgetRuntime(t)
			const launching marotte.ChatID = "c-parent"
			if _, _, err := h.runs.log.Open(t.Context(), translate.RunStep{RunID: "wf_1", NodePath: "seq/coder", SessionID: "sess-step"}, launching); err != nil {
				t.Fatalf("Open(step turn): %v", err)
			}

			h.runs.observeComplete(t.Context(), launching, runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "status": tc.status,
			}))

			if open := h.runs.log.Turn("wf_1", "seq/coder") != nil; open != tc.stillOpen {
				t.Errorf("the step turn is still open = %v after status %q, want %v",
					open, tc.status, tc.stillOpen)
			}
			if h.liveTurn(launching) != nil {
				t.Errorf("the launching chat %q holds a turn; a step opens a run turn only", launching)
			}
		})
	}
}

// TestObserveComplete_ClosesAParentlessRunsStepTurns pins the close keyed on the workflow: a parentless
// run's frames carry an empty chat id.
func TestObserveComplete_ClosesAParentlessRunsStepTurns(t *testing.T) {
	h := newBudgetRuntime(t)
	host := runChatID("wf_1")
	if _, _, err := h.runs.log.Open(t.Context(), translate.RunStep{RunID: "wf_1", NodePath: "seq/coder", SessionID: "sess-step"}, host); err != nil {
		t.Fatalf("Open(step turn): %v", err)
	}

	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))

	if h.runs.log.Turn("wf_1", "seq/coder") != nil {
		t.Error("a parentless run's terminal frame left its step turn open")
	}
	for _, chatID := range []marotte.ChatID{"", host} {
		if h.liveTurn(chatID) != nil {
			t.Errorf("a chat turn is keyed under %q; a step opens no chat turn", chatID)
		}
	}
}

// TestCancel_ReleasesTheLeaseOfAPausedRunThatSendsNoTerminalFrame pins that a node-boundary cancel of a run with
// no in-flight node sends no `run_complete`, so releaseIfOver must release. The cancel precedes the reconcile.
func TestCancel_ReleasesTheLeaseOfAPausedRunThatSendsNoTerminalFrame(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, held := h.runs.lease("wf_1"); held {
		t.Error("a landed cancel of a paused run left its lease behind, so the run stays " +
			"advertised as live and its chat stays exempt from eviction")
	}
	log := br.callLog()
	cancelAt := slices.Index(log, methodKiroWorkflowCancel)
	inspectAt := slices.Index(log, methodKiroWorkflowInspect)
	if cancelAt < 0 {
		t.Fatalf("no cancel went out: %v", log)
	}
	if inspectAt >= 0 && inspectAt < cancelAt {
		t.Errorf("the reconcile ran BEFORE the cancel: %v", log)
	}
}

// TestCancel_LeavesTheLeaseOfARunThatIsStillRunning pins that a running run's own terminal frame releases it.
func TestCancel_LeavesTheLeaseOfARunThatIsStillRunning(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// The boundary is not reached yet.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("the reconcile released the lease of a run KAS still reports as running")
	}
}

// TestCancel_ReconcilesNothingWhenTheCancelFAILED pins that a refused cancel issues no inspect and hands back the claim.
func TestCancel_ReconcilesNothingWhenTheCancelFAILED(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("bridge gone")}
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a cancel that failed")
	}

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("a FAILED cancel released the lease, stranding a live run with no clock " +
			"and nothing to explain the row it blocks")
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowInspect) {
		t.Errorf("a failed cancel still paid for a reconcile: %v", br.callLog())
	}
}
