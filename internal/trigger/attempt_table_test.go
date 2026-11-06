package trigger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/launchrule"
)

// attemptCase is one line of the design's test table: a start state, the event delivered, and what must follow.
type attemptCase struct {
	setup   func(tc *testConfirmer, r *launchrule.Rule)
	deliver func(tc *testConfirmer, r *launchrule.Rule)
	check   func(t *testing.T, tc *testConfirmer, r *launchrule.Rule)
	name    string
	// a0 marks a case whose expected row is A0.
	a0 bool
}

var attemptCases = []attemptCase{
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
}

func TestAttemptTable(t *testing.T) {
	for _, c := range attemptCases {
		t.Run(c.name, func(t *testing.T) {
			tc := newTestConfirmer(t)
			if c.a0 {
				tc.expectA0()
			}
			r := tc.createRule(runSpec("success"))
			if c.setup != nil {
				c.setup(tc, &r)
			}
			c.deliver(tc, &r)
			c.check(t, tc, &r)
		})
	}
}

// assertNoStateFile fails unless the rule has no state file on disk.
func assertNoStateFile(t *testing.T, tc *testConfirmer, ruleID string) {
	t.Helper()
	path := filepath.Join(tc.dir, "launch-state", ruleID+".json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file stat = %v, want no file written", err)
	}
}
