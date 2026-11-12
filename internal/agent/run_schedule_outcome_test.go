package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// scheduledHub is a hub with one daily schedule in a real store and a run bridge whose cancel lands.
func scheduledHub(t *testing.T) (*Runtime, *fakeBridge, *launchrule.Store, launchrule.Rule) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	entry := putClockRule(t, st, "bundled://nightly", launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, true)
	h.runs.launchRules = st
	return h, br, st, entry
}

// launchScheduled leases workflowID as the schedule's fire, bounded by a slot an hour out.
func launchScheduled(t *testing.T, h *Runtime, br *fakeBridge, entry *launchrule.Rule, workflowID string) {
	t.Helper()
	h.insertBridgeNow(runChatID(workflowID), testBridge(br, bridgeReady))
	h.runs.grantLease(t.Context(), workflowID, "nightly",
		scheduledLaunch(entry.Row(), time.Now().Add(time.Hour)), hostSec())
}

// A launch refused by the single-run rule names the live run and is not a hold; the manual route still answers 409
// with the sentence.
func TestLaunch_ARecipeBusyRefusalNamesTheLiveRun(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_new")
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{
		"workflowId": "wf_live", "name": "publish", "status": "running",
	})

	_, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
		nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
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
	busyHub := func(t *testing.T) (*Runtime, *launchrule.Store, launchrule.Rule) {
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
	refusal := func(t *testing.T, h *Runtime, entry *launchrule.Rule) *schedule.BusyError {
		t.Helper()
		_, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(),
			nil, marotte.RunDefaults{}, time.Now().Add(25*time.Hour))
		busy, ok := errors.AsType[*schedule.BusyError](err)
		if !ok || busy.WorkflowID != "wf_1" {
			t.Fatalf("LaunchScheduled = %v, want a busy refusal naming wf_1", err)
		}
		return busy
	}

	t.Run("the schedule's own run", func(t *testing.T) {
		h, _, entry := busyHub(t)
		l, _ := h.runs.lease("wf_1")
		if busy := refusal(t, h, &entry); busy.ScheduleID != entry.ID || !busy.SlotAt.Equal(l.SlotAt) {
			t.Errorf("the refusal carries (%q, %v), want this schedule %q and its run's slot %v",
				busy.ScheduleID, busy.SlotAt, entry.ID, l.SlotAt)
		}
	})

	t.Run("a person's retry of it", func(t *testing.T) {
		h, _, entry := busyHub(t)
		h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "failed",
		}))
		h.runs.grantRetryLease(t.Context(), "wf_1", &runOrigin{recipe: "nightly"}, nil)
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
	if row := onlyRow(t, st); row.LastStatus != launchrule.StatusFailed || row.LastReason != "the run failed" {
		t.Errorf("the row reads (%q, %q), want (failed, the run failed): the scheduled run's own ending",
			row.LastStatus, row.LastReason)
	}
}

// awaitEndedRow polls st until its one row no longer reads started: a stop stamps its end reason inside the cancel,
// before its caller writes the row.
func awaitEndedRow(t *testing.T, st *launchrule.Store) launchrule.Clock {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	row := onlyRow(t, st)
	for row.LastStatus == "started" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		row = onlyRow(t, st)
	}
	return row
}

func onlyRow(t *testing.T, st *launchrule.Store) launchrule.Clock {
	t.Helper()
	rows := st.Clocks()
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
	if len(row.LastReason) > launchrule.MaxReasonBytes || !strings.HasPrefix(row.LastReason, "step build failed: x") ||
		!strings.HasSuffix(row.LastReason, "...") {
		t.Errorf("a 4 KB stop reason reads %d bytes on the row (%.40q...), want at most %d, marked as cut",
			len(row.LastReason), row.LastReason, launchrule.MaxReasonBytes)
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

// TestScheduledRun_AStallSaysWhyOnTheRow pins that a stall writes its reason on the row, and the cancel's own
// terminal frame, which carries none, does not overwrite it.
func TestScheduledRun_AStallSaysWhyOnTheRow(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	launchScheduled(t, h, br, &entry, id)
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)

	h.runs.cancelExpired(id, l.Deadline)
	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": id, "status": "cancelled",
	}))

	row := onlyRow(t, st)
	if row.LastStatus != launchrule.StatusFailed || !strings.Contains(row.LastReason, "15 minutes") {
		t.Errorf("a stalled run left the row reading (%q, %q), want failed naming 15 minutes",
			row.LastStatus, row.LastReason)
	}
}

// TestScheduledRun_ASilenceStopSaysWhyOnTheRow pins that a scheduled run stopped for an hour of silence on an open step
// says so on its row, while one with only a container open is the idle window's 15-minute stall.
func TestScheduledRun_ASilenceStopSaysWhyOnTheRow(t *testing.T) {
	for name, tc := range map[string]struct {
		arrange    func(r runTree)
		wantReason string
	}{
		"a silent step": {
			arrange:    func(r runTree) { r.start(stepNodeType, "a") },
			wantReason: "sent nothing for 60 minutes",
		},
		"only a container open": {
			arrange: func(r runTree) {
				r.start("repeat", "loop")
				r.start(stepNodeType, "loop", "iter-0", "a")
				r.complete("loop", "iter-0", "a")
			},
			wantReason: "15 minutes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			const id = "wf_1"
			launchScheduled(t, h, br, &entry, id)
			h.runs.log = newRunLog(t.TempDir())
			tc.arrange(runTree{t: t, h: h, id: id})
			h.runs.armDeadline(t.Context(), id)
			stageSilence(h.runs, id, runSilenceBound)
			l, _ := h.runs.lease(id)

			h.runs.cancelExpired(id, l.Deadline)

			row := onlyRow(t, st)
			if row.LastStatus != launchrule.StatusFailed || !strings.Contains(row.LastReason, tc.wantReason) {
				t.Errorf("the stopped scheduled run left the row reading (%q, %q), want failed naming %q",
					row.LastStatus, row.LastReason, tc.wantReason)
			}
		})
	}
}

// TestScheduledRun_ABoundStopRecordsOnlyOnceItsCancelLands pins that a refused cancel leaves the row reading
// started while the run still runs, and the retry that lands writes the bound's reason.
func TestScheduledRun_ABoundStopRecordsOnlyOnceItsCancelLands(t *testing.T) {
	for name, tc := range map[string]struct {
		slotIn     time.Duration
		wantReason string
	}{
		"stalled":     {time.Hour, "15 minutes"},
		"at its slot": {-time.Minute, reasonOverran},
	} {
		expire := func(t *testing.T, h *Runtime, br *fakeBridge, entry *launchrule.Rule) {
			t.Helper()
			const id = "wf_1"
			leaseScheduledWithSlot(t, h, br, entry, id, tc.slotIn)
			h.runs.armDeadline(t.Context(), id)
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
			if row.LastStatus != launchrule.StatusFailed || !strings.Contains(row.LastReason, tc.wantReason) {
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
		slotIn time.Duration
	}{
		"stalled":     {time.Hour},
		"at its slot": {-time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			h, br, st, entry := scheduledHub(t)
			br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
			setCancelRetryDelay(h.runs, time.Millisecond)
			const id = "wf_1"
			before := failureCounts(t)
			leaseScheduledWithSlot(t, h, br, &entry, id, tc.slotIn)
			h.runs.armDeadline(t.Context(), id)
			l, _ := h.runs.lease(id)

			h.runs.cancelExpired(id, l.Deadline)

			awaitCalls(t, br, methodKiroWorkflowCancel, 1+maxCancelRetries, "the refused cancel's ladder did not run")
			row := awaitEndedRow(t, st)
			if row.LastStatus != launchrule.StatusFailed || !strings.Contains(row.LastReason, "may still be running") {
				t.Errorf("after every cancel was refused the row reads (%q, %q), want failed saying the run may "+
					"still be running", row.LastStatus, row.LastReason)
			}
			// The bound's own stop never landed, so only the given-up cancel counts.
			wantFailureDeltas(t, before, map[string]float64{"scheduled/cancel_given_up": 1})
		})
	}
	t.Run("a person's cancel", func(t *testing.T) {
		logs := captureLogs(t)
		h, br, st, entry := scheduledHub(t)
		br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
		setCancelRetryDelay(h.runs, time.Millisecond)
		const id = "wf_1"
		before := failureCounts(t)
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
		wantFailureDeltas(t, before, nil)
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

		if row := onlyRow(t, st); row.LastStatus != launchrule.StatusFailed || row.LastReason != "the run was aborted" {
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

// triggeredHub is scheduledHub with a recording confirmer and one triggered run, wf_1, of rule-1 leased on a bridge
// whose cancel lands.
func triggeredHub(t *testing.T) (*Runtime, *fakeConfirmer, *launchrule.Store) {
	t.Helper()
	h, br, st, _ := scheduledHub(t)
	fc := &fakeConfirmer{}
	h.runs.confirmer = fc
	h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeReady))
	h.runs.grantLease(t.Context(), "wf_1", "nightly", eventLaunch(), hostSec())
	return h, fc, st
}

// assertClockRowUntouched fails when a triggered run's outcome reached the clock rule's row: an event rule's row is
// the confirmer's to write.
func assertClockRowUntouched(t *testing.T, st *launchrule.Store) {
	t.Helper()
	if row := onlyRow(t, st); row.LastRunID != "" || row.LastStatus != "" {
		t.Errorf("the clock rule's row reads (%q, %q, %q), want untouched by a triggered run",
			row.LastRunID, row.LastStatus, row.LastReason)
	}
}

// A triggered run's ending reaches its rule's row through the confirmer, which applies the rule's last_run_id
// guard; the ending is handed over as an ending, so it cannot overwrite a cause written first.
func TestTriggeredRunOutcomeOnRuleRow(t *testing.T) {
	h, fc, st := triggeredHub(t)

	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "failed", "finalState": map[string]any{"stopReason": "step build failed"},
	}))

	want := []string{"RecordEnding rule-1 wf_1 failed step build failed"}
	if got := fc.recorded(); !slices.Equal(got, want) {
		t.Errorf("confirmer calls = %q, want %q", got, want)
	}
	assertClockRowUntouched(t, st)
}

// Every writer hands a triggered run's outcome to the confirmer under the run's own workflow id, which the
// confirmer matches against the rule's last_run_id, so an abandoned attempt's run, which never confirmed, writes
// nothing on the row; the agent itself writes no row.
func TestAbandonedRunOutcomeReachesNoRow(t *testing.T) {
	t.Run("its cancel's terminal frame", func(t *testing.T) {
		h, fc, st := triggeredHub(t)
		h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "aborted",
		}))
		if got := fc.count("RecordEnding rule-1 wf_1 failed"); got != 1 {
			t.Errorf("RecordEnding for wf_1 called %d times, want 1; calls %q", got, fc.recorded())
		}
		assertClockRowUntouched(t, st)
	})
	t.Run("a stall", func(t *testing.T) {
		h, fc, st := triggeredHub(t)
		h.runs.armDeadline(t.Context(), "wf_1")
		l, _ := h.runs.lease("wf_1")
		h.runs.cancelExpired("wf_1", l.Deadline)
		h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": "wf_1", "status": "cancelled",
		}))
		var stall string
		for _, c := range fc.recorded() {
			if strings.HasPrefix(c, "RecordOutcome rule-1 wf_1 failed") && strings.Contains(c, "15 minutes") {
				stall = c
			}
		}
		if stall == "" {
			t.Errorf("no RecordOutcome naming the stall for wf_1; calls %q", fc.recorded())
		}
		if got := fc.count("RecordOutcome rule-1 wf_1 failed the run was cancelled"); got != 0 {
			t.Errorf("the cancel's own ending was sent as an outcome, which would overwrite the stall's reason")
		}
		assertClockRowUntouched(t, st)
	})
	t.Run("a release from the run list", func(t *testing.T) {
		h, fc, st := triggeredHub(t)
		h.runs.reconcileLeasePresence(t.Context(),
			map[string]marotte.RunStatus{"wf_1": marotte.RunStatusCancelled}, time.Now(), false)
		if got := fc.count("RecordEnding rule-1 wf_1 failed"); got != 1 {
			t.Errorf("RecordEnding for wf_1 called %d times, want 1; calls %q", got, fc.recorded())
		}
		assertClockRowUntouched(t, st)
	})
}
