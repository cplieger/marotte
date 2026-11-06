package trigger

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// The retry and outcome lines of the design's test table, run by TestAttemptTable.

// retriedLive retries wf_1 and checks A35's answer: not handed back, the event to re-lease the run for, no write.
func retriedLive(tc *testConfirmer, r *launchrule.Rule) {
	tc.t.Helper()
	tc.written()
	ans, err := tc.Retried("wf_1")
	want := RetriedEvent{RuleID: r.ID, EventKey: "k1", CoalesceKey: "c1", Label: "triggered"}
	if err != nil || ans.HandedBack || ans.Event == nil || *ans.Event != want {
		tc.t.Errorf("Retried = %+v, %v; want the live attempt's event %+v, not handed back", ans, err, want)
	}
	if n := tc.written(); n != 0 {
		tc.t.Errorf("%d writes at the retry, want none", n)
	}
}

// retriedAt delivers retried d after the setup, then moves the clock on by after.
func retriedAt(d, after time.Duration) func(*testConfirmer, *launchrule.Rule) {
	return func(tc *testConfirmer, r *launchrule.Rule) {
		advance(d)
		retriedLive(tc, r)
		advance(after)
	}
}

// boundRestarted checks A35's bound: nothing cancelled yet, the record still live, and A6 at the restarted bound.
func boundRestarted(want attemptState, left time.Duration) func(*testing.T, *testConfirmer, *launchrule.Rule) {
	return func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
		if got := tc.cancels(); len(got) != 0 {
			t.Errorf("cancels = %v, want none before the restarted bound", got)
		}
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != want {
			t.Fatalf("record = %+v, want %v", rec, want)
		}
		advance(left)
		if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
			t.Errorf("cancels at the restarted bound = %v, want one for wf_1", got)
		}
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeBound {
			t.Errorf("record = %+v, want abandoned at its bound", rec)
		}
	}
}

// fileAttempts is the workflow ids of the rule's attempts on disk.
func fileAttempts(tc *testConfirmer, ruleID string) []string {
	_, attempts := tc.file(ruleID)
	var out []string
	for _, a := range attempts {
		out = append(out, a.WorkflowID)
	}
	return out
}

var retryCases = []attemptCase{
	{
		name:    "23 invoked-unconfirmed, retried 4 min 59 s after A1, run_start 1 min later: A35 then A3",
		setup:   unconfirmed,
		deliver: retriedAt(launchConfirmBound-time.Second, time.Minute),
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if got := tc.cancels(); len(got) != 0 {
				t.Errorf("cancels = %v, want none: the bound restarted from the retry", got)
			}
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
			confirmed(t, tc, r)
		},
	},
	{
		name:    "23 invoked-unconfirmed, retried 4 min 59 s after A1, no run_start: A6 5 minutes after the retry",
		setup:   unconfirmed,
		deliver: retriedAt(launchConfirmBound-time.Second, launchConfirmBound-time.Second),
		check:   boundRestarted(stateInvokedUnconfirmed, 2*time.Second),
	},
	{
		name:    "23 invoking, retried: A35 restarts the bound",
		setup:   invoking,
		deliver: retriedAt(4*time.Minute, 2*time.Minute),
		check:   boundRestarted(stateInvoking, 3*time.Minute+time.Second),
	},
	{
		name:  "23 boot-held before engine ready, retried: A35 leaves it waiting for A7",
		setup: bootedHeld,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			retriedLive(tc, r)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateBootHeld || rec.bound != nil {
				t.Errorf("record = %+v, want boot-held with no bound until engine ready", rec)
			}
			if !tc.BootHeld("wf_1") {
				t.Error("BootHeld(wf_1) = false after the retry, want it still held")
			}
		},
	},
	{
		name: "23 boot-held after engine ready, retried: A35 restarts the bound A7 armed",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			bootedHeld(tc, r)
			tc.run()
			tc.engineReady()
			advance(4 * time.Minute)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			retriedLive(tc, r)
			advance(2 * time.Minute)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if cancels, _ := tc.fx.calls(); len(cancels) != 0 {
				t.Errorf("cancels = %v, want none before the restarted bound", cancels)
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateBootHeld {
				t.Fatalf("record = %+v, want still boot-held", rec)
			}
			advance(3*time.Minute + time.Second)
			if cancels, _ := tc.fx.calls(); !slices.Equal(cancels, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want A8's one at the restarted bound", cancels)
			}
		},
	},
	{
		name: "39 none, run_start on a deleted rule's lease, then an outcome: A23",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
			tc.written()
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
			tc.RecordOutcome(r.ID, "wf_1", launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "denied"})
			tc.RecordEnding(r.ID, "wf_1", launchrule.Outcome{Status: launchrule.StatusCompleted})
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertNoStateFile(t, tc, r.ID)
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes, want none for a deleted rule", n)
			}
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the deleted rule's value is kept in memory")
			}
		},
	},
	{
		name:  "70 abandoned-cancelled, retried, its write fails, then a restart: A33 not committed, W2, A30",
		setup: abandonedCancelled,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.failWrites(errDiskFull)
			ans, err := tc.Retried("wf_1")
			if !errors.Is(err, errDiskFull) || ans.HandedBack || ans.Event != nil {
				tc.t.Errorf("Retried = %+v, %v; want not handed back, the write's error", ans, err)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelled {
				t.Errorf("record = %+v, want still abandoned-cancelled in memory", rec)
			}
			if got := fileAttempts(tc, r.ID); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("file attempts = %v, want wf_1 still on disk", got)
			}
			if got := tc.writeBlock(r.ID); got != writeBlockedText(r) {
				t.Errorf("write block = %q, want %q", got, writeBlockedText(r))
			}
			tc.restart()
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelled {
				t.Errorf("record after the restart = %+v, want abandoned-cancelled (A30)", rec)
			}
			if got := tc.cancels(); len(got) != 0 {
				t.Errorf("cancels after the restart = %v, want none: its cancel had landed", got)
			}
		},
	},
	{
		name: "76 abandoned, its key queued, retried lands, stop before releaseLease, then boot: A31, then A33",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			abandonedCancelled(tc, r)
			tc.lease(r.ID, "k1", "wf_1")
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			ans, err := tc.Retried("wf_1")
			if err != nil || !ans.HandedBack || ans.Event != nil {
				tc.t.Errorf("Retried = %+v, %v; want handed back", ans, err)
			}
			if got := fileAttempts(tc, r.ID); len(got) != 0 {
				tc.t.Errorf("file attempts = %v, want none: the hand-back's write landed", got)
			}
			tc.restart()
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeBootStale {
				t.Errorf("record = %+v, want abandoned-cancelling, boot_stale (A31)", rec)
			}
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one on the ended run", got)
			}
			if ans, err := tc.Retried("wf_1"); err != nil || !ans.HandedBack {
				t.Errorf("a later Retried = %+v, %v; want handed back (A33)", ans, err)
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec != nil {
				t.Errorf("record = %+v, want none after A33", rec)
			}
		},
	},
}
