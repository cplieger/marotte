package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// scheduledHub is a hub with one daily schedule in a real store and a run bridge whose cancel lands.
func scheduledHub(t *testing.T) (*Runtime, *fakeBridge, *schedule.Store, schedule.Entry) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	entry := schedule.Entry{
		Source: "bundled://nightly", Enabled: true,
		Spec: schedule.Spec{Freq: schedule.FreqDaily, Hour: 2},
	}
	if pErr := st.Put(t.Context(), &entry); pErr != nil {
		t.Fatalf("Put schedule: %v", pErr)
	}
	h.runs.schedules = st
	return h, br, st, entry
}

// launchScheduled leases workflowID as the schedule's fire, bounded by a slot an hour out.
func launchScheduled(t *testing.T, h *Runtime, br *fakeBridge, entry *schedule.Entry, workflowID string) {
	t.Helper()
	h.bridge.mgr.insert(runChatID(workflowID), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.grantLease(t.Context(), workflowID, "nightly",
		scheduledLaunch(entry.ID(), time.Now().Add(time.Hour)), hostSec())
}

// A launch refused by the single-run rule names the live run and is not a hold; the manual route still answers 409
// with the sentence.
func TestLaunch_ARecipeBusyRefusalNamesTheLiveRun(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_new")
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{
		"workflowId": "wf_live", "name": "publish", "status": "running",
	})

	_, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", "sched-1",
		marotte.RunDefaults{}, time.Now().Add(time.Hour))
	var busy *schedule.BusyError
	if !errors.As(err, &busy) || busy.WorkflowID != "wf_live" {
		t.Fatalf("LaunchScheduled = %v, want a busy refusal naming wf_live", err)
	}
	if errors.Is(err, schedule.ErrNoRunCanStart) {
		t.Errorf("the busy refusal wraps ErrNoRunCanStart, so the runner would hold the slot instead of skipping it")
	}

	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
	rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
	if rec.Code != http.StatusConflict || errorText(t, rec) != "this recipe already has a live run" {
		t.Errorf("POST /api/runs on a busy recipe = %d %s, want 409 with the single-run sentence", rec.Code, rec.Body.String())
	}
}

// The busy refusal carries the blocking run's lease, which is what tells the runner whether this schedule's bound ends
// that run at the slot. A person's retry reuses the scheduled run's workflow id under a manual lease, which no
// schedule bounds, so its refusal names no schedule.
func TestLaunch_ARecipeBusyRefusalCarriesTheLiveRunsLease(t *testing.T) {
	busyHub := func(t *testing.T) (*Runtime, *schedule.Store, schedule.Entry) {
		t.Helper()
		h, br, st, entry := scheduledHub(t)
		br.callResults[methodKiroWorkflowListRecipes] = json.RawMessage(
			`{"recipes":[{"name":"nightly","source":"bundled://nightly"}]}`,
		)
		br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "nightly", "status": "running",
		})
		launchScheduled(t, h, br, &entry, "wf_1")
		return h, st, entry
	}
	refusal := func(t *testing.T, h *Runtime, entry *schedule.Entry) *schedule.BusyError {
		t.Helper()
		_, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.ID(),
			marotte.RunDefaults{}, time.Now().Add(25*time.Hour))
		busy, ok := errors.AsType[*schedule.BusyError](err)
		if !ok || busy.WorkflowID != "wf_1" {
			t.Fatalf("LaunchScheduled = %v, want a busy refusal naming wf_1", err)
		}
		return busy
	}

	t.Run("the schedule's own run", func(t *testing.T) {
		h, _, entry := busyHub(t)
		l, _ := h.runs.lease("wf_1")
		if busy := refusal(t, h, &entry); busy.ScheduleID != entry.ID() || !busy.SlotAt.Equal(l.SlotAt) {
			t.Errorf("the refusal carries (%q, %v), want this schedule %q and its run's slot %v",
				busy.ScheduleID, busy.SlotAt, entry.ID(), l.SlotAt)
		}
	})

	t.Run("a person's retry of it", func(t *testing.T) {
		h, _, entry := busyHub(t)
		h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "failed",
		}))
		h.runs.grantRetryLease(t.Context(), "wf_1", &runOrigin{recipe: "nightly"})
		if busy := refusal(t, h, &entry); busy.ScheduleID != "" || !busy.SlotAt.IsZero() {
			t.Errorf("the retried run's refusal carries (%q, %v), want no schedule and no slot", busy.ScheduleID, busy.SlotAt)
		}
	})
}

// A person's Retry is a run of their own even when marotte missed the scheduled run's terminal frame and still holds
// its lease: the retried run is not the schedule's (no row, no slot, not unattended), and the row keeps how the
// scheduled run ended.
func TestRetry_AScheduledRunWhoseEndWasMissedIsRetriedAsThePersons(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	launchScheduled(t, h, br, &entry, id)
	h.runs.recordParentSession(t.Context(), id, testLaunchSession)
	h.runs.armDeadline(t.Context(), id)
	br.callResults[methodKiroWorkflowRetry] = retryReply(t, id, "running", "build")
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{
		"workflowId": id, "name": "nightly", "status": "failed", "parentSessionId": testLaunchSession,
	})

	if _, err := h.runs.retry(t.Context(), id, h.runs.affordance(id, &runHead{Status: "failed"})); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	l, held := h.runs.lease(id)
	if !held || l.Origin != runlease.OriginManual || l.ScheduleID != "" || l.Unattended || !l.SlotAt.IsZero() {
		t.Errorf("the retried run's lease = %+v held %v, want the person's: manual, attended, no schedule, no slot", l, held)
	}
	if row := onlyRow(t, st); row.LastStatus != schedule.StatusFailed || row.LastReason != "the run failed" {
		t.Errorf("the row reads (%q, %q), want (failed, the run failed): the scheduled run's own ending",
			row.LastStatus, row.LastReason)
	}
}

// awaitEndedRow polls st until its one row no longer reads started: a stop stamps its end reason inside the cancel,
// before its caller writes the row.
func awaitEndedRow(t *testing.T, st *schedule.Store) schedule.Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	row := onlyRow(t, st)
	for row.LastStatus == "started" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		row = onlyRow(t, st)
	}
	return row
}

func onlyRow(t *testing.T, st *schedule.Store) schedule.Entry {
	t.Helper()
	rows := st.List()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows, want 1", len(rows))
	}
	return rows[0]
}

// TestScheduledRun_TheRowRecordsHowItsRunEnded pins that a scheduled run's terminal `run_complete` lands on its
// schedule row; without it the row reads `started` forever after a run that completed or failed.
func TestScheduledRun_TheRowRecordsHowItsRunEnded(t *testing.T) {
	for name, tc := range map[string]struct {
		status, stopReason string
		wantStatus         string
		wantReason         string
	}{
		"completed":                {"completed", "", "completed", ""},
		"failed with KAS's reason": {"failed", "step build failed: exit 2", "failed", "step build failed: exit 2"},
		"failed with no reason":    {"failed", "", "failed", "the run failed"},
		"failed with a reason on two lines": {
			"failed", "step build failed:\nexit \x1b[31m2\x1b[0m", "failed", "step build failed: exit 2",
		},
		"aborted":   {"aborted", "", "failed", "the run was aborted"},
		"cancelled": {"cancelled", "", "failed", "the run was cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			const id = "wf_1"
			launchScheduled(t, h, br, &entry, id)

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": id, "status": tc.status,
				"finalState": map[string]any{"stopReason": tc.stopReason},
			}))

			row := onlyRow(t, st)
			if string(row.LastStatus) != tc.wantStatus || row.LastReason != tc.wantReason {
				t.Errorf("after a %s run_complete the row reads (%q, %q), want (%q, %q)",
					tc.status, row.LastStatus, row.LastReason, tc.wantStatus, tc.wantReason)
			}
		})
	}
}

// TestScheduledRun_ALongStopReasonIsCappedOnTheRow pins that KAS's stop reason, persisted on the row at every fire and
// shown on one line, is bounded.
func TestScheduledRun_ALongStopReasonIsCappedOnTheRow(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	launchScheduled(t, h, br, &entry, id)

	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": id, "status": "failed",
		"finalState": map[string]any{"stopReason": "step build failed: " + strings.Repeat("x", 4096)},
	}))

	row := onlyRow(t, st)
	if len(row.LastReason) > maxRowTextBytes || !strings.HasPrefix(row.LastReason, "step build failed: x") ||
		!strings.HasSuffix(row.LastReason, "...") {
		t.Errorf("a 4 KB stop reason reads %d bytes on the row (%.40q...), want at most %d, marked as cut",
			len(row.LastReason), row.LastReason, maxRowTextBytes)
	}
}

// TestScheduledRun_TheRowReadsStartedFromItsLease pins that a run's row says started from the moment its lease is
// granted, before invoke: a run that ends before the launch returns would otherwise leave no ending to fill.
func TestScheduledRun_TheRowReadsStartedFromItsLease(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	launchScheduled(t, h, br, &entry, "wf_1")

	if row := onlyRow(t, st); string(row.LastStatus) != "started" {
		t.Errorf("a granted scheduled lease left the row reading %q, want started", row.LastStatus)
	}
}

// TestScheduledRun_ALateEndingNeverLandsOnANewerFire pins that an older run's terminal frame, arriving after the
// schedule fired again, leaves the newer run's row alone.
func TestScheduledRun_ALateEndingNeverLandsOnANewerFire(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	launchScheduled(t, h, br, &entry, "wf_old")
	launchScheduled(t, h, br, &entry, "wf_new")

	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_old", "status": "failed",
	}))

	if row := onlyRow(t, st); string(row.LastStatus) != "started" {
		t.Errorf("wf_old's ending rewrote the row wf_new owns to (%q, %q); want it still started",
			row.LastStatus, row.LastReason)
	}
}

// TestScheduledRun_ABoundStopSaysWhyOnTheRow pins that a stall or the backstop writes its reason on the row, and
// the cancel's own terminal frame, which carries none, does not overwrite it.
func TestScheduledRun_ABoundStopSaysWhyOnTheRow(t *testing.T) {
	for name, tc := range map[string]struct {
		mark       func(h *Runtime, id string)
		wantReason string
	}{
		"stalled": {func(*Runtime, string) {}, "15 minutes"},
		"past the backstop": {func(h *Runtime, id string) {
			h.runs.mu.Lock()
			if h.runs.bounds.executed == nil {
				h.runs.bounds.executed = map[string]time.Duration{}
			}
			h.runs.bounds.executed[id] = runBackstop
			h.runs.mu.Unlock()
		}, "36 hours"},
	} {
		t.Run(name, func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			const id = "wf_1"
			launchScheduled(t, h, br, &entry, id)
			h.runs.armDeadline(t.Context(), id)
			tc.mark(h, id)
			l, _ := h.runs.lease(id)

			h.runs.cancelExpired(id, l.Deadline)
			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": id, "status": "cancelled",
			}))

			row := onlyRow(t, st)
			if row.LastStatus != schedule.StatusFailed || !strings.Contains(row.LastReason, tc.wantReason) {
				t.Errorf("a run stopped %s left the row reading (%q, %q), want failed naming %q",
					name, row.LastStatus, row.LastReason, tc.wantReason)
			}
		})
	}
}

// TestScheduledRun_ABoundStopRecordsOnlyOnceItsCancelLands pins that a refused cancel leaves the row reading
// started while the run still runs, and the retry that lands writes the bound's reason.
func TestScheduledRun_ABoundStopRecordsOnlyOnceItsCancelLands(t *testing.T) {
	for name, tc := range map[string]struct {
		slotIn     time.Duration
		backstop   bool
		wantReason string
	}{
		"stalled":           {time.Hour, false, "15 minutes"},
		"past the backstop": {time.Hour, true, "36 hours"},
		"at its slot":       {-time.Minute, false, reasonOverran},
	} {
		expire := func(t *testing.T, h *Runtime, br *fakeBridge, entry *schedule.Entry) {
			t.Helper()
			const id = "wf_1"
			leaseScheduledWithSlot(t, h, br, entry, id, tc.slotIn)
			h.runs.armDeadline(t.Context(), id)
			if tc.backstop {
				h.runs.mu.Lock()
				h.runs.bounds.executed = map[string]time.Duration{id: runBackstop}
				h.runs.mu.Unlock()
			}
			l, _ := h.runs.lease(id)
			h.runs.cancelExpired(id, l.Deadline)
		}
		t.Run(name+" refused", func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
			setCancelRetryDelay(h.runs, time.Hour)

			expire(t, h, br, &entry)

			if row := onlyRow(t, st); string(row.LastStatus) != "started" {
				t.Errorf("a refused cancel left the row reading (%q, %q), want started: the run still runs",
					row.LastStatus, row.LastReason)
			}
		})
		t.Run(name+" refused once", func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			setCancelRetryDelay(h.runs, time.Millisecond)
			refuseCancelOnce(br)

			expire(t, h, br, &entry)

			awaitCalls(t, br, methodKiroWorkflowCancel, 2, "the refused cancel was not retried")
			row := awaitEndedRow(t, st)
			if row.LastStatus != schedule.StatusFailed || !strings.Contains(row.LastReason, tc.wantReason) {
				t.Errorf("the retried cancel left the row reading (%q, %q), want failed naming %q",
					row.LastStatus, row.LastReason, tc.wantReason)
			}
		})
	}
}

// TestScheduledRun_ABoundStopWhoseCancelIsNeverAcceptedSaysSo pins that a bound stop whose every cancel kiro-cli
// refuses leaves the row saying the run may still be running, instead of `started` while every later slot is
// skipped. A person's own cancel is theirs to see on the run page and leaves the row alone.
func TestScheduledRun_ABoundStopWhoseCancelIsNeverAcceptedSaysSo(t *testing.T) {
	for name, tc := range map[string]struct {
		slotIn   time.Duration
		backstop bool
	}{
		"stalled":           {time.Hour, false},
		"past the backstop": {time.Hour, true},
		"at its slot":       {-time.Minute, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
			setCancelRetryDelay(h.runs, time.Millisecond)
			const id = "wf_1"
			leaseScheduledWithSlot(t, h, br, &entry, id, tc.slotIn)
			h.runs.armDeadline(t.Context(), id)
			if tc.backstop {
				h.runs.mu.Lock()
				h.runs.bounds.executed = map[string]time.Duration{id: runBackstop}
				h.runs.mu.Unlock()
			}
			l, _ := h.runs.lease(id)

			h.runs.cancelExpired(id, l.Deadline)

			awaitCalls(t, br, methodKiroWorkflowCancel, 1+maxCancelRetries, "the refused cancel's ladder did not run")
			row := awaitEndedRow(t, st)
			if row.LastStatus != schedule.StatusFailed || !strings.Contains(row.LastReason, "may still be running") {
				t.Errorf("after every cancel was refused the row reads (%q, %q), want failed saying the run may "+
					"still be running", row.LastStatus, row.LastReason)
			}
		})
	}
	t.Run("a person's cancel", func(t *testing.T) {
		logs := captureLogs(t)
		h, br, st, entry := scheduledHub(t)
		br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
		setCancelRetryDelay(h.runs, time.Millisecond)
		const id = "wf_1"
		launchScheduled(t, h, br, &entry, id)
		h.runs.armDeadline(t.Context(), id)

		if err := h.runs.cancel(t.Context(), id, callerPage); err == nil {
			t.Fatal("Cancel reported success for a refused cancel")
		}

		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(logs.String(), logMsgCancelUnretried) {
			if time.Now().After(deadline) {
				t.Fatalf("the spent ladder never logged %q", logMsgCancelUnretried)
			}
			time.Sleep(time.Millisecond)
		}
		if row := onlyRow(t, st); string(row.LastStatus) != "started" {
			t.Errorf("a person's refused cancel left the row reading (%q, %q), want started",
				row.LastStatus, row.LastReason)
		}
	})
}

// TestScheduledRun_AReleaseWithoutAFrameRecordsTheEnding pins both paths that release a terminal run's lease with no
// `run_complete` (a cancel with no node in flight, a run list read): each writes the run's ending on the row.
func TestScheduledRun_AReleaseWithoutAFrameRecordsTheEnding(t *testing.T) {
	t.Run("releaseIfOver", func(t *testing.T) {
		h, br, st, entry := scheduledHub(t)
		const id = "wf_1"
		br.callResults[methodKiroWorkflowInspect] = inspectReply(t, id, marotte.RunStatusAborted, "")
		launchScheduled(t, h, br, &entry, id)

		h.runs.releaseIfOver(t.Context(), id)

		if row := onlyRow(t, st); row.LastStatus != schedule.StatusFailed || row.LastReason != "the run was aborted" {
			t.Errorf("the row reads (%q, %q), want (failed, the run was aborted)", row.LastStatus, row.LastReason)
		}
	})
	t.Run("reconcileLeasePresence", func(t *testing.T) {
		h, br, st, entry := scheduledHub(t)
		const id = "wf_1"
		launchScheduled(t, h, br, &entry, id)

		h.runs.reconcileLeasePresence(t.Context(),
			map[string]marotte.RunStatus{id: marotte.RunStatusCompleted}, time.Now(), false)

		if row := onlyRow(t, st); string(row.LastStatus) != "completed" {
			t.Errorf("the row reads (%q, %q), want completed", row.LastStatus, row.LastReason)
		}
	})
}
