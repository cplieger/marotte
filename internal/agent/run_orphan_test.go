package agent

// The unacceptable failure is cancelling a live run, so most cases assert a refusal.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// kasRuns builds a workflow/list reply, defaulting `workflowName` to `name` as the wire always carries both.
func kasRuns(t *testing.T, rows ...map[string]any) json.RawMessage {
	t.Helper()
	for _, row := range rows {
		if _, ok := row["workflowName"]; !ok {
			row["workflowName"] = row["name"]
		}
	}
	raw, err := json.Marshal(map[string]any{"runs": rows})
	if err != nil {
		t.Fatalf("marshal runs: %v", err)
	}
	return raw
}

// inspectReply builds a KAS inspect reply. It takes the workflow id: the predicate refuses a reply naming another run.
func inspectReply(t *testing.T, workflowID string, status marotte.RunStatus, reason string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state":      map[string]any{"status": status, "pauseReason": reason},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// inspectPaused is the ordinary positive shape: this run, paused, for this reason.
func inspectPaused(t *testing.T, workflowID, reason string) json.RawMessage {
	t.Helper()
	return inspectReply(t, workflowID, marotte.RunStatusPaused, reason)
}

// inspectPausedWithDetail adds `state.pauseDetail`; reason and detail are separate since they can disagree.
// `occurredAt` is written because KAS sends it.
func inspectPausedWithDetail(t *testing.T, workflowID, reason string, d pauseDetail) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"status":      marotte.RunStatusPaused,
			"pauseReason": reason,
			"pauseDetail": map[string]any{
				"class": d.Class, "code": d.Code,
				"occurredAt": "2026-09-04T12:03:43.000Z",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// A parallel branch's pause, verbatim from a real run (KAS 2.21.0): the sentence matches no reason arm and
// the detail equals a plain step's.
const branchWrapperReason = "Parallel 'phase1' is waiting on branch 'live-verify' " +
	"(branch paused on transient error EAI_AGAIN)."

// branchWaitReason is the same wrapper with no cause, which interruptions, failures and need-input parks all produce.
const branchWaitReason = "Parallel 'phase1' is waiting on branch 'live-verify'."

func transientDetail() pauseDetail {
	return pauseDetail{
		Class: transientErrorClass,
		Code:  "EAI_AGAIN",
	}
}

// TestRestartPaused_AcceptsOnlyKASsOwnRestartLiteral pins that many KAS sites set a pause reason and only one means
// the owner died; cancelling any other `paused` run destroys work.
func TestRestartPaused_AcceptsOnlyKASsOwnRestartLiteral(t *testing.T) {
	for name, tc := range map[string]struct {
		reason string
		want   bool
	}{
		"KAS's restart literal":         {stalePauseReason, true},
		"a deliberate pause":            {"Paused by user request", false},
		"a policy stop":                 {"Maximum iterations reached", false},
		"a step waiting for input":      {"Waiting for user input", false},
		"a torn plan":                   {"Plan was modified during execution", false},
		"no reason at all":              {"", false},
		"a prefix of the literal":       {"Interrupted by agent restart;", false},
		"the literal with a suffix":     {stalePauseReason + " (retry 2)", false},
		"the literal in different case": {"interrupted by agent restart; the previously running step was paused for resume.", false},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: inspectPaused(t, "wf_1", tc.reason),
			}
			if got := h.runs.restartPaused(t.Context(), "wf_1"); got != tc.want {
				t.Errorf("restartPaused(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}

	t.Run("a failed inspect leaves the run alone", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
		if h.runs.restartPaused(t.Context(), "wf_1") {
			t.Error("an unreadable pause reason was treated as a dead process; at boot the " +
				"likeliest cause is that kiro-cli is still installing")
		}
	})

	t.Run("an undecodable inspect reply leaves the run alone", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: json.RawMessage(`{"state":`),
		}
		if h.runs.restartPaused(t.Context(), "wf_1") {
			t.Error("a malformed inspect reply was treated as a dead process")
		}
	})

	// The reply must name this run and still say paused, or a lease's id could be cancelled on another run's state.
	t.Run("the reply must be about this run and still say paused", func(t *testing.T) {
		for name, reply := range map[string]json.RawMessage{
			"a reply naming a DIFFERENT run": inspectPaused(t, "wf_other", stalePauseReason),
			"a reply naming an empty run":    inspectPaused(t, "", stalePauseReason),
			"a reply naming no run at all": json.RawMessage(
				`{"state":{"status":"paused","pauseReason":"` + stalePauseReason + `"}}`,
			),
			"a run KAS says is running":   inspectReply(t, "wf_1", "running", stalePauseReason),
			"a run KAS says completed":    inspectReply(t, "wf_1", "completed", stalePauseReason),
			"a reply carrying no status":  inspectReply(t, "wf_1", "", stalePauseReason),
			"a status in different case":  inspectReply(t, "wf_1", "Paused", stalePauseReason),
			"nothing but the pause state": json.RawMessage(`{"state":{"pauseReason":"x"}}`),
		} {
			t.Run(name, func(t *testing.T) {
				h, _, br := newTestHub()
				br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
				if h.runs.restartPaused(t.Context(), "wf_1") {
					t.Errorf("reply %s was accepted as proof that wf_1's process died", reply)
				}
			})
		}
	})

	// An empty id would match a reply with no workflowId.
	t.Run("an empty workflow id is never asked about", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: json.RawMessage(
				`{"state":{"status":"paused","pauseReason":"` + stalePauseReason + `"}}`,
			),
		}
		if h.runs.restartPaused(t.Context(), "") {
			t.Error("the empty workflow id read as a dead process")
		}
		if len(br.callLog()) != 0 {
			t.Errorf("the empty workflow id was put on the wire: %v", br.callLog())
		}
	})
}

// TestSweepOrphanedRuns_NeverTouchesARunItDoesNotOwn pins that each case is live or not marotte's, and each would be
// cancelled by one plausible widening. Bridge presence is unused: after a restart no run has one.
func TestSweepOrphanedRuns_NeverTouchesARunItDoesNotOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		lease  *runlease.Lease
		status marotte.RunStatus
		reason string
	}{
		"a TUI-launched run, which has no lease": {
			lease: nil, status: marotte.RunStatusPaused, reason: stalePauseReason,
		},
		"an agent-launched run, which its chat resumes": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginAgent},
			status: marotte.RunStatusPaused, reason: stalePauseReason,
		},
		"a run paused by a policy stop": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual},
			status: marotte.RunStatusPaused, reason: "Maximum iterations reached",
		},
		"a run paused by a person on purpose": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled},
			status: marotte.RunStatusPaused, reason: "Paused by user request",
		},
		"a run that is still running": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled},
			status: "running", reason: "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_1", "name": "publish", "status": tc.status,
				}),
				methodKiroWorkflowInspect: inspectPaused(t, "wf_1", tc.reason),
				methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			}
			if tc.lease != nil {
				if err := h.runs.leaseStore().Put(t.Context(), tc.lease); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			// No live bridge in any case, as after a restart.
			if h.bridge.mgr.get(runChatID("wf_1")) != nil {
				t.Fatal("the fixture registered a bridge; the sweep must be wrong-by-default without one")
			}

			h.runs.SweepOrphaned(t.Context())

			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("the sweep recorded %q against a run it does not own", got)
			}
			for _, m := range br.callLog() {
				if m == methodKiroWorkflowCancel {
					t.Fatal("the sweep CANCELLED a live run; this is the failure the predicate " +
						"is narrow to prevent")
				}
			}
			if tc.lease != nil {
				if _, held := h.runs.lease("wf_1"); !held {
					t.Error("the sweep released the lease of a run it must leave alone")
				}
			}
		})
	}
}

// TestSweepOrphanedRuns_ClearsTheRunARestartOrphaned pins that otherwise the `paused` orphan blocks every later slot.
func TestSweepOrphanedRuns_ClearsTheRunARestartOrphaned(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "nightly", "status": marotte.RunStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
	}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "nightly", Origin: runlease.OriginScheduled,
		ScheduleID: "sched-1", Unattended: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	var cancelled bool
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowCancel {
			cancelled = true
		}
	}
	if !cancelled {
		t.Errorf("no cancel went out for the orphan: %v", br.callLog())
	}
	wantStopParams(t, "sweep cancel", br.paramsFor(methodKiroWorkflowCancel), false, "")
	// Its own reason: the row names which of four stops it was.
	if got := h.runs.endReason("wf_1"); got != runEndOrphaned {
		t.Errorf("the orphan recorded %q, want %q", got, runEndOrphaned)
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the cleared orphan kept its lease, so the recipe still reads as marotte's own")
	}
}

// TestSweepOrphanedRuns_RecordsTheSweepOnTheSchedulesRow pins that the schedule must stop reading "started".
func TestSweepOrphanedRuns_RecordsTheSweepOnTheSchedulesRow(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "nightly", "status": marotte.RunStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
	}
	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://nightly", Enabled: true,
		Spec: schedule.Spec{Freq: schedule.FreqDaily, Hour: 2},
	}
	if err := st.Put(t.Context(), &entry); err != nil {
		t.Fatalf("Put schedule: %v", err)
	}
	h.runs.schedules = st
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "nightly", Origin: runlease.OriginScheduled,
		ScheduleID: "sched-1", Unattended: true,
	}); err != nil {
		t.Fatalf("Put lease: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	rows := st.List()
	if len(rows) != 1 {
		t.Fatalf("schedule rows = %d, want 1", len(rows))
	}
	if rows[0].LastStatus != schedule.StatusFailed || rows[0].LastReason != reasonOrphaned {
		t.Errorf("schedule outcome = (%q, %q), want (%q, %q)",
			rows[0].LastStatus, rows[0].LastReason, schedule.StatusFailed, reasonOrphaned)
	}
}

// TestSweepOrphanedRuns_ReleasesTerminalLeasesImmediately pins that bookkeeping for both origins; an agent lease
// outliving its run keeps its chat exempt from eviction.
func TestSweepOrphanedRuns_ReleasesTerminalLeasesImmediately(t *testing.T) {
	for name, origin := range map[string]runlease.Origin{
		"manual": runlease.OriginManual,
		"agent":  runlease.OriginAgent,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_1", "name": "publish", "status": "completed",
				}),
			}
			if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
				WorkflowID: "wf_1", Recipe: "publish", ChatID: "c-live", Origin: origin,
			}); err != nil {
				t.Fatalf("Put: %v", err)
			}

			h.runs.SweepOrphaned(t.Context())

			if _, held := h.runs.lease("wf_1"); held {
				t.Error("a terminal run kept its lease")
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Fatal("the bookkeeping arm cancelled a run KAS already reported terminal")
			}
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("a terminal run was recorded as %q; nothing was cancelled", got)
			}
		})
	}
}

// TestSweepOrphanedRuns_AbsenceStartsAClockWithoutReleasing pins the first miss.
func TestSweepOrphanedRuns_AbsenceStartsAClockWithoutReleasing(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t)}
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	got, held := h.runs.lease("wf_1")
	if !held {
		t.Fatal("an absent run lost its lease without terminal evidence")
	}
	if got.FirstAbsentAt.IsZero() {
		t.Error("an absent run did not start its continuous-absence clock")
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Fatal("absence caused a cancel; the backstop is bookkeeping-only")
	}
}

// TestRecipeIdle_ReappearanceClearsTheAbsenceClock pins continuity.
func TestRecipeIdle_ReappearanceClearsTheAbsenceClock(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "publish", "status": "running",
		}),
	}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual,
		FirstAbsentAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := h.runs.recipeIdle(t.Context(), "another-recipe"); err != nil {
		t.Fatalf("recipeIdle(another-recipe): %v", err)
	}
	got, held := h.runs.lease("wf_1")
	if !held {
		t.Fatal("a listed live run lost its lease")
	}
	if !got.FirstAbsentAt.IsZero() {
		t.Errorf("FirstAbsentAt = %v after reappearance, want zero", got.FirstAbsentAt)
	}
}

// TestSweepOrphanedRuns_ContinuousAbsenceBackstopReleasesAndRecordsOutcome pins the six-hour backstop; absence never cancels.
func TestSweepOrphanedRuns_ContinuousAbsenceBackstopReleasesAndRecordsOutcome(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t)}
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}

	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://publish", Enabled: true,
		Spec: schedule.Spec{Freq: schedule.FreqDaily, Hour: 2},
	}
	if err := st.Put(t.Context(), &entry); err != nil {
		t.Fatalf("Put schedule: %v", err)
	}
	h.runs.schedules = st
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled,
		ScheduleID: "sched-1", FirstAbsentAt: time.Now().Add(-6 * time.Hour),
	}); err != nil {
		t.Fatalf("Put lease: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	if _, held := h.runs.lease("wf_1"); held {
		t.Error("a lease continuously absent past the budget was not released")
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Fatal("the absence backstop cancelled the run")
	}
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) ||
		!strings.Contains(out, "no terminal signal was ever seen") {
		t.Errorf("absence backstop log = %s, want WARN stating no terminal signal was seen", out)
	}
	rows := st.List()
	if len(rows) != 1 {
		t.Fatalf("schedule rows = %d, want 1", len(rows))
	}
	const wantReason = "no terminal signal was seen and the run stayed absent for 6 hours"
	if rows[0].LastStatus != schedule.StatusUnknown || rows[0].LastReason != wantReason {
		t.Errorf("schedule outcome = (%q, %q), want (%q, %q)",
			rows[0].LastStatus, rows[0].LastReason, schedule.StatusUnknown, wantReason)
	}
}

// TestSweepOrphanedRuns_InspectUnknownReleasesImmediately pins the definitive
// per-run answer KAS gives after its stale-run reconciliation.
func TestSweepOrphanedRuns_InspectUnknownReleasesImmediately(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t)}
	br.callRPCErrs = map[string]*marotte.RPCError{
		methodKiroWorkflowInspect: {
			Code: -32603, Message: "Internal error",
			Data: json.RawMessage(`{"details":"workflow not found"}`),
		},
	}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	if _, held := h.runs.lease("wf_1"); held {
		t.Error("inspect confirmed an unknown workflow, but its lease survived")
	}
}

// TestSweepOrphanedRuns_InspectFailureStartsTheClock pins the fail-safe branch.
func TestSweepOrphanedRuns_InspectFailureStartsTheClock(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t)}
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginAgent,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	got, held := h.runs.lease("wf_1")
	if !held || got.FirstAbsentAt.IsZero() {
		t.Errorf("lease after inspect failure = %+v, held=%v; want held with absence clock", got, held)
	}
}

// TestSweepOrphanedRuns_EmptySuccessfulListReleasesNothing pins that one short reply must not drop every lease.
func TestSweepOrphanedRuns_EmptySuccessfulListReleasesNothing(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t)}
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
	for _, id := range []string{"wf_1", "wf_2", "wf_3"} {
		if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
			WorkflowID: id, Recipe: "publish", Origin: runlease.OriginManual,
		}); err != nil {
			t.Fatalf("Put(%s): %v", id, err)
		}
	}

	h.runs.SweepOrphaned(t.Context())

	if got := len(h.runs.leaseStore().List()); got != 3 {
		t.Errorf("leases after empty successful list = %d, want 3", got)
	}
}

// TestSweepOrphanedRuns_LeavesEveryLeaseAloneWhenTheListFails pins that at boot kiro-cli may still be installing.
func TestSweepOrphanedRuns_LeavesEveryLeaseAloneWhenTheListFails(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowList: errRecipeBusy}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("an unreadable run list released a lease, so a live run's envelope is gone")
	}
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowCancel {
			t.Fatal("the sweep cancelled a run it could not see the status of")
		}
	}
}

// TestSweepOrphanedRuns_KeepsTheLeaseWhenTheCancelFails pins that the kept lease lets the next launch retry, and the
// row must not announce an ending that did not happen.
func TestSweepOrphanedRuns_KeepsTheLeaseWhenTheCancelFails(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "publish", "status": marotte.RunStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
	}
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errRecipeBusy}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("a failed cancel released the lease, stranding a paused KAS row with nothing " +
			"left to explain it")
	}
	if got := h.runs.endReason("wf_1"); got != "" {
		t.Errorf("the row reads %q after a cancel that never landed; the run is still paused "+
			"in KAS and the next admission attempt retries the clear, so History must not "+
			"already say it was stopped", got)
	}
	// The claim is back for that retry.
	if !h.runs.claimTermination("wf_1") {
		t.Error("the failed cancel kept the termination claim, so nothing can clear the orphan")
	}
}

// TestClearOrphanedRun_RefusesWhenTheRunNoLongerReadsAsAnOrphan pins that re-asking just before the cancel narrows
// the window to one round trip (KAS has no compare-and-cancel); nothing marotte owns can resume such a run.
func TestClearOrphanedRun_RefusesWhenTheRunNoLongerReadsAsAnOrphan(t *testing.T) {
	for name, reply := range map[string]json.RawMessage{
		"it is executing again":       inspectReply(t, "wf_1", "running", stalePauseReason),
		"it was paused on purpose":    inspectPaused(t, "wf_1", "Paused by user request"),
		"the reply names another run": inspectPaused(t, "wf_other", stalePauseReason),
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: reply,
				methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			}
			l := runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual}
			if err := h.runs.leaseStore().Put(t.Context(), &l); err != nil {
				t.Fatalf("Put: %v", err)
			}

			if h.runs.clearOrphaned(t.Context(), &l) {
				t.Error("a run that no longer reads as an orphan was cleared")
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("a cancel went out for a run that is not an orphan: %v", br.callLog())
			}
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("the row reads %q for a run nothing ended", got)
			}
			if _, held := h.runs.lease("wf_1"); !held {
				t.Error("the lease of a run that was left alone was released")
			}
			// The claim goes back.
			if !h.runs.claimTermination("wf_1") {
				t.Error("the refusal kept the termination claim, so no bound and no Cancel " +
					"button can ever act on this run")
			}
		})
	}
}

// TestRecipeIdle_ClearsABlockingOrphanAndProceeds pins that a run can be orphaned without a restart.
func TestRecipeIdle_ClearsABlockingOrphanAndProceeds(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_old", "name": "publish", "status": marotte.RunStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_old", stalePauseReason),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
	}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginScheduled,
		ScheduleID: "sched-1", Unattended: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := h.runs.recipeIdle(t.Context(), "publish"); err != nil {
		t.Fatalf("admission refused a launch over an orphan it owns: %v", err)
	}
	if got := h.runs.endReason("wf_old"); got != runEndOrphaned {
		t.Errorf("the cleared orphan recorded %q, want %q", got, runEndOrphaned)
	}
	if _, held := h.runs.lease("wf_old"); held {
		t.Error("the cleared orphan kept its lease")
	}
}

// TestRecipeIdle_RefusesALabelledRunOfTheSameRecipe pins that `name` is `runLabel ?? workflowName`, so matching on it failed open.
func TestRecipeIdle_RefusesALabelledRunOfTheSameRecipe(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_live", "status": "running",
			// KAS's watch stamp made the name `<recipe>-<targetId>`.
			"name": "publish-pr-4127", "workflowName": "publish",
		}),
	}

	if err := h.runs.recipeIdle(t.Context(), "publish"); err == nil {
		t.Fatal("admission allowed a second live run of a recipe whose live run wears a label")
	}
}

// TestRecipeIdle_StillRefusesEveryBlockingRowItCannotExplain pins that only KAS's list sees agent and TUI runs.
func TestRecipeIdle_StillRefusesEveryBlockingRowItCannotExplain(t *testing.T) {
	for name, tc := range map[string]struct {
		lease  *runlease.Lease
		status marotte.RunStatus
		reason string
	}{
		"a TUI-launched run, unleased": {
			lease: nil, status: marotte.RunStatusPaused, reason: stalePauseReason,
		},
		"an agent-launched run": {
			lease:  &runlease.Lease{WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginAgent},
			status: marotte.RunStatusPaused, reason: stalePauseReason,
		},
		"a leased run that is still running": {
			lease:  &runlease.Lease{WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginManual},
			status: "running", reason: "",
		},
		"a leased run paused on purpose": {
			lease:  &runlease.Lease{WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginManual},
			status: marotte.RunStatusPaused, reason: "Paused by user request",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_old", "name": "publish", "status": tc.status,
				}),
				methodKiroWorkflowInspect: inspectPaused(t, "wf_old", tc.reason),
				methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			}
			if tc.lease != nil {
				if err := h.runs.leaseStore().Put(t.Context(), tc.lease); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}

			if err := h.runs.recipeIdle(t.Context(), "publish"); err == nil {
				t.Fatal("admission allowed a second live run of one recipe")
			}
			for _, m := range br.callLog() {
				if m == methodKiroWorkflowCancel {
					t.Fatal("admission cancelled a run it cannot explain")
				}
			}
		})
	}
}

// TestOrphanSweepBudget_ExceedsThePerCallTimeout pins that the sequential inspects need a budget above one call.
func TestOrphanSweepBudget_ExceedsThePerCallTimeout(t *testing.T) {
	t.Parallel()
	if orphanSweepBudget <= sessionListTimeout {
		t.Errorf("orphanSweepBudget = %v, not longer than one call's %v, so the sweep would "+
			"time out before its first inspect could answer", orphanSweepBudget, sessionListTimeout)
	}
	if orphanSweepBudget > 5*time.Minute {
		t.Errorf("orphanSweepBudget = %v; a boot goroutine holding a utility bridge that long "+
			"is a stall, not a sweep", orphanSweepBudget)
	}
}

// TestResumablePause_CoversEveryInvoluntaryPauseAndNothingElse pins that of KAS's pause causes (involuntary, waiting
// on a human, policy) only the first may resume unasked; the negatives include what a loose prefix swallows.
func TestResumablePause_CoversEveryInvoluntaryPauseAndNothingElse(t *testing.T) {
	transient := transientDetail()

	for name, tc := range map[string]struct {
		detail *pauseDetail
		reason string
		want   bool
	}{
		// The reason arm, no detail. Involuntary.
		"the reconcile's restart literal": {nil, stalePauseReason, true},
		"an interrupted step":             {nil, interruptedPauseReason, true},
		"a transient model 5xx":           {nil, modelServicePauseReason, true},
		"a transient network code":        {nil, "Transient connection error (EAI_AGAIN); the run is paused and can be resumed.", true},
		"a different network code":        {nil, "Transient connection error (ECONNRESET); the run is paused and can be resumed.", true},

		// Waiting on a human.
		"a step that asked for input":   {nil, "Step requested user input via send_message.", false},
		"a step awaiting the next turn": {nil, "Step 'review' is waiting for the next user message.", false},
		"a step awaiting user input":    {nil, "Step 'design' is waiting for user input.", false},

		// Policy or over.
		"a repeat at maxIterations":   {nil, "Repeat 'implement' reached maxIterations.", false},
		"a repeat aborted at the cap": {nil, "Repeat 'implement' aborted at maxIterations.", false},
		"a recorded failure":          {nil, "Run failed: the reviewer never approved", false},
		"a deliberate pause":          {nil, "Paused by user request", false},

		// Shapes a careless prefix would swallow, detail-less so the prefix stays tested.
		"no reason at all":                           {nil, "", false},
		"the network phrase mid-sentence":            {nil, "Step failed: Transient connection error (EAI_AGAIN)", false},
		"the network phrase without its parenthesis": {nil, "Transient connection error EAI_AGAIN", false},
		"a permanent connection failure":             {nil, "Permanent connection error (ENOTFOUND); the run failed.", false},
		"the interruption literal truncated":         {nil, "Step interrupted (agent shutdown or connection reset)", false},
		"the restart literal in different case":      {nil, "interrupted by agent restart; the previously running step was paused for resume.", false},

		// The detail arm, with no accepted reason: executeParallel's wrapper matches nothing.
		"a transient fault inside a parallel branch": {&transient, branchWrapperReason, true},
		// Unseen prose: the class still decides.
		"a classified fault under prose no arm knows": {&transient, "Something upstream re-worded this.", true},
		// No prose at all: the class is still a verdict.
		"a classified fault with no reason at all": {&transient, "", true},

		// A class match, not presence.
		"a permanent fault carrying a detail": {
			&pauseDetail{Class: "permanent", Code: "ENOTFOUND"},
			branchWaitReason, false,
		},
		"a detail with an empty class": {
			&pauseDetail{Code: "EAI_AGAIN"},
			branchWaitReason, false,
		},
		"a detail whose class is a prefix of the transient one": {
			&pauseDetail{Class: "transient", Code: "EAI_AGAIN"},
			branchWaitReason, false,
		},
		// A parallel need-input park: the wrapper without detail stays false (run_ask.go's signal handles it).
		"a need-input park inside a parallel branch": {nil, branchWaitReason, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resumablePause(tc.reason, tc.detail); got != tc.want {
				t.Errorf("resumablePause(%q, %+v) = %v, want %v",
					tc.reason, tc.detail, got, tc.want)
			}
		})
	}
}

// TestResumablePause_IsStrictlyWiderThanTheCancelPredicate pins that the cancel predicate fires only for a dead owner,
// the resume one for any involuntary stop. Widening cancel destroys work; narrowing resume stranded runs.
func TestResumablePause_IsStrictlyWiderThanTheCancelPredicate(t *testing.T) {
	// Both predicates over the same fixture.
	both := func(t *testing.T, reply json.RawMessage) (cancel, resume bool) {
		t.Helper()
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
		return h.runs.restartPaused(t.Context(), "wf_1"), h.runs.involuntarilyPaused(t.Context(), "wf_1")
	}

	t.Run("everything the cancel side accepts, the resume side accepts too", func(t *testing.T) {
		// Otherwise a restart-paused run would be cancellable but never resumable.
		cancel, resume := both(t, inspectPaused(t, "wf_1", stalePauseReason))
		if !cancel || !resume {
			t.Errorf("restartPaused = %v, involuntarilyPaused = %v for KAS's restart literal; want both true",
				cancel, resume)
		}
	})

	for name, reason := range map[string]string{
		"an interrupted step":      interruptedPauseReason,
		"a transient model 5xx":    modelServicePauseReason,
		"a transient network code": "Transient connection error (EAI_AGAIN); the run is paused and can be resumed.",
	} {
		t.Run(name+" is resumable but never cancellable", func(t *testing.T) {
			cancel, resume := both(t, inspectPaused(t, "wf_1", reason))
			if cancel {
				t.Errorf("restartPaused = true for %q; widening the CANCEL side destroys work "+
					"a resume would have saved (see clearOrphaned)", reason)
			}
			if !resume {
				t.Errorf("involuntarilyPaused = false for %q; this is the class that stranded "+
					"six live runs", reason)
			}
		})
	}

	// The detail licenses a resume, never a cancel; restartPaused takes no detail by construction.
	t.Run("a classified transient fault is resumable and never cancellable", func(t *testing.T) {
		cancel, resume := both(t, inspectPausedWithDetail(t, "wf_1", branchWrapperReason, transientDetail()))
		if cancel {
			t.Error("restartPaused = true for a pause carrying only a transient-error DETAIL; " +
				"the cancel side reads KAS's restart literal and nothing else, and widening it " +
				"cancels work a resume would have saved")
		}
		if !resume {
			t.Error("involuntarilyPaused = false for a transient fault inside a parallel branch; " +
				"this is the run the whole detail arm exists for")
		}
	})

	// The detail-less wrapper: neither predicate may touch it.
	t.Run("the branch wrapper with no detail is neither resumable nor cancellable", func(t *testing.T) {
		cancel, resume := both(t, inspectPaused(t, "wf_1", branchWaitReason))
		if cancel || resume {
			t.Errorf("restartPaused = %v, involuntarilyPaused = %v for a detail-less parallel "+
				"wrapper; want both false, because that one sentence covers a need-input park, "+
				"an interruption and a permanent failure alike", cancel, resume)
		}
	})
}

// TestInvoluntarilyPaused_KeepsItsSiblingsThreeConditions pins that only the reason predicate is wider; status is
// re-read since a reason outlives its pause.
func TestInvoluntarilyPaused_KeepsItsSiblingsThreeConditions(t *testing.T) {
	transient := "Transient connection error (EAI_AGAIN); the run is paused and can be resumed."

	t.Run("an involuntary pause on this paused run is resumable", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: inspectPaused(t, "wf_1", transient),
		}
		if !h.runs.involuntarilyPaused(t.Context(), "wf_1") {
			t.Error("involuntarilyPaused = false for a transient-network pause on this very run")
		}
	})

	for name, reply := range map[string]json.RawMessage{
		"a reply naming a DIFFERENT run": inspectPaused(t, "wf_other", transient),
		"a run KAS says is running":      inspectReply(t, "wf_1", "running", transient),
		"a run KAS says completed":       inspectReply(t, "wf_1", "completed", transient),
		"a reply carrying no status":     inspectReply(t, "wf_1", "", transient),
	} {
		t.Run(name+" is not resumable", func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
			if h.runs.involuntarilyPaused(t.Context(), "wf_1") {
				t.Errorf("involuntarilyPaused = true for %s", name)
			}
		})
	}

	t.Run("a failed inspect leaves the run paused", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
		if h.runs.involuntarilyPaused(t.Context(), "wf_1") {
			t.Error("an unreadable pause reason was treated as resumable")
		}
	})

	t.Run("an empty workflow id is not resumable", func(t *testing.T) {
		h, _, _ := newTestHub()
		if h.runs.involuntarilyPaused(t.Context(), "") {
			t.Error("involuntarilyPaused(\"\") = true")
		}
	})
}

// TestReleaseIfOver_ReleasesTheLeaseOfARunThatStoppedWithoutAFrame pins that a cancel of a run with no in-flight node
// sends no `run_complete`; a real run stayed on /api/runs/live 27 hours after reaching `aborted`.
func TestReleaseIfOver_ReleasesTheLeaseOfARunThatStoppedWithoutAFrame(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	leased(t, h.runs, "wf_1")

	h.runs.releaseIfOver(t.Context(), "wf_1")

	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the lease outlived a run KAS reports as aborted, so /api/runs/live " +
			"keeps advertising it and its chat can never be evicted")
	}
}

// TestReleaseIfOver_RefusesEveryReplyThatDoesNotSayTheRunIsOver pins that releasing a live run's lease unbounds it,
// silences the unattended floor and strands its blocking row.
func TestReleaseIfOver_RefusesEveryReplyThatDoesNotSayTheRunIsOver(t *testing.T) {
	for name, reply := range map[string]json.RawMessage{
		"a reply naming a DIFFERENT run": inspectReply(t, "wf_other", "aborted", ""),
		"a run KAS says is running":      inspectReply(t, "wf_1", "running", ""),
		"a run KAS says is paused":       inspectPaused(t, "wf_1", stalePauseReason),
		"a reply carrying no status":     inspectReply(t, "wf_1", "", ""),
	} {
		t.Run(name+" keeps the lease", func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
			leased(t, h.runs, "wf_1")

			h.runs.releaseIfOver(t.Context(), "wf_1")

			if _, held := h.runs.lease("wf_1"); !held {
				t.Errorf("the lease was released for %s", name)
			}
		})
	}
}

// TestReleaseIfOver_LeavesARunItCouldNotReadAlone pins that inspect reports an unknown run as an error like a dead
// bridge's, so it keeps the lease, the cheaper mistake.
func TestReleaseIfOver_LeavesARunItCouldNotReadAlone(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
	leased(t, h.runs, "wf_1")

	h.runs.releaseIfOver(t.Context(), "wf_1")

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("an unreadable inspect released the lease; a failed RPC must never " +
			"be read as a terminal run")
	}
}

// TestReleaseIfOver_AsksNothingWhenThereIsNoLeaseToRelease pins that the early return before the RPC.
func TestReleaseIfOver_AsksNothingWhenThereIsNoLeaseToRelease(t *testing.T) {
	for name, id := range map[string]string{
		"a run whose lease is already gone": "wf_1",
		"an empty workflow id":              "",
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: inspectReply(t, id, "aborted", ""),
			}

			h.runs.releaseIfOver(t.Context(), id)

			if slices.Contains(br.callLog(), methodKiroWorkflowInspect) {
				t.Errorf("%s put an inspect on the wire; the reconcile costs one RPC per "+
					"DELIBERATE stop and none on any other path", name)
			}
		})
	}
}

// TestSweepOrphaned_ReportsWhetherItReachedKAS so the composition root can retry when kiro-cli was still
// installing. An empty store reports reached: a retry finds the same.
func TestSweepOrphaned_ReportsWhetherItReachedKAS(t *testing.T) {
	t.Run("a run list that answered", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_1", "name": "nightly", "status": "running",
			}),
		}
		leased(t, h.runs, "wf_1")

		if !h.runs.SweepOrphaned(t.Context()) {
			t.Error("a sweep that read the run list reported unreached, so the caller " +
				"pays for a retry it does not need")
		}
	})

	t.Run("a run list the utility bridge could not serve", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{
			methodKiroWorkflowList: errors.New("utility bridge start: kiro-cli is not available yet"),
		}
		leased(t, h.runs, "wf_1")

		if h.runs.SweepOrphaned(t.Context()) {
			t.Error("a sweep that never read the run list reported reached; this is the " +
				"one failure the caller retries, and it would be silently dropped")
		}
		if _, held := h.runs.lease("wf_1"); !held {
			t.Error("the sweep touched a lease without reading the run list")
		}
	})

	t.Run("no leases at all", func(t *testing.T) {
		h, _, _ := newTestHub()
		if !h.runs.SweepOrphaned(t.Context()) {
			t.Error("an empty lease store reported unreached, so every boot with no runs " +
				"parks a goroutine waiting for an install it has no use for")
		}
	})
}
