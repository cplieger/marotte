package trigger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

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
	tc.setStatus(r.ID, func(st *launchrule.Status) {
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
			if len(attempts) != 1 || attempts[0] != want {
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
			if err := tc.NameAttempt(r.ID, "k1", "wf_1"); !errors.Is(err, ErrAttemptGone) {
				tc.t.Errorf("NameAttempt = %v, want ErrAttemptGone", err)
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
			for key, want := range map[string]string{
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
	advance(launchConfirmBound + time.Second)
	if got := tc.cancels(); len(got) != 0 {
		t.Errorf("cancels after the bound = %v, want none: the bound stopped", got)
	}
	if n := tc.log.CountExact(logMsgRunStarted); n != 1 {
		t.Errorf("%d INFO %q lines, want 1", n, logMsgRunStarted)
	}
}

func TestAttemptTable(t *testing.T) {
	for _, c := range attemptCases {
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
