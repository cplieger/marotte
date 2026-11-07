package trigger

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// The rule rows' lines of the design's test table, run by TestAttemptTable.

// liveStates are the three pending states, each set up as wf_1 holding the queued key k1.
var liveStates = []struct {
	setup func(tc *testConfirmer, r *launchrule.Rule)
	state attemptState
}{
	{invoking, stateInvoking},
	{unconfirmed, stateInvokedUnconfirmed},
	{bootHeldRecord, stateBootHeld},
}

// bootHeldRecord holds k1 under a boot-held wf_1 whose bound is not armed, as A27 loads it.
func bootHeldRecord(tc *testConfirmer, r *launchrule.Rule) {
	tc.enqueue(r.ID, held("k1", "c1"))
	tc.seedRecord(r.ID, &record{workflowID: "wf_1", key: "k1", coalesceKey: "c1", state: stateBootHeld, at: time.Now()})
}

// abandonedStates are the two abandoned states of wf_1, its key k1 queued again by Q and its count at 1.
var abandonedStates = []struct {
	setup func(tc *testConfirmer, r *launchrule.Rule)
	state attemptState
}{
	{abandonedCancelling, stateAbandonedCancelling},
	{abandonedCancelled, stateAbandonedCancelled},
}

// widenFilter is a filter edit: the rule matches failures too.
func widenFilter(r *launchrule.Rule) { r.When.Event.Filter.Result = []string{"success", "failure"} }

// moveRepo is a rebaseline edit: the rule watches another repository.
func moveRepo(r *launchrule.Rule) { r.When.Event.Repo = "cplieger/other" }

// edit applies fn through the route's path, failing the test on a refusal, and answers the writes it made.
func edit(tc *testConfirmer, r *launchrule.Rule, confirmDrop int, fn func(*launchrule.Rule)) int {
	tc.t.Helper()
	tc.written()
	if err := changeRule(tc.t, tc.Confirmer, r.ID, confirmDrop, fn); err != nil {
		tc.t.Fatalf("edit: %v", err)
	}
	return tc.written()
}

func liveCases() []attemptCase {
	var out []attemptCase
	for _, ls := range liveStates {
		out = append(out,
			attemptCase{
				name:  fmt.Sprintf("18 %s, edit (filter): E1 with A11", ls.state),
				setup: func(tc *testConfirmer, r *launchrule.Rule) { ls.setup(tc, r); failing(tc, r) },
				deliver: func(tc *testConfirmer, r *launchrule.Rule) {
					if n := edit(tc, r, 0, widenFilter); n != 1 {
						tc.t.Errorf("writes = %d, want one", n)
					}
				},
				check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
					if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != ls.state {
						t.Errorf("record = %+v, want it kept, %s", rec, ls.state)
					}
					if got := tc.cancels(); len(got) != 0 {
						t.Errorf("cancels = %v, want none: the bound decides", got)
					}
					if _, st := tc.memory(r.ID); st.BoundAbandons != 0 || !st.FailingSince.IsZero() {
						t.Errorf("count %d, failing since %v; want 0 and zero", st.BoundAbandons, st.FailingSince)
					}
				},
			},
			attemptCase{
				name:  fmt.Sprintf("22 %s, cancel ok, run gone and ttl: A26", ls.state),
				setup: ls.setup,
				deliver: func(tc *testConfirmer, r *launchrule.Rule) {
					tc.written()
					tc.deliver(r.ID, &attemptEvent{kind: evCancelOK, workflowID: "wf_1"})
					tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{"wf_1": marotte.RunStatusAborted})
					tc.deliver(r.ID, &attemptEvent{kind: evTTL})
					if n := tc.written(); n != 0 {
						tc.t.Errorf("writes = %d, want none", n)
					}
				},
				check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
					rec := tc.recordOf(r.ID, "wf_1")
					if rec == nil || rec.state != ls.state {
						t.Fatalf("record = %+v, want it unchanged, %s", rec, ls.state)
					}
					if armed := rec.bound != nil; armed != (ls.state != stateBootHeld) {
						t.Errorf("bound armed = %v, want it as it was", armed)
					}
				},
			},
			attemptCase{
				name: fmt.Sprintf("46 %s, count 1, pause: E8 with A25, then run_start confirms", ls.state),
				setup: func(tc *testConfirmer, r *launchrule.Rule) {
					ls.setup(tc, r)
					tc.setStatus(r.ID, func(st *launchrule.Status) { st.BoundAbandons = 1 })
				},
				deliver: func(tc *testConfirmer, r *launchrule.Rule) {
					if n := edit(tc, r, 0, func(r *launchrule.Rule) { r.Enabled = false }); n != 0 {
						tc.t.Errorf("writes = %d, want none", n)
					}
					if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != ls.state {
						tc.t.Errorf("record after the pause = %+v, want it unchanged, %s", rec, ls.state)
					}
					if _, st := tc.memory(r.ID); st.BoundAbandons != 1 {
						tc.t.Errorf("count after the pause = %d, want 1 kept", st.BoundAbandons)
					}
					tc.ConfirmEvent(r.ID, "k1", "wf_1")
				},
				check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
					es, _ := tc.memory(r.ID)
					if len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" {
						t.Errorf("launched = %+v, want k1 confirmed under wf_1", es.launched)
					}
				},
			},
		)
	}
	return out
}

func abandonedCases() []attemptCase {
	var out []attemptCase
	for _, as := range abandonedStates {
		out = append(out,
			attemptCase{
				name:  fmt.Sprintf("35 %s, edit (filter): E1 with A20", as.state),
				setup: as.setup,
				deliver: func(tc *testConfirmer, r *launchrule.Rule) {
					if n := edit(tc, r, 0, widenFilter); n != 1 {
						tc.t.Errorf("writes = %d, want one", n)
					}
				},
				check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
					if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != as.state {
						t.Errorf("record = %+v, want it kept, %s", rec, as.state)
					}
					if _, st := tc.memory(r.ID); st.BoundAbandons != 0 {
						t.Errorf("count = %d, want 0", st.BoundAbandons)
					}
					if _, file := tc.file(r.ID); len(file) != 1 || file[0].WorkflowID != "wf_1" {
						t.Errorf("file attempts = %+v, want wf_1 kept", file)
					}
				},
			},
			attemptCase{
				name:  fmt.Sprintf("36 %s, edit (rebaseline): E2 with A20", as.state),
				setup: as.setup,
				deliver: func(tc *testConfirmer, r *launchrule.Rule) {
					if n := edit(tc, r, 1, moveRepo); n != 1 {
						tc.t.Errorf("writes = %d, want one", n)
					}
				},
				check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
					if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != as.state || rec.cause != causeBound {
						t.Errorf("record = %+v, want it kept through the reset, %s, cause bound", rec, as.state)
					}
					es, file := tc.file(r.ID)
					if len(es.queue) != 0 || len(file) != 1 {
						t.Errorf("file queue %v, attempts %+v; want the queue reset and wf_1 kept", keysOf(es.queue), file)
					}
				},
			},
		)
	}
	return out
}

var ruleRowCases = slices.Concat(liveCases(), abandonedCases(), []attemptCase{
	{
		name:  "19 invoking, edit (rebaseline), then an in-band refusal and cancel ok 1 s later: E2 with A9, A15, R",
		setup: func(tc *testConfirmer, r *launchrule.Rule) { tc.run(); invoking(tc, r); failing(tc, r) },
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if n := edit(tc, r, 1, moveRepo); n != 1 {
				tc.t.Errorf("writes = %d, want the reset's one", n)
			}
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeEdit || !rec.owesRelease {
				tc.t.Fatalf("record = %+v, want abandoned-cancelling, cause edit, owes_release", rec)
			}
			es, file := tc.file(r.ID)
			if len(es.queue) != 0 || len(file) != 1 || file[0].Cause != causeEdit || file[0].State != persistedAbandoned {
				tc.t.Errorf("file queue %v, attempts %+v; want the queue emptied and the edit record in one write",
					keysOf(es.queue), file)
			}
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 {
				tc.t.Errorf("count = %d, want 0", st.BoundAbandons)
			}
			tc.LaunchReturned(r.ID, "wf_1", refusal)
			advance(time.Second)
			tc.answerCancels(nil)
		},
		check: releasedOnce,
	},
	{
		name:  "20 invoking, delete, then invoke reply lost: E3 with A10, A15, R, A32, then a run_start is A23",
		setup: func(tc *testConfirmer, r *launchrule.Rule) { tc.run(); invoking(tc, r) },
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
			assertNoStateFile(tc.t, tc, r.ID)
			tc.LaunchReturned(r.ID, "wf_1", context.DeadlineExceeded)
			tc.answerCancels(nil)
			advance(2 * time.Second)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if cancels, releases := tc.fx.calls(); !slices.Equal(cancels, []string{"wf_1"}) ||
				!slices.Equal(releases, []string{"wf_1"}) {
				t.Errorf("cancels %v, releases %v; want one of each", cancels, releases)
			}
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the deleted rule's value is kept, want it dropped by A32")
			}
			assertNoStateFile(t, tc, r.ID)
			if n := tc.log.CountExact(logMsgRunStarted); n != 0 {
				t.Errorf("%d INFO %q lines, want none: the run_start confirms nothing", n, logMsgRunStarted)
			}
		},
	},
	{
		name: "32 21 abandoned records gone and 1 active, run gone: E5 with A18 and A37",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			landed := time.Now().Add(-abandonedTTL - time.Hour)
			for i := range 22 {
				tc.seedRecord(r.ID, &record{
					workflowID: fmt.Sprintf("wf_%d", i), key: fmt.Sprintf("k%d", i), state: stateAbandonedCancelled,
					cause: causeBound, at: landed, cancelLandedAt: landed,
				})
			}
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			statuses := map[string]marotte.RunStatus{"wf_21": marotte.RunStatusRunning}
			for i := range 10 {
				statuses[fmt.Sprintf("wf_%d", i)] = marotte.RunStatusAborted
			}
			tc.written()
			tc.PruneAbandoned(r.ID, statuses)
			if n := tc.written(); n != 1 {
				tc.t.Errorf("writes = %d, want one", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, file := tc.file(r.ID); len(file) != 1 || file[0].WorkflowID != "wf_21" {
				t.Errorf("file attempts = %+v, want only the active wf_21", file)
			}
		},
	},
	{
		name:  "33 abandoned-cancelling in flight, run gone: E5 with A37",
		setup: abandonedInFlight,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.written()
			tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{})
			if n := tc.written(); n != 0 {
				tc.t.Errorf("writes = %d, want none", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelling {
				t.Errorf("record = %+v, want it kept", rec)
			}
		},
	},
	{
		name: "34 two landed 49 hours ago, one of them owing a release, and one cancelling 10 days old, ttl: E6",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			now := time.Now()
			landed := now.Add(-49 * time.Hour)
			tc.seedRecord(r.ID, &record{workflowID: "wf_1", key: "k1", state: stateAbandonedCancelled, at: landed, cancelLandedAt: landed})
			tc.seedRecord(r.ID, &record{
				workflowID: "wf_2", key: "k2", state: stateAbandonedCancelled, at: landed, cancelLandedAt: landed, owesRelease: true,
			})
			tc.seedRecord(r.ID, &record{workflowID: "wf_3", key: "k3", state: stateAbandonedCancelling, at: now.Add(-240 * time.Hour)})
			tc.run()
			tc.engineReady()
		},
		deliver: func(tc *testConfirmer, _ *launchrule.Rule) {
			tc.written()
			advance(ttlInterval)
			if n := tc.written(); n != 1 {
				tc.t.Errorf("writes = %d, want one", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, file := tc.file(r.ID); len(file) != 2 || file[0].WorkflowID != "wf_2" || file[1].WorkflowID != "wf_3" {
				t.Errorf("file attempts = %+v, want wf_2 and wf_3 kept, wf_1 dropped", file)
			}
		},
	},
	{
		name:  "37 abandoned-cancelling, delete, then the cancels answer: E3 with A21, then A32",
		setup: abandonedCancelling,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
			assertNoStateFile(tc.t, tc, r.ID)
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1", "wf_1"}) {
				tc.t.Errorf("cancels = %v, want the bound's and one more", got)
			}
			tc.answerCancels(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the deleted rule's value is kept after its answers, want it dropped")
			}
			assertNoStateFile(t, tc, r.ID)
		},
	},
	{
		name: "40 none, confirmed then rebaselined, invoke reply lost: A23",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.run()
			invoking(tc, r)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
			edit(tc, r, 0, moveRepo)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.LaunchReturned(r.ID, "wf_1", context.DeadlineExceeded)
			tc.answerCancels(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if cancels, releases := tc.fx.calls(); len(cancels) != 0 || len(releases) != 0 {
				t.Errorf("cancels %v, releases %v; want none: the run keeps its lease and host session", cancels, releases)
			}
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 || !st.FailingSince.IsZero() {
				t.Errorf("count %d, failing since %v; want nothing counted", st.BoundAbandons, st.FailingSince)
			}
		},
	},
	{
		name: "43 no record, count 2, blocked by A6, edit (filter): E1, then one abandon does not block",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.setStatus(r.ID, func(st *launchrule.Status) {
				st.BoundAbandons, st.Blocked, st.RowText = 2, twiceUnreported("wf_0"), twiceUnreported("wf_0")
			})
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if n := edit(tc, r, 0, widenFilter); n != 1 {
				tc.t.Errorf("writes = %d, want one", n)
			}
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 || st.Blocked != "" || st.RowText != "" {
				tc.t.Errorf("after the edit: count %d, blocked %q, row %q; want 0 and the block lifted",
					st.BoundAbandons, st.Blocked, st.RowText)
			}
			if !tc.woken() {
				tc.t.Error("the launcher was not woken")
			}
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.abandonAtBound(r.ID, "k1", "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, st := tc.memory(r.ID); st.BoundAbandons != 1 || st.Blocked != "" {
				t.Errorf("count %d, blocked %q; want 1 and no block", st.BoundAbandons, st.Blocked)
			}
		},
	},
	{
		name: "44 no record, three queued keys, edit (rebaseline): E2",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			minted := time.Now().Add(-time.Hour)
			tc.update(r.ID, anyGeneration, func(es *eventState) bool {
				es.cursor[Feed{Selector: testRepo}.RepoID()] = &repoCursor{Runs: &runCursor{BaselineAt: minted}}
				es.queue = []queueEntry{held("k1", "c1"), held("k2", "c2"), held("k3", "c3")}
				es.launched = []launchedEntry{{Key: "k0", WorkflowID: "wf_0"}}
				es.superseded = []supersededEntry{{Key: "k-1", By: "k0"}}
				es.raw.Status.LastMinted, es.raw.Status.LastRunID, es.raw.Status.BoundAbandons = minted, "wf_0", 1
				return true
			})
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if n := edit(tc, r, 3, moveRepo); n != 1 {
				tc.t.Errorf("writes = %d, want one", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.file(r.ID)
			if len(es.cursor) != 0 || len(es.queue) != 0 || len(es.launched) != 0 || len(es.superseded) != 0 {
				t.Errorf("file = %+v, want cursor, queue, launched and superseded reset", es)
			}
			st := es.raw.Status
			if st.BoundAbandons != 0 || st.LastRunID != "wf_0" || !st.LastMinted.Equal(time.Now().Add(-time.Hour)) {
				t.Errorf("status = %+v, want count 0, last_run_id and last_minted kept", st)
			}
			def, _ := tc.store.Get(r.ID)
			if es.raw.Population != launchrule.Population(&def) || es.raw.Population == "" {
				t.Errorf("population = %q, want the new when's %q", es.raw.Population, launchrule.Population(&def))
			}
		},
	},
	{
		name: "45 no record, count 1, blocked by the 24-hour clock, resume: E4",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			edit(tc, r, 0, func(r *launchrule.Rule) { r.Enabled = false })
			tc.setStatus(r.ID, func(st *launchrule.Status) {
				st.BoundAbandons, st.FailingSince, st.Blocked = 1, time.Now().Add(-25*time.Hour), "session not idle"
			})
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			if n := edit(tc, r, 0, func(r *launchrule.Rule) { r.Enabled = true }); n != 1 {
				tc.t.Errorf("writes = %d, want one", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.file(r.ID)
			if st := es.raw.Status; st.BoundAbandons != 0 || st.Blocked != "" || !st.FailingSince.IsZero() {
				t.Errorf("status = %+v, want count 0, the block lifted, failing_since zero", st)
			}
			if !tc.woken() {
				t.Error("the launcher was not woken")
			}
		},
	},
	{
		name: "47 no record, delete: E3",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.update(r.ID, anyGeneration, func(es *eventState) bool { es.queue = []queueEntry{held("k1", "c1")}; return true })
			if _, err := os.Stat(statePath(tc, r.ID)); err != nil {
				tc.t.Fatalf("Setup: no state file: %v", err)
			}
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) { deleteRule(tc.t, tc.Confirmer, r.ID) },
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertNoStateFile(t, tc, r.ID)
		},
	},
	{
		name: "48 no record and no blocker, run gone and ttl: E5 and E6",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.written()
			tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{})
			tc.deliver(r.ID, &attemptEvent{kind: evTTL})
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if n := tc.written(); n != 0 {
				t.Errorf("writes = %d, want none", n)
			}
			assertNoStateFile(t, tc, r.ID)
		},
	},
	{
		name:  "72 abandoned-cancelled, not in flight, delete: E3 with A21",
		setup: abandonedCancelled,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
			synctest.Wait()
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertNoStateFile(t, tc, r.ID)
			if cancels, _ := tc.fx.calls(); !slices.Equal(append(cancels, tc.cancels()...), []string{"wf_1"}) {
				t.Errorf("cancels = %v, want only the first: KAS took it", cancels)
			}
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the record and the rule's value are kept, want both gone")
			}
		},
	},
	{
		name: "73 abandoned-cancelled, in flight and owing a release, delete, then the call returns: A21, A15, R, A32",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			abandonedInFlight(tc, r)
			tc.answerCancels(nil)
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || !rec.unpersisted {
				tc.t.Fatalf("record after the delete = %+v, want it unpersisted", rec)
			}
			tc.LaunchReturned(r.ID, "wf_1", nil)
			synctest.Wait()
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if _, releases := tc.fx.calls(); !slices.Equal(releases, []string{"wf_1"}) {
				t.Errorf("releases = %v, want one for wf_1", releases)
			}
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the record and the rule's value are kept, want both gone")
			}
			assertNoStateFile(t, tc, r.ID)
		},
	},
	{
		name: "78 none, a fifo rule with three queued entries of one coalesce key, edit to newest: E1's Q",
		spec: func() *launchrule.EventSpec { return fifo(runSpec("success"), 10) },
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c1"), held("k3", "c1"))
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			n := edit(tc, r, 0, func(r *launchrule.Rule) {
				r.When.Event.Hold = &launchrule.Hold{Policy: launchrule.HoldNewest, Cap: 10}
			})
			if n != 1 {
				tc.t.Errorf("writes = %d, want E1's one", n)
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.file(r.ID)
			want := []supersededEntry{{Key: "k1", By: "k3"}, {Key: "k2", By: "k3"}}
			if !slices.Equal(keysOf(es.queue), []string{"k3"}) || !slices.Equal(es.superseded, want) {
				t.Errorf("file queue %v, superseded %+v; want k3 alone and %+v", keysOf(es.queue), es.superseded, want)
			}
			if !tc.relaunchable(r.ID, "k3") || tc.relaunchable(r.ID, "k1") || tc.relaunchable(r.ID, "k2") {
				t.Error("want only k3 launchable")
			}
		},
	},
})
