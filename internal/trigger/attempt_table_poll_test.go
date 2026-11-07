package trigger

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// The lines of the design's test table in which a poll's reads straddle a rule edit, run by TestAttemptTable.

var pollCases = []attemptCase{
	{
		name: "N1 a poll of repo A in flight, edit (rebaseline) to repo B, then the poll's update: E2, nothing applied",
		spec: func() *launchrule.EventSpec { return openedSpec("alice") },
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			src := issueSource(issue(5, "alice"))
			apply := beginPoll(tc.t, tc.Confirmer, r.ID, src)
			edit(tc, r, 0, moveRepo)
			apply()
			if n := tc.written(); n != 0 {
				tc.t.Errorf("the poll's update wrote %d times, want no write", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es := tc.memoryState(r.ID)
			if rc := es.cursor[Feed{Selector: testRepo}.RepoID()]; rc != nil {
				t.Errorf("cursor entry for %s = %+v, want none: the poll read the old repository", testRepo, rc)
			}
			if len(es.queue) != 0 {
				t.Errorf("queue = %v, want empty", keysOf(es.queue))
			}
			def, _ := tc.store.Get(r.ID)
			if want := launchrule.Population(&def); es.raw.Population != want {
				t.Errorf("population = %q, want the edited rule's %q", es.raw.Population, want)
			}
		},
	},
	{
		name: "N2 a poll in flight, edit (filter) changing filter.label, then the poll's update: E1, results dropped",
		spec: labeledSpec,
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			src := issueSource(issue(7, "alice"))
			beginPoll(tc.t, tc.Confirmer, r.ID, src)()
			if rc := tc.memoryState(r.ID).cursor[Feed{Selector: testRepo}.RepoID()]; rc == nil || rc.Labeled == nil {
				tc.t.Fatalf("Setup: cursor entry = %+v, want the labeled baseline", rc)
			}
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			src := issueSource(issue(7, "alice", testLabel))
			apply := beginPoll(tc.t, tc.Confirmer, r.ID, src)
			edit(tc, r, 0, func(r *launchrule.Rule) { r.When.Event.Filter.Label = "report:slow" })
			before := cursorJSON(tc.t, tc.memoryState(r.ID))
			apply()
			if n := tc.written(); n != 0 {
				tc.t.Errorf("the poll's update wrote %d times, want no write", n)
			}
			if after := cursorJSON(tc.t, tc.memoryState(r.ID)); after != before {
				tc.t.Errorf("the poll's update changed the cursor from %s to %s, want it unchanged", before, after)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if es := tc.memoryState(r.ID); len(es.queue) != 0 {
				t.Errorf("queue = %v, want empty: the gain was read under the old label", keysOf(es.queue))
			}
		},
	},
}

// issueSource is a source whose open issues are items, in one complete page.
func issueSource(items ...forgeapi.Issue) *fakeSource {
	return &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{{Items: items}}}
}

// memoryState is a deep copy of the rule's whole in-memory state.
func (tc *testConfirmer) memoryState(ruleID string) eventState {
	tc.t.Helper()
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	st, err := rv.es.encode()
	rv.mu.Unlock()
	if err != nil {
		tc.t.Fatalf("encode(%s): %v", ruleID, err)
	}
	es, err := decodeState(&st)
	if err != nil {
		tc.t.Fatalf("decodeState(%s): %v", ruleID, err)
	}
	return es
}

func cursorJSON(t *testing.T, es eventState) string {
	t.Helper()
	b, err := json.Marshal(es.cursor)
	if err != nil {
		t.Fatalf("marshal cursor: %v", err)
	}
	return string(b)
}
