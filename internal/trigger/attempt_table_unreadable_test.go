package trigger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/launchrule"
)

// The lines of the design's test table for a state file that does not parse, run by TestAttemptTable.

// unparseable is a state file cut off mid-write.
const unparseable = `{"status":`

var unreadableCases = []attemptCase{
	{
		name:    "N6 an unparseable file, boot: blocked, no write, the file's bytes unchanged",
		setup:   func(tc *testConfirmer, r *launchrule.Rule) { writeStateBytes(tc, r.ID, unparseable) },
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertUnreadable(t, tc, r.ID, unparseable)
		},
	},
	{
		name:  "N7 unreadable, then write retry, edit (filter), pause, resume and a run_start for the rule's lease: nothing written, still blocked",
		setup: bootUnreadable,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.persist(r.ID)
			edit(tc, r, 0, widenFilter)
			edit(tc, r, 0, func(r *launchrule.Rule) { r.Enabled = false })
			edit(tc, r, 0, func(r *launchrule.Rule) { r.Enabled = true })
			tc.lease(r.ID, "k1", "wf_9")
			tc.ConfirmEvent(r.ID, "k1", "wf_9") // A23: an unreadable rule holds no record
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertUnreadable(t, tc, r.ID, unparseable)
			if rec := tc.recordOf(r.ID, "wf_9"); rec != nil {
				t.Errorf("record of wf_9 = %+v, want none", rec)
			}
		},
	},
	{
		name: "N8 unreadable and edited, then edit (rebaseline): a fresh file with the new population, unblocked",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			bootUnreadable(tc, r)
			edit(tc, r, 0, widenFilter)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if n := edit(tc, r, 0, moveRepo); n != 1 {
				tc.t.Errorf("the rebaseline wrote %d times, want once", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, err := tc.store.State(r.ID); err != nil {
				t.Fatalf("State after the rebaseline: %v, want the fresh file", err)
			}
			es, _ := tc.file(r.ID)
			def, _ := tc.store.Get(r.ID)
			if want := launchrule.Population(&def); es.raw.Population != want {
				t.Errorf("file population = %q, want the new when's %q", es.raw.Population, want)
			}
			if _, st := tc.memory(r.ID); st.Blocked != "" {
				t.Errorf("block after the rebaseline = %q, want none", st.Blocked)
			}
		},
	},
	{
		name:    "N9 unreadable, then delete: the file removed",
		setup:   bootUnreadable,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) { deleteRule(tc.t, tc.Confirmer, r.ID) },
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertNoStateFile(t, tc, r.ID)
		},
	},
}

// writeStateBytes puts body on disk as the rule's state file.
func writeStateBytes(tc *testConfirmer, ruleID, body string) {
	tc.t.Helper()
	path := statePath(tc, ruleID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		tc.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		tc.t.Fatal(err)
	}
}

// bootUnreadable boots over an unparseable state file of the rule.
func bootUnreadable(tc *testConfirmer, r *launchrule.Rule) {
	writeStateBytes(tc, r.ID, unparseable)
	tc.restart()
}

// assertUnreadable checks the rule is blocked by its unreadable file, which still holds body, and that nothing
// tried to write over it.
func assertUnreadable(t *testing.T, tc *testConfirmer, ruleID, body string) {
	t.Helper()
	want := "could not read launch-state/" + ruleID + ".json: "
	if _, st := tc.memory(ruleID); !strings.HasPrefix(st.Blocked, want) {
		t.Errorf("block = %q, want it to start %q", st.Blocked, want)
	}
	if got, err := os.ReadFile(statePath(tc, ruleID)); err != nil || string(got) != body {
		t.Errorf("state file = %q, %v; want its bytes unchanged", got, err)
	}
	if n := tc.written(); n != 0 {
		t.Errorf("%d writes landed, want none", n)
	}
	if n := tc.log.CountExact(logMsgNotWritten); n != 0 {
		t.Errorf("%d ERROR %q lines, want none: a write over the unreadable file was tried", n, logMsgNotWritten)
	}
}
