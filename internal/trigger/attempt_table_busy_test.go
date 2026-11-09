package trigger

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// The busy lines of the design's test table, run by TestAttemptTable.

// errClaimed is a cancel KAS was not sent because another stop of the run held its claim.
var errClaimed = errors.New("another stop of this run is in progress")

// pollTick is a rule's poll interval at the store's default, the period of a busy launch's re-attempt.
const pollTick = 5 * time.Minute

// otherRule stores a second enabled rule, named nightly, on the same recipe and spec as the case's.
func otherRule(tc *testConfirmer) launchrule.Rule {
	tc.t.Helper()
	r, err := tc.store.Create(tc.t.Context(), &launchrule.Rule{
		Source: "bundled://weekly", Name: "nightly", Enabled: true, When: launchrule.When{Event: runSpec("success")},
	})
	if err != nil {
		tc.t.Fatalf("Setup: Create: %v", err)
	}
	return r
}

// manualLease is a person's lease on wf, as a manual launch or a retry grants it.
func (tc *testConfirmer) manualLease(wf string) {
	tc.t.Helper()
	l := runlease.Lease{WorkflowID: wf, Recipe: "weekly", Origin: runlease.OriginManual, StartedAt: time.Now()}
	if err := tc.leases.Put(tc.t.Context(), &l); err != nil {
		tc.t.Fatalf("Setup: lease %s: %v", wf, err)
	}
}

// blockerOf is the run the rule holds as its unnamed blocker, empty when none.
func (tc *testConfirmer) blockerOf(ruleID string) string {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return rv.blocker.workflowID
}

// warns is the count of WARN logMsgRunNotStarted lines with class=blocked so far.
func (tc *testConfirmer) warns() int { return tc.log.CountExact(logMsgRunNotStarted) }

// unchangedBy checks a busy's launching rule took nothing from it: nothing counted, no block, no blocker.
func unchangedBy(t *testing.T, tc *testConfirmer, ruleID string) {
	t.Helper()
	if _, st := tc.memory(ruleID); st.BoundAbandons != 0 || !st.FailingSince.IsZero() || st.Blocked != "" {
		t.Errorf("status %+v, want nothing counted and no block", st)
	}
	if b := tc.blockerOf(ruleID); b != "" {
		t.Errorf("blocker = %s, want none", b)
	}
}

// liveBusy is case 17 for one live state: A12 counts nothing, cancels nothing and writes nothing.
func liveBusy(name string, setup func(*testConfirmer, *launchrule.Rule), want attemptState) attemptCase {
	return attemptCase{
		name:  name,
		setup: setup,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.written()
			tc.busy(r.ID, "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, st := tc.memory(r.ID); st.RowText != "starting run wf_1" {
				t.Errorf("row text = %q, want A1's, unchanged", st.RowText)
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != want {
				t.Errorf("record = %+v, want it unchanged, %s", rec, want)
			}
			if got := tc.cancels(); len(got) != 0 {
				t.Errorf("cancels = %v, want none", got)
			}
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes, want none", n)
			}
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 || !st.FailingSince.IsZero() {
				t.Errorf("status %+v, want nothing counted", st)
			}
		},
	}
}

// refusedEveryTime is case 30 for one abandoned-cancelling record, set up by setup with its first cancel queued:
// that cancel is refused, then five busys each resend the cancel and count, and after 24 hours of them the rule
// is blocked.
func refusedEveryTime(name string, setup func(*testConfirmer, *launchrule.Rule)) attemptCase {
	return attemptCase{
		name:  name,
		setup: setup,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.answerCancels(errCancelRefused)
			for range 5 {
				tc.busy(r.ID, "wf_1")
				tc.answerCancels(errCancelRefused)
				advance(pollTick)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if ran, _ := tc.fx.calls(); len(ran) != 6 {
				t.Errorf("cancels run = %v, want the first and five resends", ran)
			}
			_, st := tc.memory(r.ID)
			if want := "run wf_1 was abandoned by this rule and its cancel has not landed; retrying"; st.RowText != want {
				t.Errorf("row text = %q, want %q", st.RowText, want)
			}
			if st.FailingSince.IsZero() || st.Blocked != "" {
				t.Fatalf("status %+v, want the clock started and no block yet", st)
			}
			before := tc.warns()
			advance(time.Until(st.FailingSince.Add(failingBlockAfter)))
			tc.busy(r.ID, "wf_1")
			tc.answerCancels(errCancelRefused)
			if _, st := tc.memory(r.ID); st.Blocked == "" {
				t.Errorf("status %+v, want the rule blocked after 24 hours of busys", st)
			}
			if n := tc.warns() - before; n != 1 {
				t.Errorf("%d WARN %q lines at the block, want 1", n, logMsgRunNotStarted)
			}
		},
	}
}

// nameFailedCancelling leaves wf_1 abandoned-cancelling with cause name_failed, its clock not started, the effect
// goroutine running.
func nameFailedCancelling(tc *testConfirmer, r *launchrule.Rule) {
	tc.run()
	if err := tc.NameAttempt(r.ID, "k1", "wf_1"); !errors.Is(err, errAttemptGone) {
		tc.t.Fatalf("Setup: NameAttempt = %v, want errAttemptGone", err)
	}
}

// bootUnleasedCancelling leaves wf_1 abandoned-cancelling with cause boot_unleased, as A28 does after a restart.
func bootUnleasedCancelling(tc *testConfirmer, r *launchrule.Rule) {
	unconfirmed(tc, r)
	tc.restart()
	tc.run()
}

func leasedCancelling(tc *testConfirmer, r *launchrule.Rule) {
	abandonedCancelling(tc, r)
	tc.lease(r.ID, "k1", "wf_1")
}

// heldByOther runs a busy on X naming Y's run at each of n poll intervals, answering every cancel with err.
func heldByOther(tc *testConfirmer, x, wf string, n int, err error) {
	for range n {
		tc.busy(x, wf)
		tc.answerCancels(err)
		advance(pollTick)
	}
}

var busyCases = []attemptCase{
	liveBusy("17 invoking, busy: A12", invoking, stateInvoking),
	liveBusy("17 invoked-unconfirmed, busy: A12", unconfirmed, stateInvokedUnconfirmed),
	liveBusy("17 boot-held, busy: A12", bootedHeld, stateBootHeld),
	{
		name:  "26 abandoned-cancelling, cancel error (claimed): A14, then the next busy is A16",
		setup: abandonedCancelling,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.answerCancels(errClaimed)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || !rec.cancelLandedAt.IsZero() || rec.cancelsOut != 0 {
				t.Fatalf("record = %+v, want abandoned-cancelling, no cancel_landed_at, nothing out", rec)
			}
			tc.busy(r.ID, "wf_1")
			tc.answerCancels(errClaimed)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1", "wf_1"}) {
				t.Errorf("cancels run = %v, want the bound's and the busy's", ran)
			}
		},
	},
	refusedEveryTime("30 abandoned-cancelling (leased), refused every time, busy at five poll intervals: A16",
		leasedCancelling),
	refusedEveryTime("30 abandoned-cancelling (boot_unleased), refused every time, busy at five poll intervals: A16",
		bootUnleasedCancelling),
	refusedEveryTime("30 abandoned-cancelling (name_failed), refused every time, busy at five poll intervals: A16",
		nameFailedCancelling),
	{
		name: "31 abandoned-cancelled, busy: A17",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			nameFailedCancelling(tc, r)
			tc.answerCancels(nil)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.busy(r.ID, "wf_1")
			tc.answerCancels(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels run = %v, want only the abandon's", ran)
			}
			_, st := tc.memory(r.ID)
			if !st.FailingSince.Equal(time.Now()) {
				t.Errorf("failing since = %v, want the busy counted now", st.FailingSince)
			}
			if !strings.HasSuffix(st.RowText, "has not ended yet") {
				t.Errorf("row text = %q, want it to say the run has not ended yet", st.RowText)
			}
		},
	},
	{
		name:  "38 abandoned-cancelled, retried, then busy naming the id: A33 then A24",
		setup: abandonedCancelled,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			ans, err := tc.Retried("wf_1")
			if err != nil || !ans.HandedBack {
				tc.t.Errorf("Retried = %+v, %v; want handed back, nil", ans, err)
			}
			tc.manualLease("wf_1")
			tc.busy(r.ID, "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec != nil {
				t.Errorf("record = %+v, want none", rec)
			}
			if _, attempts := tc.file(r.ID); len(attempts) != 0 {
				t.Errorf("file attempts = %+v, want none", attempts)
			}
			_, st := tc.memory(r.ID)
			if want := "waiting for the running weekly run to finish"; st.RowText != want {
				t.Errorf("row text = %q, want %q", st.RowText, want)
			}
			if st.BoundAbandons != 1 {
				t.Errorf("count = %d, want the bound's abandon only", st.BoundAbandons)
			}
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels run = %v, want only the abandon's", ran)
			}
			if b := tc.blockerOf(r.ID); b != "" {
				t.Errorf("blocker = %s, want none", b)
			}
		},
	},
	{
		name: "41 none, busy, a manual run's lease: A24",
		setup: func(tc *testConfirmer, _ *launchrule.Rule) {
			tc.manualLease("wf_9")
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.busy(r.ID, "wf_9")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, st := tc.memory(r.ID); st.RowText != "waiting for the running weekly run to finish" {
				t.Errorf("row text = %q, want the running recipe's", st.RowText)
			}
			unchangedBy(t, tc, r.ID)
		},
	},
	{
		name: "42 none, busy with no lease for 10 minutes, then a run list without it: A24, then E5 with A34",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			texts := []string{
				"waiting for run wf_9 to finish",
				"run wf_9 is blocking weekly and this rule has no record of launching it",
				"run wf_9 is blocking weekly and this rule has no record of launching it",
			}
			for i, want := range texts {
				tc.busy(r.ID, "wf_9")
				if _, st := tc.memory(r.ID); st.RowText != want {
					tc.t.Errorf("row text at %v = %q, want %q", time.Duration(i)*pollTick, st.RowText, want)
				}
				if n, want := tc.warns(), min(i, 1); n != want {
					tc.t.Errorf("%d WARN lines at %v, want %d", n, time.Duration(i)*pollTick, want)
				}
				advance(pollTick)
			}
			tc.written()
			tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{"wf_9": marotte.RunStatusCompleted})
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, st := tc.memory(r.ID); st.RowText != "" {
				t.Errorf("row text = %q, want it cleared", st.RowText)
			}
			if b := tc.blockerOf(r.ID); b != "" {
				t.Errorf("blocker = %s, want it cleared", b)
			}
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes, want none", n)
			}
			if got := tc.cancels(); len(got) != 0 {
				t.Errorf("cancels = %v, want none: an unnamed run is never cancelled", got)
			}
			unchangedBy(t, tc, r.ID)
		},
	},
	{
		name: "66 Y abandoned-cancelling and refused every time, busy on X naming it at three poll intervals: A16 on Y, A38 on X",
		deliver: func(tc *testConfirmer, x *launchrule.Rule) {
			y := otherRule(tc)
			tc.run()
			tc.seedRecord(y.ID, &record{
				workflowID: "wf_y", key: "k9", state: stateAbandonedCancelling, cause: causeBound, at: time.Now(),
			})
			heldByOther(tc, x.ID, "wf_y", 3, errCancelRefused)
			if _, st := tc.memory(y.ID); st.FailingSince.IsZero() {
				tc.t.Errorf("Y status %+v, want the busys counted on Y's clock", st)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, x *launchrule.Rule) {
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_y", "wf_y", "wf_y"}) {
				t.Errorf("cancels run = %v, want three for wf_y", ran)
			}
			want := "waiting for run wf_y, abandoned by rule nightly, to be cancelled"
			if _, st := tc.memory(x.ID); st.RowText != want {
				t.Errorf("X row text = %q, want %q", st.RowText, want)
			}
			if n := tc.warns(); n != 0 {
				t.Errorf("%d WARN lines, want none", n)
			}
			unchangedBy(t, tc, x.ID)
		},
	},
	{
		name: "67a Y invoking, its lease not granted, busy on X before the bound: A12 on Y, A38 on X",
		deliver: func(tc *testConfirmer, x *launchrule.Rule) {
			y := otherRule(tc)
			invoking(tc, &y)
			for i := range 3 {
				if i > 0 {
					advance(2 * time.Minute)
				}
				tc.busy(x.ID, "wf_1")
			}
			if rec := tc.recordOf(y.ID, "wf_1"); rec == nil || rec.state != stateInvoking {
				tc.t.Errorf("Y record = %+v, want it still invoking", rec)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, x *launchrule.Rule) {
			if got := tc.cancels(); len(got) != 0 {
				t.Errorf("cancels = %v, want none before the bound", got)
			}
			if _, st := tc.memory(x.ID); st.RowText != "waiting for run wf_1, launched by rule nightly" {
				t.Errorf("X row text = %q, want the launching rule named", st.RowText)
			}
			if n := tc.warns(); n != 0 {
				t.Errorf("%d WARN lines, want none", n)
			}
			unchangedBy(t, tc, x.ID)
		},
	},
	{
		name: "67b Y invoking, its lease not granted, X holding nothing, Y's bound reached: A6 on Y, X unchanged",
		deliver: func(tc *testConfirmer, x *launchrule.Rule) {
			y := otherRule(tc)
			invoking(tc, &y)
			advance(launchConfirmBound)
			if _, st := tc.memory(y.ID); st.BoundAbandons != 1 || st.FailingSince.IsZero() {
				tc.t.Errorf("Y status %+v, want one bound abandon and the clock started", st)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, x *launchrule.Rule) {
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one for wf_1", got)
			}
			if _, st := tc.memory(x.ID); st.RowText != "" {
				t.Errorf("X row text = %q, want none", st.RowText)
			}
			unchangedBy(t, tc, x.ID)
		},
	},
	{
		name: "67c Y abandoned at its bound, then busy on X naming its run: A16 on Y, A38 on X",
		deliver: func(tc *testConfirmer, x *launchrule.Rule) {
			y := otherRule(tc)
			invoking(tc, &y)
			advance(launchConfirmBound)
			tc.busy(x.ID, "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, x *launchrule.Rule) {
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1", "wf_1"}) {
				t.Errorf("cancels = %v, want the bound's and the busy's resend", got)
			}
			want := "waiting for run wf_1, abandoned by rule nightly, to be cancelled"
			if _, st := tc.memory(x.ID); st.RowText != want {
				t.Errorf("X row text = %q, want %q", st.RowText, want)
			}
			if n := tc.warns(); n != 0 {
				t.Errorf("%d WARN lines, want none", n)
			}
			unchangedBy(t, tc, x.ID)
		},
	},
	{
		name: "69 abandoned-cancelling, failing for 23 hours, restart, busy an hour later: A16 on the loaded clock",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			abandonedOnDisk(tc, r)
			tc.setStatus(r.ID, func(st *launchrule.RuleStatus) { st.FailingSince = time.Now().Add(-23 * time.Hour) })
			tc.persist(r.ID)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.restart()
			tc.run()
			tc.answerCancels(errCancelRefused)
			advance(time.Hour)
			tc.busy(r.ID, "wf_1")
			tc.answerCancels(errCancelRefused)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			_, st := tc.memory(r.ID)
			if want := time.Now().Add(-24 * time.Hour); !st.FailingSince.Equal(want) {
				t.Errorf("failing since = %v, want %v: the loaded clock, not restarted", st.FailingSince, want)
			}
			if st.Blocked == "" {
				t.Errorf("status %+v, want the rule blocked", st)
			}
			if n := tc.warns(); n != 1 {
				t.Errorf("%d WARN %q lines, want the block's", n, logMsgRunNotStarted)
			}
		},
	},
}
