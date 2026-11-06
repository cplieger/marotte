package trigger

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// The write-failure lines of the design's test table, run by TestAttemptTable.

// errDiskFull is a state file write the disk refused.
var errDiskFull = errors.New("write launch-state: no space left on device")

// writeTick is the rule's poll interval at the store's default, the write-failure tick's period.
const writeTick = 5 * time.Minute

// writeBlockedText is W1's and W2's sentence for the rule, as errDiskFull failed its write.
func writeBlockedText(r *launchrule.Rule) string {
	return "could not write launch-state/" + r.ID + ".json: " + errDiskFull.Error()
}

// namedUnwritten queues k1 then k2 on one coalesce key and names wf_1 for k1 while every write fails, as W1 meets
// it on a newest rule.
func namedUnwritten(tc *testConfirmer, r *launchrule.Rule) {
	tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c1"))
	tc.failWrites(errDiskFull)
	err := tc.NameAttempt(r.ID, "k1", "wf_1")
	if !errors.Is(err, ErrAttemptNotWritten) || !errors.Is(err, errDiskFull) {
		tc.t.Errorf("NameAttempt = %v, want ErrAttemptNotWritten wrapping the write's error", err)
	}
}

// writeBlocked fails one E10 write of the rule, as W2 meets it.
func writeBlocked(tc *testConfirmer, r *launchrule.Rule) {
	tc.failWrites(errDiskFull)
	tc.persist(r.ID)
	if got := tc.writeBlock(r.ID); got != writeBlockedText(r) {
		tc.t.Fatalf("Setup: write block = %q, want %q", got, writeBlockedText(r))
	}
}

// supersededUnwritten checks W1's Q in memory: k1 superseded by k2, wf_1 abandoned with no bound, one cancel.
func supersededUnwritten(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
	t.Helper()
	rec := tc.recordOf(r.ID, "wf_1")
	if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeNameFailed || rec.inFlight ||
		rec.bound != nil {
		t.Errorf("record = %+v, want abandoned-cancelling, name_failed, not in flight, no bound", rec)
	}
	es, st := tc.memory(r.ID)
	if !slices.Equal(keysOf(es.queue), []string{"k2"}) ||
		!slices.Equal(es.superseded, []supersededEntry{{Key: "k1", By: "k2"}}) {
		t.Errorf("queue %v, superseded %+v; want k1 superseded by k2", keysOf(es.queue), es.superseded)
	}
	if !tc.relaunchable(r.ID, "k2") || tc.relaunchable(r.ID, "k1") {
		t.Error("want only k2 launchable")
	}
	if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
		t.Errorf("cancels = %v, want one for wf_1", got)
	}
	if st.BoundAbandons != 0 || !st.FailingSince.IsZero() {
		t.Errorf("count %d, failing since %v; want nothing counted", st.BoundAbandons, st.FailingSince)
	}
}

// supersedeLanded checks the landed write after W1: the file holds the abandoned record and k1's supersede.
func supersedeLanded(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
	t.Helper()
	es, attempts := tc.file(r.ID)
	if len(attempts) != 1 || attempts[0].WorkflowID != "wf_1" || attempts[0].State != persistedAbandoned ||
		attempts[0].Cause != causeNameFailed {
		t.Errorf("file attempts = %+v, want wf_1 abandoned with cause name_failed", attempts)
	}
	if !slices.Equal(keysOf(es.queue), []string{"k2"}) ||
		!slices.Equal(es.superseded, []supersededEntry{{Key: "k1", By: "k2"}}) {
		t.Errorf("file queue %v, superseded %+v; want k1 superseded by k2", keysOf(es.queue), es.superseded)
	}
}

var writeCases = []attemptCase{
	{
		name:    "5 none, a newest rule with a newer entry of the coalesce key, named, write fails: W1 then Q",
		spec:    newest,
		deliver: namedUnwritten,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			supersededUnwritten(t, tc, r)
			if got := tc.writeBlock(r.ID); got != writeBlockedText(r) {
				t.Errorf("write block = %q, want %q", got, writeBlockedText(r))
			}
			if n := tc.log.CountExact(logMsgNotWritten); n != 1 {
				t.Errorf("%d ERROR %q lines, want 1", n, logMsgNotWritten)
			}
			tc.failWrites(nil)
			advance(writeTick)
			supersedeLanded(t, tc, r)
		},
	},
	{
		name:  "21 invoked-unconfirmed, run_start, write fails: W2, retried by E10, then a restart is A27",
		setup: leasedPending,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.failWrites(errDiskFull)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.memory(r.ID)
			if tc.recordOf(r.ID, "wf_1") != nil || len(es.queue) != 0 || len(es.launched) != 1 {
				t.Errorf("queue %v, launched %+v; want k1 confirmed in memory", keysOf(es.queue), es.launched)
			}
			if got := tc.writeBlock(r.ID); got != writeBlockedText(r) {
				t.Errorf("write block = %q, want %q", got, writeBlockedText(r))
			}
			h := tc.pollerOn()
			h.runPage()
			h.poll()
			advance(writeTick)
			advance(writeTick)
			if n := tc.log.CountExact(logMsgNotWritten); n != 4 {
				t.Errorf("%d ERROR %q lines, want 4: the confirm's, the poll's and two ticks'", n, logMsgNotWritten)
			}
			if _, attempts := tc.file(r.ID); len(attempts) != 1 || attempts[0].State != persistedPending {
				t.Errorf("file attempts = %+v, want wf_1 still pending: nothing landed", attempts)
			}
			tc.restart()
			if !tc.BootHeld("wf_1") {
				t.Error("BootHeld(wf_1) = false after the restart, want the pending record boot-held (A27)")
			}
		},
	},
	{
		name: "61 none, a new rule's baseline poll, write retry: E10 writes the cursor and population",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			h := tc.pollerOn()
			h.runPage()
			h.poll()
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.file(r.ID)
			if es.cursor[Feed{Selector: testRepo}.RepoID()] == nil {
				t.Errorf("file cursor = %+v, want the baseline", es.cursor)
			}
			if got, want := es.raw.Population, launchrule.Population(r); got != want {
				t.Errorf("file population = %s, want the rule's %s", got, want)
			}
			tc.restart()
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes at the restart, want none: case 59", n)
			}
		},
	},
	{
		name:  "62 abandoned-cancelling from W1, newest, write-blocked, write retry lands: E10",
		spec:  newest,
		setup: namedUnwritten,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.failWrites(nil)
			tc.woken()
			tc.persist(r.ID)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			supersedeLanded(t, tc, r)
			if got := tc.writeBlock(r.ID); got != "" {
				t.Errorf("write block = %q, want it lifted", got)
			}
			if !tc.woken() {
				t.Error("the launcher was not woken")
			}
		},
	},
	{
		name:  "63 abandoned-cancelling from W1, newest, write-blocked, write retry fails: E10 then W2",
		spec:  newest,
		setup: namedUnwritten,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.woken()
			tc.persist(r.ID)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertNoStateFile(t, tc, r.ID)
			if got := tc.writeBlock(r.ID); got != writeBlockedText(r) {
				t.Errorf("write block = %q, want it kept", got)
			}
			if n := tc.log.CountExact(logMsgNotWritten); n != 2 {
				t.Errorf("%d ERROR %q lines, want 2: W1's and the retry's", n, logMsgNotWritten)
			}
			if tc.woken() {
				t.Error("the launcher was woken by a write that did not land")
			}
			tc.failWrites(nil)
			advance(writeTick)
			supersedeLanded(t, tc, r)
		},
	},
	{
		name:  "64 none, write-blocked, edit (filter) whose write lands: E1 lifts the block by W2's rule",
		setup: writeBlocked,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.BoundAbandons = 1 })
			tc.failWrites(nil)
			tc.woken()
			if n := edit(tc, r, 0, widenFilter); n != 1 {
				tc.t.Errorf("writes = %d, want one", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if got := tc.writeBlock(r.ID); got != "" {
				t.Errorf("write block = %q, want it lifted", got)
			}
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 {
				t.Errorf("count = %d, want 0", st.BoundAbandons)
			}
			if !tc.woken() {
				t.Error("the launcher was not woken")
			}
		},
	},
	{
		name: "64 none, write-blocked, edit (filter) whose write fails: the block stays",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			writeBlocked(tc, r)
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.Blocked = "session not idle" })
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			edit(tc, r, 0, widenFilter)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if got := tc.writeBlock(r.ID); got != writeBlockedText(r) {
				t.Errorf("write block = %q, want it kept: only a landed write lifts it", got)
			}
			if _, st := tc.memory(r.ID); st.Blocked != "" {
				t.Errorf("blocked = %q, want the edit to lift the other block", st.Blocked)
			}
		},
	},
	{
		name: "77 abandoned-cancelling, write-blocked, delete whose removal lands: E3 with A21, and no tick",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			abandonedCancelling(tc, r)
			writeBlocked(tc, r)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			before := tc.log.CountExact(logMsgNotWritten)
			advance(2 * writeTick)
			if n := tc.log.CountExact(logMsgNotWritten); n != before {
				t.Errorf("%d more ERROR %q lines after the delete, want none: the tick stopped", n-before,
					logMsgNotWritten)
			}
			assertNoStateFile(t, tc, r.ID)
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1", "wf_1"}) {
				t.Errorf("cancels = %v, want the bound's and E3's", got)
			}
		},
	},
}
