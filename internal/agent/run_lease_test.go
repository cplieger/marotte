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
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// launchableRecipe is the reply set a launch needs: one recipe, an empty run list, ids for new and invoke.
func launchableRecipe(br *fakeBridge, workflowID string) {
	br.mu.Lock()
	defer br.mu.Unlock()
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

		if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
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

		if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"}, nil, marotte.RunDefaults{}, slot); err != nil {
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

	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	// Every 5 minutes, the schedule floor.
	spec := launchrule.Spec{Freq: launchrule.FreqMinutely, Interval: 5}
	anchor := time.Now().Add(-time.Hour)
	entry := putClockRule(t, st, "bundled://publish", spec, true)
	anchorClockRule(t, st, entry.Row(), anchor)
	h.runs.launchRules = st

	before := time.Now()
	if _, _, _, lErr := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); lErr != nil {
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
	// The slot bounds it, floored to minRunBudget.
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
	for name, rule := range map[string]struct {
		source  string
		enabled bool
	}{
		"a DISABLED schedule for this very recipe": {source: "bundled://publish"},
		"an enabled schedule for another recipe":   {source: "bundled://other", enabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_manual")
			st, _, err := launchrule.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("launchrule.NewStore: %v", err)
			}
			putClockRule(t, st, rule.source, launchrule.Spec{Freq: launchrule.FreqMinutely, Interval: 5}, rule.enabled)
			h.runs.launchRules = st

			if _, _, _, lErr := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); lErr != nil {
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
		if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
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
	br.callErrs = map[string]error{methodKiroWorkflowInvoke: errHoldFault}

	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err == nil {
		t.Fatal("a failed invoke reported success")
	}
	if _, ok := h.runs.lease("wf_1"); ok {
		t.Error("the lease of a run that never started survived its failed launch")
	}
}

// nightlyLaunch is a scheduled hub whose launch of bundled://nightly creates wf_1.
func nightlyLaunch(t *testing.T) (*Runtime, *fakeBridge, *launchrule.Store, launchrule.Rule) {
	t.Helper()
	h, br, st, entry := scheduledHub(t)
	launchableRecipe(br, "wf_1")
	br.callResults[methodKiroWorkflowListRecipes] = json.RawMessage(
		`{"recipes":[{"name":"nightly","source":"bundled://nightly"}]}`,
	)
	return h, br, st, entry
}

// A scheduled launch whose caller stops waiting once workflow/new has answered still starts the run and names it: the
// row already reads that run, and a launch reported as nothing would let the next start fire the slot again.
func TestLaunchScheduled_ACallerCancelOnceTheRunExistsStillLaunchesIt(t *testing.T) {
	h, br, st, entry := nightlyLaunch(t)
	br.refuseCancelled = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == methodKiroWorkflowNew {
			cancel()
		}
		return nil, nil, false
	}
	before := time.Now()

	id, _, err := h.runs.LaunchScheduled(ctx, entry.Source, entry.Row(), nil, marotte.RunDefaults{}, before.Add(time.Hour))
	if id != "wf_1" || err != nil {
		t.Errorf("LaunchScheduled = %q, %v; want wf_1 started", id, err)
	}
	if !slices.Contains(br.wireLog(), methodKiroWorkflowInvoke) {
		t.Error("the created run was never invoked")
	}
	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("the created run's lease was released")
	}
	row := st.Clocks()[0]
	if row.LastRunID != "wf_1" || row.LastStatus != "started" || row.LastRunAt.Before(before) {
		t.Errorf("row = %+v, want wf_1 started at or after %v", row, before)
	}
}

// Dropping the caller's cancel keeps the launch's deadline: an invoke the engine never answers ends at it, so the
// schedule runner's sweep is not held until shutdown. KAS may have started the run, so it keeps its lease and bound.
func TestLaunchScheduled_ACallerCancelOnceTheRunExistsKeepsTheLaunchDeadline(t *testing.T) {
	saved := launchTimeout
	launchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { launchTimeout = saved })
	h, br, _, nightly := nightlyLaunch(t)
	dir := t.TempDir()
	st, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	entry := putClockRule(t, st, nightly.Source, *nightly.When.Clock, true)
	h.runs.launchRules = st
	br.refuseCancelled = true
	unanswered := make(chan struct{})
	t.Cleanup(func() { close(unanswered) })
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowInvoke: unanswered}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == methodKiroWorkflowNew {
			cancel()
		}
		return nil, nil, false
	}
	type answer struct {
		err error
		id  string
	}
	done := make(chan answer, 1)
	go func() {
		id, _, lErr := h.runs.LaunchScheduled(ctx, entry.Source, entry.Row(), nil, marotte.RunDefaults{},
			time.Now().Add(time.Hour))
		done <- answer{id: id, err: lErr}
	}()

	var got answer
	select {
	case got = <-done:
	case <-time.After(10 * launchTimeout):
		t.Fatal("LaunchScheduled outlived its launch deadline")
	}
	if got.id != "wf_1" || got.err != nil {
		t.Errorf("LaunchScheduled = %q, %v; want wf_1 reported launched, its start unconfirmed", got.id, got.err)
	}
	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("the lease was released, so a run KAS may have started runs with no envelope")
	}
	if !h.runs.bounded("wf_1") {
		t.Error("the run's deadline was not armed, so nothing bounds a run KAS may have started")
	}
	reopened, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen launchrule.NewStore: %v", err)
	}
	if row := reopened.Clocks()[0]; row.LastRunID != "wf_1" || row.LastStatus != "started" {
		t.Errorf("row on disk = %+v, want wf_1 started, its ending left to the run", row)
	}
}

// A run created but never invoked is named with its failure, so the runner records the slot it tried as failed with
// that run rather than as a launch that created nothing.
func TestLaunchScheduled_AFailedInvokeNamesTheRunWithItsFailure(t *testing.T) {
	h, br, st, entry := nightlyLaunch(t)
	delete(br.callResults, methodKiroWorkflowInvoke)
	br.callErrs = map[string]error{methodKiroWorkflowInvoke: errHoldFault}

	id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{},
		time.Now().Add(time.Hour))
	if id != "wf_1" || !errors.Is(err, errHoldFault) {
		t.Errorf("LaunchScheduled = %q, %v; want wf_1 with the invoke failure", id, err)
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the lease of a run that never started survived its failed launch")
	}
	row := st.Clocks()[0]
	if row.LastRunID != "wf_1" || row.LastStatus != launchrule.StatusFailed ||
		!strings.Contains(row.LastReason, errHoldFault.Error()) {
		t.Errorf("row = %+v, want wf_1 failed with %q", row, errHoldFault)
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
			h.runs.grantLease(t.Context(), id, "publish", manualLaunch(), hostSec())

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
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeReady))
	st, err := runlease.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h.runs.leases = st
	h.runs.grantLease(t.Context(), id, recipe, manualLaunch(), hostSec())
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

// LiveRuns answers leased runs; released or unknown ids are not live.
func TestLiveRuns_NamesTheHeldLeasesAndNoOthers(t *testing.T) {
	h, _ := heldRun(t, "wf_held", "code-review")
	h.runs.grantLease(t.Context(), "wf_released", "publish", manualLaunch(), hostSec())
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
	h := &Runs{chats: newTestChatStore()}
	h.grantLease(t.Context(), "wf_1", "publish", manualLaunch(), hostSec())
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
		h := &Runs{chats: newTestChatStore(), leases: undurableLeaseStore(t)}

		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch(), hostSec())

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
		h := &Runs{chats: newTestChatStore()}

		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch(), hostSec())

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
		h := &Runs{chats: newTestChatStore(), leases: undurableLeaseStore(t)}
		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch(), hostSec())

		h.releaseLease(t.Context(), "wf_1")

		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a release that stayed on disk said nothing; want a line reading %q. Got: %s",
				wantLine, out)
		}
	})

	t.Run("an ordinary release is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{chats: newTestChatStore()}
		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch(), hostSec())

		h.releaseLease(t.Context(), "wf_1")

		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a release that worked was reported as failed: %s", out)
		}
	})
}

// A schedule deleted and added again while its launch waits on the engine gets nothing from that launch: the run's
// lease names the deleted rule, so neither its start nor its ending lands on the new one.
func TestLaunchScheduled_ARunOfARecreatedScheduleLeavesTheNewRowAlone(t *testing.T) {
	h, br, st, entry := nightlyLaunch(t)
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == methodKiroWorkflowNew {
			if _, err := st.Delete(t.Context(), entry.ID); err != nil {
				t.Errorf("Delete: %v", err)
			}
			putClockRule(t, st, entry.Source, launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 5}, true)
		}
		return nil, nil, false
	}

	id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
	if id != "wf_1" || err != nil {
		t.Fatalf("LaunchScheduled = %q, %v; want wf_1 started", id, err)
	}
	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": id, "status": "failed", "finalState": map[string]any{"stopReason": "step build failed"},
	}))

	if row := st.Clocks()[0]; row.LastRunID != "" || row.LastStatus != "" || row.LastReason != "" || !row.LastRunAt.IsZero() {
		t.Errorf("recreated row = %+v, want no run recorded on it", row)
	}
}

// eventLaunch is a triggered launch of one queued event of rule rule-1.
func eventLaunch() launchOrigin {
	return triggeredLaunch("rule-1", "github.com/o/r/run/7/success", "github.com/o/r/run/7", "run 7")
}

// A triggered launch's lease is granted between workflow/new and invoke, and the event counts as launched only on the
// run's run_start: the grant confirms nothing.
func TestGrantLeaseDoesNotConfirmEvent(t *testing.T) {
	fc := &fakeConfirmer{}
	h, _, br := newTestHub(WithTriggerConfirmer(fc))
	launchableRecipe(br, "wf_1")

	if _, _, _, err := h.runs.launchAs(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, eventLaunch()); err != nil {
		t.Fatalf("launchAs: %v", err)
	}
	l, held := h.runs.lease("wf_1")
	if !held {
		t.Fatal("the triggered launch granted no lease")
	}
	if l.Origin != runlease.OriginTriggered || !l.Unattended {
		t.Errorf("lease origin %q unattended %v, want triggered and unattended", l.Origin, l.Unattended)
	}
	if l.RuleID != "rule-1" || l.EventKey != "github.com/o/r/run/7/success" {
		t.Errorf("lease rule %q event %q, want the launch's rule-1 and its event key", l.RuleID, l.EventKey)
	}
	for _, c := range fc.recorded() {
		if strings.HasPrefix(c, "ConfirmEvent") {
			t.Errorf("the grant confirmed the event (%q) before any run_start", c)
		}
	}
}

// The coalesce key is written on every triggered lease and survives a restart; nothing in this release reads it.
func TestTriggeredGrantStoresCoalesceKey(t *testing.T) {
	dir := t.TempDir()
	st, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("runlease.NewStore: %v", err)
	}
	h := &Runs{chats: newTestChatStore(), leases: st}

	if !h.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec()) {
		t.Fatal("grantLease reported a lease it wrote as not persisted")
	}
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen runlease.NewStore: %v", err)
	}
	l, held := reopened.Get("wf_1")
	if !held {
		t.Fatal("the triggered lease did not survive a reload")
	}
	if l.CoalesceKey != "github.com/o/r/run/7" {
		t.Errorf("coalesce key after reload = %q, want github.com/o/r/run/7", l.CoalesceKey)
	}
}

// A triggered run's row start is written by its confirmation, so the grant leaves the rule's row alone; a scheduled
// grant still records the start.
func TestTriggeredGrantWritesNoRowStart(t *testing.T) {
	t.Run("triggered", func(t *testing.T) {
		h, _, st, entry := scheduledHub(t)
		o := triggeredLaunch(entry.ID, "github.com/o/r/run/7/success", "github.com/o/r/run/7", "run 7")

		h.runs.grantLease(t.Context(), "wf_1", "nightly", o, hostSec())

		if row := st.Clocks()[0]; row.LastRunID != "" || row.LastStatus != "" || !row.LastRunAt.IsZero() {
			t.Errorf("row = %+v after a triggered grant, want no start recorded", row)
		}
	})
	t.Run("scheduled", func(t *testing.T) {
		h, _, st, entry := scheduledHub(t)

		h.runs.grantLease(t.Context(), "wf_1", "nightly", scheduledLaunch(entry.Row(), time.Now().Add(time.Hour)), hostSec())

		if row := st.Clocks()[0]; row.LastRunID != "wf_1" || row.LastStatus != launchrule.StatusStarted {
			t.Errorf("row = %+v after a scheduled grant, want wf_1 started", row)
		}
	})
}

// A triggered run is named after its event, which the lease alone carries: RunLabel and the run's display name read
// the label the launch sent KAS.
func TestTriggeredRunLabel(t *testing.T) {
	h := &Runs{chats: newTestChatStore()}
	h.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())

	if got := h.RunLabel("wf_1"); got != "publish · run 7" {
		t.Errorf("RunLabel(triggered) = %q, want %q", got, "publish · run 7")
	}
	l, _ := h.lease("wf_1")
	if got := leaseRunLabel(&l); got != "publish · run 7" {
		t.Errorf("leaseRunLabel = %q, want the label the launch sent", got)
	}
}

// After a restart the label comes from the persisted lease, since nothing else can rebuild an event's label; a lease
// written before leases kept the label still rebuilds a scheduled run's.
func TestTriggeredRunLabelSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("runlease.NewStore: %v", err)
	}
	h := &Runs{chats: newTestChatStore(), leases: st}
	h.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())
	if err := st.Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_old", Recipe: "nightly", Origin: runlease.OriginScheduled, ScheduleID: "sched-1",
		Unattended: true, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Put the pre-label lease: %v", err)
	}

	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen runlease.NewStore: %v", err)
	}
	after := &Runs{chats: newTestChatStore(), leases: reopened}
	if got := after.RunLabel("wf_1"); got != "publish · run 7" {
		t.Errorf("RunLabel after restart = %q, want %q", got, "publish · run 7")
	}
	if got := after.RunLabel("wf_old"); got != "nightly · scheduled" {
		t.Errorf("RunLabel of a lease with no stored label = %q, want the rebuilt %q", got, "nightly · scheduled")
	}
}
