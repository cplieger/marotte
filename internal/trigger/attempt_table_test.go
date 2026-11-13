package trigger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// attemptCase is one line of the design's test table: a start state, the event delivered, and what must follow.
// Every case runs in a synctest bubble, so the bound's timers run on its clock.
type attemptCase struct {
	// spec is the rule's event spec, runSpec("success") when nil.
	spec    func() *launchrule.EventSpec
	setup   func(tc *testConfirmer, r *launchrule.Rule)
	deliver func(tc *testConfirmer, r *launchrule.Rule)
	check   func(t *testing.T, tc *testConfirmer, r *launchrule.Rule)
	name    string
	// a0 marks a case whose expected row is A0.
	a0 bool
}

// invoking names wf_1 for the queued key k1, as A1 does.
func invoking(tc *testConfirmer, r *launchrule.Rule) {
	tc.enqueue(r.ID, held("k1", "c1"))
	tc.name(r.ID, "k1", "wf_1")
}

// unconfirmed is invoking with the call returned nil, as A4 leaves it.
func unconfirmed(tc *testConfirmer, r *launchrule.Rule) {
	invoking(tc, r)
	tc.LaunchReturned(r.ID, "wf_1", nil)
}

// failing sets a counted bound abandon and a started 24-hour clock, so a success has something to reset.
func failing(tc *testConfirmer, r *launchrule.Rule) {
	tc.setStatus(r.ID, func(st *launchrule.RuleStatus) {
		st.BoundAbandons, st.FailingSince = 1, time.Now().Add(-time.Hour)
	})
}

func newest() *launchrule.EventSpec {
	ev := runSpec("success")
	ev.Hold = &launchrule.Hold{Policy: launchrule.HoldNewest, Cap: 10}
	return ev
}

var attemptCases = []attemptCase{
	{
		name: "1 none, named with the key queued: A1",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.name(r.ID, "k1", "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateInvoking || !rec.inFlight || rec.bound == nil {
				t.Fatalf("record = %+v, want invoking, in flight, its bound armed", rec)
			}
			if rec.inputs["event_id"] != "id/k1" {
				t.Errorf("record inputs = %v, want the entry's, event_id id/k1", rec.inputs)
			}
			es, attempts := tc.file(r.ID)
			want := attemptEntry{WorkflowID: "wf_1", Key: "k1", State: persistedPending, At: rec.at}
			if len(attempts) != 1 || !sameAttempt(attempts[0], want) {
				t.Errorf("file attempts = %+v, want one pending record %+v written before NameAttempt returned", attempts, want)
			}
			if len(es.queue) != 1 || es.queue[0].Live != "wf_1" {
				t.Errorf("file queue = %+v, want k1 naming its live record wf_1", es.queue)
			}
		},
	},
	{
		name: "2 none, named with the key reset by an edit: A2",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if err := tc.NameAttempt(r.ID, "k1", "wf_1"); !errors.Is(err, errAttemptGone) {
				tc.t.Errorf("NameAttempt = %v, want errAttemptGone", err)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			_, attempts := tc.file(r.ID)
			if len(attempts) != 1 || attempts[0].State != persistedAbandoned || attempts[0].Cause != causeNameFailed ||
				attempts[0].WorkflowID != "wf_1" {
				t.Errorf("file attempts = %+v, want one abandoned wf_1 with cause name_failed", attempts)
			}
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one for wf_1", got)
			}
			_, st := tc.memory(r.ID)
			if st.RowText != "" || !st.FailingSince.IsZero() {
				t.Errorf("row text %q, failing since %v; want no row text and the 24-hour clock not started", st.RowText, st.FailingSince)
			}
		},
	},
	deletedRuleNamed("3 none, named with the rule deleted, then cancel ok: A2, A13, then A32", nil),
	deletedRuleNamed("3 none, named with the rule deleted, then cancel error: A2, A14, then A32", errCancelRefused),
	{
		name:  "6 invoking, invoke ok: A4",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			before, _ := os.ReadFile(statePath(tc, r.ID))
			tc.LaunchReturned(r.ID, "wf_1", nil)
			if after, _ := os.ReadFile(statePath(tc, r.ID)); string(after) != string(before) {
				tc.t.Error("invoke ok rewrote the state file, want no write")
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateInvokedUnconfirmed || rec.inFlight || rec.bound == nil {
				t.Fatalf("record = %+v, want invoked-unconfirmed, not in flight, its bound still armed", rec)
			}
			advance(launchConfirmBound - time.Second)
			if got := tc.cancels(); len(got) != 0 {
				t.Errorf("cancels before the bound = %v, want none", got)
			}
		},
	},
	{
		name:  "7 invoking, invoke reply lost (deadline): A4, then a run_start 2 s later confirms",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", context.DeadlineExceeded)
			advance(2 * time.Second)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: replyLostConfirmed,
	},
	{
		name:  "7 invoking, invoke reply lost (bridge exited): A4, then a run_start 2 s later confirms",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", marotte.ErrBridgeExited)
			advance(2 * time.Second)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: replyLostConfirmed,
	},
	{
		name:  "9 invoking, invoke error ErrLeaseNotGranted: A5",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", ErrLeaseNotGranted)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeLeaseNotGranted || rec.inFlight {
				t.Fatalf("record = %+v, want abandoned-cancelling, cause lease_not_granted, not in flight", rec)
			}
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) || rec.cancelsOut != 1 {
				t.Errorf("cancels = %v (out %d), want exactly one, from the effect", got, rec.cancelsOut)
			}
			if _, st := tc.memory(r.ID); st.FailingSince.IsZero() {
				t.Error("status.failing_since not set, want the failed attempt on the 24-hour clock")
			}
		},
	},
	{
		name: "10 invoking, run_start: A3",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			invoking(tc, r)
			failing(tc, r)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
			tc.LaunchReturned(r.ID, "wf_1", nil)
		},
		check: confirmed,
	},
	{
		name: "11 invoked-unconfirmed, run_start: A3",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			unconfirmed(tc, r)
			failing(tc, r)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: confirmed,
	},
	{
		name: "12 invoked-unconfirmed, bound reached twice across two events of a newest rule: A6 then Q",
		spec: newest,
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			unconfirmed(tc, r)
			tc.enqueue(r.ID, held("k2", "c1"))
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			advance(launchConfirmBound + time.Second)
			es, st := tc.memory(r.ID)
			if !slices.Equal(keysOf(es.queue), []string{"k2"}) ||
				!slices.Equal(es.superseded, []supersededEntry{{Key: "k1", By: "k2"}}) || st.BoundAbandons != 1 {
				tc.t.Fatalf("after the first abandon: queue %v, superseded %+v, count %d; want k2 alone, k1 superseded by it, count 1",
					keysOf(es.queue), es.superseded, st.BoundAbandons)
			}
			tc.name(r.ID, "k2", "wf_2")
			tc.LaunchReturned(r.ID, "wf_2", nil)
			advance(launchConfirmBound + time.Second)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			_, st := tc.memory(r.ID)
			if st.BoundAbandons != 2 || st.Blocked != st.RowText || st.RowText != twiceUnreported("wf_2") {
				t.Errorf("count %d, blocked %q, row %q; want 2 and the rule blocked with A6's second text", st.BoundAbandons, st.Blocked, st.RowText)
			}
			if n := tc.log.CountExact(logMsgRunNotStarted); n != 1 {
				t.Errorf("%d WARN %q lines, want 1", n, logMsgRunNotStarted)
			}
			if got, _ := tc.log.AttrValueExact(logMsgRunNotStarted, "class"); got != "blocked" {
				t.Errorf("WARN class = %q, want blocked", got)
			}
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1", "wf_2"}) {
				t.Errorf("cancels = %v, want both runs, the second abandon still cancelling", got)
			}
		},
	},
	{
		name:  "13 invoking, bound reached, the call returns, then the cancel answers: A6 with owes_release, R at the answer",
		setup: abandonedInFlight,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || !rec.owesRelease || !rec.inFlight {
				tc.t.Fatalf("record at the bound = %+v, want owes_release while its call is in flight", rec)
			}
			tc.LaunchReturned(r.ID, "wf_1", nil)
			synctest.Wait()
			if _, releases := tc.fx.calls(); len(releases) != 0 {
				tc.t.Errorf("releases before the cancel answered = %v, want none", releases)
			}
			tc.answerCancels(nil)
		},
		check: releasedOnce,
	},
	{
		name:  "13 invoking, bound reached, the cancel answers, then the call returns: A6 with owes_release, R at the return",
		setup: abandonedInFlight,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.answerCancels(nil)
			if _, releases := tc.fx.calls(); len(releases) != 0 {
				tc.t.Errorf("releases before the call returned = %v, want none", releases)
			}
			tc.LaunchReturned(r.ID, "wf_1", nil)
			synctest.Wait()
		},
		check: releasedOnce,
	},
	{
		name:  "24 abandoned-cancelling with owes_release, cancel ok after the call returned: A13 and R",
		setup: owingRelease,
		deliver: func(tc *testConfirmer, _ *launchrule.Rule) {
			tc.answerCancels(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			releasedOnce(t, tc, r)
			_, attempts := tc.file(r.ID)
			if len(attempts) != 1 || !attempts[0].CancelLandedAt.Equal(time.Now()) || attempts[0].State != persistedAbandoned {
				t.Errorf("file attempts = %+v, want wf_1 abandoned with cancel_landed_at written now", attempts)
			}
		},
	},
	{
		name:  "25 abandoned-cancelling with owes_release, cancel error after the call returned: A14 and R",
		setup: owingRelease,
		deliver: func(tc *testConfirmer, _ *launchrule.Rule) {
			tc.answerCancels(errCancelRefused)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if cancels, releases := tc.fx.calls(); !slices.Equal(cancels, []string{"wf_1"}) || len(releases) != 0 {
				t.Errorf("cancels %v, releases %v; want one cancel and no release: the lease stays as the floor", cancels, releases)
			}
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.owesRelease || rec.cancelsOut != 0 ||
				!rec.cancelLandedAt.IsZero() {
				t.Errorf("record = %+v, want abandoned-cancelling, owes_release cleared, no cancel out, none landed", rec)
			}
			if _, attempts := tc.file(r.ID); len(attempts) != 1 || !attempts[0].CancelLandedAt.IsZero() {
				t.Errorf("file attempts = %+v, want wf_1 with no cancel_landed_at", attempts)
			}
		},
	},
	secondCancelAnswers("27 abandoned-cancelled, cancel error from a second cancel: A22", errCancelRefused),
	secondCancelAnswers("27 abandoned-cancelled, cancel ok from a second cancel: A22", nil),
	{
		name:    "28 abandoned-cancelling, run_start: A22",
		setup:   abandonedCancelling,
		deliver: lateRunStart,
		check:   stillQueued(stateAbandonedCancelling),
	},
	{
		name:    "28 abandoned-cancelled, run_start: A22",
		setup:   abandonedCancelled,
		deliver: lateRunStart,
		check:   stillQueued(stateAbandonedCancelled),
	},
	{
		name:    "29 abandoned-cancelling, bound reached: A22",
		setup:   abandonedCancelling,
		deliver: lateBound,
		check:   cancelledOnce(stateAbandonedCancelling),
	},
	{
		name:    "29 abandoned-cancelled, bound reached: A22",
		setup:   abandonedCancelled,
		deliver: lateBound,
		check:   cancelledOnce(stateAbandonedCancelled),
	},
	{
		name: "58 invoking, named for the same id: A0",
		a0:   true,
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.seedRecord(r.ID, &record{workflowID: "wf_1", key: "k1", state: stateInvoking})
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if err := tc.NameAttempt(r.ID, "k1", "wf_1"); err == nil {
				tc.t.Error("NameAttempt = nil on A0, want an error so the run is not invoked")
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateInvoking || rec.key != "k1" {
				t.Errorf("record = %+v, want the invoking record of k1 unchanged", rec)
			}
			assertNoStateFile(t, tc, r.ID)
			if n := tc.log.CountExact(logMsgImpossible); n != 1 {
				t.Errorf("%d ERROR %q lines, want 1", n, logMsgImpossible)
			}
			for key, want := range map[capture.AttrName]string{
				"rule_id": r.ID, "state": "invoking", "event": "named", "workflow_id": "wf_1",
			} {
				if got, ok := tc.log.AttrValueExact(logMsgImpossible, key); !ok || got != want {
					t.Errorf("A0 line %s = %q (logged %v), want %q", key, got, ok, want)
				}
			}
		},
	},
	{
		name:  "75 invoking, invoke error *marotte.RPCError -32602: A5 blocks the rule",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", &marotte.RPCError{Code: -32602, Message: "input selection is required"})
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one", got)
			}
			es, st := tc.memory(r.ID)
			if len(es.queue) != 1 || es.queue[0].Key != "k1" || es.queue[0].Live != "" {
				t.Errorf("queue = %+v, want k1 kept in its place with no live record (Q)", es.queue)
			}
			if st.Blocked != "input selection is required" {
				t.Errorf("blocked = %q, want KAS's sentence", st.Blocked)
			}
			if got, _ := tc.log.AttrValueExact(logMsgRunNotStarted, "class"); got != "blocked" {
				t.Errorf("WARN class = %q, want blocked", got)
			}
			if _, file := tc.file(r.ID); len(file) != 1 || file[0].State != persistedAbandoned {
				t.Errorf("file attempts = %+v, want the abandoned record in A5's write", file)
			}
		},
	},
	{
		name:  "75b invoking, invoke error *marotte.RPCError -32602: A5 blocks without starting the 24-hour clock",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", fmt.Errorf("workflow invoke: %w", invalidParams))
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			_, st := tc.memory(r.ID)
			if st.Blocked != "inputs.run_id: Required" || !st.FailingSince.IsZero() {
				t.Errorf("status %+v, want blocked with KAS's sentence and the 24-hour clock not started", st)
			}
		},
	},
	{
		name:  "N4 invoking, invoke error wrapping workflow.ErrJustClaimed: A5, one cancel, Q, nothing counted",
		setup: invoking,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", fmt.Errorf("workflow invoke: %w", justClaimed))
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeInvokeError {
				t.Fatalf("record = %+v, want abandoned-cancelling with cause invoke_error", rec)
			}
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one", got)
			}
			es, st := tc.memory(r.ID)
			if len(es.queue) != 1 || es.queue[0].Key != "k1" || es.queue[0].Live != "" {
				t.Errorf("queue = %+v, want k1 kept in its place with no live record (Q)", es.queue)
			}
			if !st.FailingSince.IsZero() || st.Blocked != "" || st.BoundAbandons != 0 {
				t.Errorf("status %+v, want the 24-hour clock not started, no block, the bound count unchanged", st)
			}
			if got := tc.classes(); !slices.Equal(got, []string{"retryable"}) {
				t.Errorf("WARN classes %v, want one retryable", got)
			}
		},
	},
}

var errCancelRefused = errors.New("the cancel was refused")

// deletedRuleNamed is case 3: a run named for a rule launch-rules.json no longer holds is abandoned unpersisted,
// and once its cancel answers with cancelErr the record and the rule's in-memory value are gone.
func deletedRuleNamed(name string, cancelErr error) attemptCase {
	return attemptCase{
		name: name,
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.run()
			deleteRule(tc.t, tc.Confirmer, r.ID)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if err := tc.NameAttempt(r.ID, "k1", "wf_1"); !errors.Is(err, errAttemptGone) {
				tc.t.Errorf("NameAttempt = %v, want errAttemptGone", err)
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || !rec.unpersisted || rec.cancelsOut != 1 {
				tc.t.Fatalf("record before the answer = %+v, want an unpersisted abandon with its cancel out", rec)
			}
			tc.answerCancels(cancelErr)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the deleted rule's in-memory value is kept after its last record left, want it dropped")
			}
			if cancels, releases := tc.fx.calls(); !slices.Equal(cancels, []string{"wf_1"}) || len(releases) != 0 {
				t.Errorf("cancels %v, releases %v; want one cancel and no release", cancels, releases)
			}
			assertNoStateFile(t, tc, r.ID)
		},
	}
}

// secondCancelAnswers is case 27: a second cancel of an abandoned-cancelled record, as A16 or A21 send one, answers
// with cancelErr a minute after the first landed. The record keeps its state and the first landing, and writes
// nothing.
func secondCancelAnswers(name string, cancelErr error) attemptCase {
	return attemptCase{
		name:  name,
		setup: abandonedCancelled,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			before, _ := os.ReadFile(statePath(tc, r.ID))
			landed := tc.recordOf(r.ID, "wf_1").cancelLandedAt
			advance(time.Minute)
			// The engine is ready, so the effect goroutine answers the cancel as soon as it is queued.
			tc.fx.mu.Lock()
			tc.fx.cancelErr = cancelErr
			tc.fx.mu.Unlock()
			tc.withRecord(r.ID, "wf_1", func(rec *record) { rec.cancelsOut++ })
			tc.dispatch(r.ID, []attemptEffect{{kind: effectCancel, workflowID: "wf_1"}})
			synctest.Wait()
			if cancels, _ := tc.fx.calls(); !slices.Equal(cancels, []string{"wf_1", "wf_1"}) {
				tc.t.Fatalf("cancels = %v, want the first and the second", cancels)
			}
			if after, _ := os.ReadFile(statePath(tc, r.ID)); string(after) != string(before) {
				tc.t.Error("the second cancel's answer rewrote the state file, want no write")
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || !rec.cancelLandedAt.Equal(landed) {
				tc.t.Errorf("record = %+v, want cancel_landed_at kept at %v", rec, landed)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelled || rec.cancelsOut != 0 {
				t.Errorf("record = %+v, want abandoned-cancelled with cancels_out back to 0", rec)
			}
			if _, releases := tc.fx.calls(); len(releases) != 0 {
				t.Errorf("releases = %v, want none: the record never owed one", releases)
			}
		},
	}
}

// abandonedInFlight leaves wf_1 abandoned at its bound while its LaunchTriggered call is still out, as A6 does, its
// cancel queued and the engine not yet ready.
func abandonedInFlight(tc *testConfirmer, r *launchrule.Rule) {
	tc.run()
	invoking(tc, r)
	advance(launchConfirmBound + time.Second)
}

// owingRelease is abandonedInFlight with the call returned: abandoned-cancelling, owes_release, its cancel out.
func owingRelease(tc *testConfirmer, r *launchrule.Rule) {
	abandonedInFlight(tc, r)
	tc.LaunchReturned(r.ID, "wf_1", nil)
}

// abandonedCancelling leaves wf_1 abandoned at its bound after its call returned, its cancel queued and the engine
// not yet ready.
func abandonedCancelling(tc *testConfirmer, r *launchrule.Rule) {
	tc.run()
	unconfirmed(tc, r)
	advance(launchConfirmBound + time.Second)
}

// abandonedCancelled is abandonedCancelling with its cancel landed.
func abandonedCancelled(tc *testConfirmer, r *launchrule.Rule) {
	abandonedCancelling(tc, r)
	tc.answerCancels(nil)
}

// releasedOnce checks R on a record that owed a release: once its call returned and its cancel landed, one
// ReleaseTriggered, and the record abandoned-cancelled with owes_release cleared.
func releasedOnce(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
	t.Helper()
	if cancels, releases := tc.fx.calls(); !slices.Equal(cancels, []string{"wf_1"}) ||
		!slices.Equal(releases, []string{"wf_1"}) {
		t.Errorf("cancels %v, releases %v; want one of each for wf_1", cancels, releases)
	}
	rec := tc.recordOf(r.ID, "wf_1")
	if rec == nil || rec.state != stateAbandonedCancelled || rec.owesRelease || rec.inFlight || rec.cancelsOut != 0 ||
		!rec.cancelLandedAt.Equal(time.Now()) {
		t.Errorf("record = %+v, want abandoned-cancelled now, owes_release cleared, nothing in flight or out", rec)
	}
}

func lateRunStart(tc *testConfirmer, r *launchrule.Rule) {
	tc.ConfirmEvent(r.ID, "k1", "wf_1")
}

// lateBound is a bound timer delivering after its record was abandoned.
func lateBound(tc *testConfirmer, r *launchrule.Rule) {
	tc.deliver(r.ID, &attemptEvent{kind: evBoundReached, workflowID: "wf_1"})
	synctest.Wait()
}

// stillQueued checks case 28: the abandoned record confirms nothing and its key stays queued.
func stillQueued(want attemptState) func(*testing.T, *testConfirmer, *launchrule.Rule) {
	return func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != want {
			t.Errorf("record = %+v, want it unchanged, %s", rec, want)
		}
		es, _ := tc.memory(r.ID)
		if !slices.Equal(keysOf(es.queue), []string{"k1"}) || len(es.launched) != 0 {
			t.Errorf("queue %v, launched %+v; want k1 still queued and nothing confirmed", keysOf(es.queue), es.launched)
		}
		if n := tc.log.CountExact(logMsgRunStarted); n != 0 {
			t.Errorf("%d INFO %q lines, want none", n, logMsgRunStarted)
		}
	}
}

// cancelledOnce checks case 29: a late bound neither cancels again nor counts.
func cancelledOnce(want attemptState) func(*testing.T, *testConfirmer, *launchrule.Rule) {
	return func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
		ran, _ := tc.fx.calls()
		if all := append(ran, tc.cancels()...); !slices.Equal(all, []string{"wf_1"}) {
			t.Errorf("cancels run and queued = %v, want wf_1 once", all)
		}
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != want {
			t.Errorf("record = %+v, want it unchanged, %s", rec, want)
		}
		if _, st := tc.memory(r.ID); st.BoundAbandons != 1 {
			t.Errorf("count = %d, want 1: the late bound is not another abandon", st.BoundAbandons)
		}
	}
}

// replyLostConfirmed checks case 7: no cancel, the event confirmed under wf_1, nothing left to launch.
func replyLostConfirmed(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
	t.Helper()
	advance(launchConfirmBound)
	if got := tc.cancels(); len(got) != 0 {
		t.Errorf("cancels = %v, want none: the lease and host session stay", got)
	}
	es, _ := tc.memory(r.ID)
	if len(es.queue) != 0 || len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" {
		t.Errorf("queue %v, launched %+v; want k1 launched once under wf_1 and nothing queued", keysOf(es.queue), es.launched)
	}
}

// confirmed checks cases 10 and 11: A3's move, its status resets and a stopped bound.
func confirmed(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
	t.Helper()
	if rec := tc.recordOf(r.ID, "wf_1"); rec != nil {
		t.Errorf("record = %+v, want none: a confirmed attempt has no record", rec)
	}
	es, attempts := tc.file(r.ID)
	if len(es.queue) != 0 || len(es.launched) != 1 || es.launched[0].Key != "k1" || es.launched[0].WorkflowID != "wf_1" {
		t.Errorf("file queue %v, launched %+v; want k1 in launched under wf_1", keysOf(es.queue), es.launched)
	}
	if len(attempts) != 0 {
		t.Errorf("file attempts = %+v, want none", attempts)
	}
	st := es.raw.Status
	if st.LastRunID != "wf_1" || st.BoundAbandons != 0 || !st.FailingSince.IsZero() {
		t.Errorf("status = %+v, want last_run_id wf_1, count 0, failing_since zero", st)
	}
	if st.LastEvent != "k1" {
		t.Errorf("last_event = %q, want k1: the row names the confirmed event", st.LastEvent)
	}
	advance(launchConfirmBound + time.Second)
	if got := tc.cancels(); len(got) != 0 {
		t.Errorf("cancels after the bound = %v, want none: the bound stopped", got)
	}
	if n := tc.log.CountExact(logMsgRunStarted); n != 1 {
		t.Errorf("%d INFO %q lines, want 1", n, logMsgRunStarted)
	}
}

func TestAttemptTable(t *testing.T) {
	for _, c := range slices.Concat(attemptCases, ruleRowCases, bootCases, writeCases, retryCases, busyCases, launcherCases, launchErrorCases, pollCases, unreadableCases) {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tc := newTestConfirmer(t)
				if c.a0 {
					tc.expectA0()
				}
				spec := runSpec("success")
				if c.spec != nil {
					spec = c.spec()
				}
				r := tc.createRule(spec)
				if c.setup != nil {
					c.setup(tc, &r)
				}
				c.deliver(tc, &r)
				c.check(t, tc, &r)
			})
		})
	}
}

// beginPoll starts one poll of the rule's feed over src: its reads happen now, and the step it returns applies them,
// as the poll does once its reads are back.
func beginPoll(t *testing.T, c *Confirmer, ruleID string, src FeedSource) (apply func()) {
	t.Helper()
	r, ok := c.store.Get(ruleID)
	if !ok || r.When.Event == nil {
		t.Fatalf("Setup: no event rule %s", ruleID)
	}
	fr := feedRules{Feed: Feed{Connection: r.When.Event.Connection, Selector: r.When.Event.Repo}, Rules: []launchrule.Rule{r}}
	p := newPoller(src, c, NewBudget())
	run := p.begin(t.Context(), fr)
	if run == nil {
		t.Fatalf("Setup: the poll skipped rule %s", ruleID)
	}
	return func() { p.apply(run) }
}

func statePath(tc *testConfirmer, ruleID string) string {
	return filepath.Join(tc.dir, "launch-state", ruleID+".json")
}

// assertNoStateFile fails unless the rule has no state file on disk.
func assertNoStateFile(t *testing.T, tc *testConfirmer, ruleID string) {
	t.Helper()
	if _, err := os.Stat(statePath(tc, ruleID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file stat = %v, want no file written", err)
	}
}

// sameAttempt compares At with Equal: a decoded time carries another location than time.Now's,
// so == depends on the host's Local zone.
func sameAttempt(got, want attemptEntry) bool {
	at := got.At.Equal(want.At) && got.CancelLandedAt.Equal(want.CancelLandedAt)
	got.At, got.CancelLandedAt, want.At, want.CancelLandedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return at && got == want
}
