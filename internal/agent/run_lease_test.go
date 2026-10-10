package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// launchableRecipe is the reply set a launch needs: one recipe, an empty run list, ids for new and invoke.
func launchableRecipe(br *fakeBridge, workflowID string) {
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(
			`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`,
		),
		methodKiroWorkflowList:   json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:    json.RawMessage(`{"workflowId":"` + workflowID + `"}`),
		methodKiroWorkflowInvoke: json.RawMessage(`{}`),
	}
}

// TestLaunchRun_GrantsTheRunsEnvelope pins what a launch records per origin, on one record because they
// are read together: recipe, origin and schedule id, slot, unattended mark.
func TestLaunchRun_GrantsTheRunsEnvelope(t *testing.T) {
	slot := time.Now().Add(30 * time.Minute)

	t.Run("a manual launch is attended and has no slot", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_manual")

		if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		l, ok := h.runs.lease("wf_manual")
		if !ok {
			t.Fatal("the manual launch granted no lease, so nothing bounds or explains the run")
		}
		if l.Origin != runlease.OriginManual {
			t.Errorf("origin = %q, want manual", l.Origin)
		}
		if l.Recipe != "publish" {
			t.Errorf("recipe = %q, want publish (the single-run rule's key)", l.Recipe)
		}
		if l.Unattended {
			t.Error("a manual run was marked unattended; the user clicked Run and can answer")
		}
		if !l.SlotAt.IsZero() {
			t.Errorf("SlotAt = %v; a manual launch has no interval to be bounded by", l.SlotAt)
		}
		if l.ScheduleID != "" {
			t.Errorf("ScheduleID = %q on a manual run", l.ScheduleID)
		}
		if l.StartedAt.IsZero() {
			t.Error("StartedAt is zero")
		}
	})

	t.Run("a scheduled launch carries its row, its slot and the floor's mark", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_sched")

		if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", "sched-1", slot); err != nil {
			t.Fatalf("LaunchScheduled: %v", err)
		}
		l, ok := h.runs.lease("wf_sched")
		if !ok {
			t.Fatal("the scheduled launch granted no lease")
		}
		if l.Origin != runlease.OriginScheduled {
			t.Errorf("origin = %q, want scheduled", l.Origin)
		}
		if l.ScheduleID != "sched-1" {
			t.Errorf("ScheduleID = %q, want sched-1; an unattended denial could not be attributed", l.ScheduleID)
		}
		if !l.Unattended {
			t.Error("a scheduled run was not marked unattended; a 03:00 permission ask would park forever")
		}
		if !l.SlotAt.Equal(slot) {
			t.Errorf("SlotAt = %v, want %v", l.SlotAt, slot)
		}
	})
}

// TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot pins that a manual run must yield to its recipe's
// next slot, derived through schedule.NextRunFrom so it cannot drift from the runner and REST row.
func TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_manual")

	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	// Every 5 minutes, the schedule floor.
	spec := schedule.Spec{Freq: schedule.FreqMinutely, Interval: 5}
	anchor := time.Now().Add(-time.Hour)
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://publish", Enabled: true, Spec: spec, Anchor: anchor,
	}
	if pErr := st.Put(t.Context(), &entry); pErr != nil {
		t.Fatalf("Put schedule: %v", pErr)
	}
	h.runs.schedules = st

	before := time.Now()
	if _, _, lErr := h.runs.launch(t.Context(), "bundled://publish", nil); lErr != nil {
		t.Fatalf("Launch: %v", lErr)
	}
	wantSlot, err := schedule.NextRunFrom(spec, anchor, before)
	if err != nil {
		t.Fatalf("NextRunFrom: %v", err)
	}

	l, ok := h.runs.lease("wf_manual")
	if !ok {
		t.Fatal("the manual launch granted no lease")
	}
	if l.SlotAt.IsZero() {
		t.Fatal("the manual run carried NO slot, so it is bounded by the hour-long ceiling and " +
			"will refuse every scheduled slot underneath it — the bug this change exists to close")
	}
	// Within a tick: on a 5-minute grid anything further is another slot.
	if l.SlotAt.Sub(wantSlot).Abs() > time.Minute {
		t.Errorf("SlotAt = %v, want the schedule's own next slot %v", l.SlotAt, wantSlot)
	}
	// The slot bounds it, floored to minRunBudget; BackstopAt is zero on a first arm.
	want := runlease.NextDeadline(before, runlease.Bounds{
		SlotAt: l.SlotAt, Idle: runIdleWindow, Floor: minRunBudget,
	})
	if l.Deadline.Sub(want).Abs() > time.Second {
		t.Errorf("deadline = %v, want the one derivation's answer %v for slot %v",
			l.Deadline, want, l.SlotAt)
	}
	if budget := l.Deadline.Sub(before); budget > minRunBudget+time.Second {
		t.Errorf("the manual run got %v of budget against a 5-minute schedule; it must not hold "+
			"the recipe for the whole %v idle window and refuse the slots underneath it",
			budget.Round(time.Second), runIdleWindow)
	}
	// Still manual in every other respect.
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual", l.Origin)
	}
	if l.Unattended {
		t.Error("the manual run was marked unattended; the user clicked Run and can answer, and " +
			"the deny-fast permission floor must not reach it")
	}
	if l.ScheduleID != "" {
		t.Errorf("ScheduleID = %q; no row asked for this run, so no row may be blamed for its "+
			"outcome", l.ScheduleID)
	}
}

// TestLaunchRun_ManualSlotIgnoresWhatCannotBindThisRun pins that another recipe's schedule must not bound this run.
func TestLaunchRun_ManualSlotIgnoresWhatCannotBindThisRun(t *testing.T) {
	for name, entry := range map[string]schedule.Entry{
		"a DISABLED schedule for this very recipe": {
			ID: "s1", Source: "bundled://publish", Enabled: false,
			Spec: schedule.Spec{Freq: schedule.FreqMinutely, Interval: 5},
		},
		"an enabled schedule for another recipe": {
			ID: "s2", Source: "bundled://other", Enabled: true,
			Spec: schedule.Spec{Freq: schedule.FreqMinutely, Interval: 5},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_manual")
			st, err := schedule.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("schedule.NewStore: %v", err)
			}
			e := entry
			if pErr := st.Put(t.Context(), &e); pErr != nil {
				t.Fatalf("Put schedule: %v", pErr)
			}
			h.runs.schedules = st

			if _, _, lErr := h.runs.launch(t.Context(), "bundled://publish", nil); lErr != nil {
				t.Fatalf("Launch: %v", lErr)
			}
			l, _ := h.runs.lease("wf_manual")
			if !l.SlotAt.IsZero() {
				t.Errorf("SlotAt = %v; this schedule cannot bind this run, so the ceiling is the "+
					"whole bound", l.SlotAt)
			}
		})
	}

	t.Run("no schedule store at all", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_manual")
		if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		if l, _ := h.runs.lease("wf_manual"); !l.SlotAt.IsZero() {
			t.Errorf("SlotAt = %v with scheduling switched off", l.SlotAt)
		}
	})
}

// TestLaunchRun_ReleasesTheLeaseWhenInvokeFails pins that a leftover lease makes the recipe look busy and arms an idle run.
func TestLaunchRun_ReleasesTheLeaseWhenInvokeFails(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_1")
	delete(br.callResults, methodKiroWorkflowInvoke)
	br.callErrs = map[string]error{methodKiroWorkflowInvoke: errRecipeBusy}

	if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil); err == nil {
		t.Fatal("a failed invoke reported success")
	}
	if _, ok := h.runs.lease("wf_1"); ok {
		t.Error("the lease of a run that never started survived its failed launch")
	}
}

// TestObserveRunComplete_ReleasesTheLeaseOfATerminalRun pins that the one site every origin reaches, including an
// agent run with no bridge of its own.
func TestObserveRunComplete_ReleasesTheLeaseOfATerminalRun(t *testing.T) {
	for name, tc := range map[string]struct {
		status    string
		stillHeld bool
	}{
		"completed": {"completed", false},
		"failed":    {"failed", false},
		"aborted":   {"aborted", false},
		// A policy pause is still resumable, so its envelope survives.
		"a policy pause": {"paused", true},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			const id = "wf_1"
			h.runs.grantLease(t.Context(), id, "publish", manualLaunch())

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": id, "status": tc.status,
			}))

			_, held := h.runs.lease(id)
			if held != tc.stillHeld {
				t.Errorf("lease held = %v after status %q, want %v", held, tc.status, tc.stillHeld)
			}
		})
	}
}

// heldRun hosts a run under run:<id> with a disk-backed lease, so a cancel lands on br and CancelRun waits on the lease.
func heldRun(t *testing.T, id, recipe string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	st, err := runlease.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h.runs.leases = st
	h.runs.grantLease(t.Context(), id, recipe, manualLaunch())
	return h, br
}

func rewindCancelWaitOf(t *testing.T, d time.Duration) {
	t.Helper()
	prev := rewindCancelWait
	rewindCancelWait = d
	t.Cleanup(func() { rewindCancelWait = prev })
}

func cancelsIssued(br *fakeBridge) int {
	n := 0
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowCancel {
			n++
		}
	}
	return n
}

// A lease release during CancelRun's polling answers nil, with one cancel issued.
func TestCancelRun_ReturnsOnceTheLeaseIsReleasedInsideTheWait(t *testing.T) {
	rewindCancelWaitOf(t, 5*time.Second)
	h, br := heldRun(t, "wf_1", "code-review")
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for cancelsIssued(br) == 0 {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		// The node boundary, a few polls after the cancel.
		time.Sleep(6 * rewindCancelPoll)
		h.runs.releaseLease(t.Context(), "wf_1")
	}()

	if err := h.runs.CancelRun(t.Context(), "wf_1"); err != nil {
		t.Fatalf("CancelRun(wf_1) = %v, want nil once the lease is released", err)
	}
	if n := cancelsIssued(br); n != 1 {
		t.Errorf("cancels issued = %d, want 1: the wait re-reads the lease, never re-cancels", n)
	}
}

func TestCancelRun_AHeldLeasePastTheWaitIsStillLive(t *testing.T) {
	rewindCancelWaitOf(t, 100*time.Millisecond)
	h, br := heldRun(t, "wf_1", "code-review")

	err := h.runs.CancelRun(t.Context(), "wf_1")
	if !errors.Is(err, command.ErrRunStillLive) {
		t.Fatalf("CancelRun(wf_1) = %v, want command.ErrRunStillLive with the lease still held", err)
	}
	if _, held := h.runs.lease("wf_1"); !held {
		t.Errorf("the lease was released, so the wait had nothing to run out on")
	}
	if n := cancelsIssued(br); n != 1 {
		t.Errorf("cancels issued = %d, want 1: the wait re-reads the lease, never re-cancels", n)
	}
}

func TestCancelRun_ACancelledContextEndsTheWait(t *testing.T) {
	rewindCancelWaitOf(t, 5*time.Second)
	h, _ := heldRun(t, "wf_1", "code-review")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := h.runs.CancelRun(ctx, "wf_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CancelRun(dead ctx) = %v, want context.Canceled", err)
	}
}

// LiveRuns answers leased runs by recipe; released or unknown ids are not live.
func TestLiveRuns_NamesTheHeldLeasesByRecipe(t *testing.T) {
	h, _ := heldRun(t, "wf_held", "code-review")
	h.runs.grantLease(t.Context(), "wf_released", "publish", manualLaunch())
	h.runs.releaseLease(t.Context(), "wf_released")

	got := h.runs.LiveRuns([]string{"wf_held", "wf_released", "wf_unknown"})
	want := []command.LiveRunRef{{ID: "wf_held", Label: "code-review"}}
	if !slices.Equal(got, want) {
		t.Errorf("LiveRuns([held released unknown]) = %+v, want %+v", got, want)
	}
}

// TestLeaseStore_FallsBackToMemory pins that a lease carries the run's clock, so there is no leases-off mode.
func TestLeaseStore_FallsBackToMemory(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())
	if _, ok := h.lease("wf_1"); !ok {
		t.Fatal("a runtime with no durable store lost the lease, so the run would be unbounded")
	}
}

// TestGrantLease_ReportsAnEnvelopeItCouldNotPersist pins that a restart would find an unrecognisable, unbounded run,
// so the line is the only warning, and must not fire on success.
func TestGrantLease_ReportsAnEnvelopeItCouldNotPersist(t *testing.T) {
	const wantLine = "run lease not persisted; this run's envelope will not survive a restart"

	t.Run("a lease that could not be written is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{leases: undurableLeaseStore(t)}

		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		if _, held := h.lease("wf_1"); !held {
			t.Fatal("the failed persist also dropped the in-memory lease, so this process " +
				"no longer bounds the run either")
		}
		out := logs.String()
		if !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a run whose envelope was lost said nothing; want a line reading %q. Got: %s",
				wantLine, out)
		}
		if !strings.Contains(out, `"recipe":"publish"`) {
			t.Errorf("the line does not name the recipe whose run is now unbounded: %s", out)
		}
	})

	t.Run("an ordinary launch is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{}

		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a lease that persisted fine was reported as lost: %s", out)
		}
	})
}

// TestReleaseLease_ReportsAReleaseItCouldNotWrite pins that a stale lease would resurrect at restart.
func TestReleaseLease_ReportsAReleaseItCouldNotWrite(t *testing.T) {
	const wantLine = "run lease not released on disk"

	t.Run("a release that could not be written is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{leases: undurableLeaseStore(t)}
		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		h.releaseLease(t.Context(), "wf_1")

		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a release that stayed on disk said nothing; want a line reading %q. Got: %s",
				wantLine, out)
		}
	})

	t.Run("an ordinary release is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{}
		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		h.releaseLease(t.Context(), "wf_1")

		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a release that worked was reported as failed: %s", out)
		}
	})
}
