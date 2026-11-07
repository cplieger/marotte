package trigger

import (
	"errors"
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// leasedPending leaves wf_1 on disk as k1's pending attempt with its triggered lease, as a stop after the grant does.
func leasedPending(tc *testConfirmer, r *launchrule.Rule) {
	unconfirmed(tc, r)
	tc.lease(r.ID, "k1", "wf_1")
}

// bootedHeld is leasedPending, then a restart: wf_1 is boot-held.
func bootedHeld(tc *testConfirmer, r *launchrule.Rule) {
	leasedPending(tc, r)
	tc.restart()
}

// abandonedOnDisk leaves wf_1 on disk as k1's abandoned attempt whose cancel never ran.
func abandonedOnDisk(tc *testConfirmer, r *launchrule.Rule) {
	unconfirmed(tc, r)
	advance(launchConfirmBound + time.Second)
}

func restart(tc *testConfirmer, _ *launchrule.Rule) { tc.restart() }

// errRemoveFailed is a state file removal the disk refused.
var errRemoveFailed = errors.New("remove launch-state: device busy")

var bootCases = []attemptCase{
	{
		name:  "14 boot-held, run_start before engine ready: A3",
		setup: bootedHeld,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if tc.BootHeld("wf_1") {
				t.Error("BootHeld(wf_1) = true after its confirmation, want it out of the set")
			}
			es, _ := tc.file(r.ID)
			if len(es.queue) != 0 || len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" {
				t.Errorf("file queue %v, launched %+v; want k1 launched under wf_1", keysOf(es.queue), es.launched)
			}
			tc.run()
			tc.engineReady()
			advance(launchConfirmBound + time.Second)
			if ran, _ := tc.fx.calls(); len(ran) != 0 || len(tc.cancels()) != 0 {
				t.Errorf("cancels run %v, queued %v; want none", ran, tc.cancels())
			}
		},
	},
	{
		name: "15 boot-held, engine ready 7 minutes after the load, then run_start 10 s later: A7 then A3",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			bootedHeld(tc, r)
			tc.run()
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			advance(7 * time.Minute)
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateBootHeld || rec.bound != nil {
				tc.t.Fatalf("record before engine ready = %+v, want boot-held with no bound", rec)
			}
			tc.engineReady()
			synctest.Wait()
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.bound == nil {
				tc.t.Fatalf("record at engine ready = %+v, want its bound armed", rec)
			}
			advance(10 * time.Second)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			advance(launchConfirmBound)
			if ran, _ := tc.fx.calls(); len(ran) != 0 {
				t.Errorf("cancels = %v, want none", ran)
			}
			es, _ := tc.file(r.ID)
			if len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" {
				t.Errorf("file launched = %+v, want k1 confirmed under wf_1", es.launched)
			}
		},
	},
	{
		name: "16 boot-held, bound reached: A8",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			leasedPending(tc, r)
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.BoundAbandons = 1 })
			tc.persist(r.ID)
			tc.restart()
			tc.run()
		},
		deliver: func(tc *testConfirmer, _ *launchrule.Rule) {
			tc.engineReady()
			advance(launchConfirmBound + time.Second)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || !rec.state.abandoned() || rec.cause != causeBound {
				t.Fatalf("record = %+v, want abandoned, cause bound", rec)
			}
			_, st := tc.memory(r.ID)
			if st.BoundAbandons != 1 || st.FailingSince.IsZero() || st.Blocked != "" {
				t.Errorf("count %d, failing since %v, blocked %q; want the count unchanged at 1, the clock started, no block",
					st.BoundAbandons, st.FailingSince, st.Blocked)
			}
			if tc.BootHeld("wf_1") {
				t.Error("BootHeld(wf_1) = true after A8, want it out of the set")
			}
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one for wf_1", ran)
			}
			if !tc.relaunchable(r.ID, "k1") {
				t.Error("k1 is not launchable again after A8")
			}
		},
	},
	{
		name:    "49 pending on disk with a lease, boot: E7 with A27",
		setup:   leasedPending,
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if !tc.BootHeld("wf_1") {
				t.Error("BootHeld(wf_1) = false before the first frame, want true")
			}
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateBootHeld || rec.bound != nil {
				t.Errorf("record = %+v, want boot-held with no bound before engine ready", rec)
			}
			if n := tc.written(); n != 0 || len(tc.cancels()) != 0 {
				t.Errorf("%d writes, cancels %v; want no write and no cancel", n, tc.cancels())
			}
			if es, _ := tc.memory(r.ID); len(es.queue) != 1 || es.queue[0].Live != "wf_1" {
				t.Errorf("queue = %+v, want k1 naming its live record wf_1", es.queue)
			}
		},
	},
	{
		name: "51 pending on disk with no lease and a newer entry of its coalesce key, boot: E7 with A28 then Q",
		spec: newest,
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c1"))
			tc.name(r.ID, "k1", "wf_1")
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, attempts := tc.file(r.ID)
			if !slices.Equal(keysOf(es.queue), []string{"k2"}) ||
				!slices.Equal(es.superseded, []supersededEntry{{Key: "k1", By: "k2"}}) {
				t.Errorf("file queue %v, superseded %+v; want k2 alone and k1 superseded by it in the boot write",
					keysOf(es.queue), es.superseded)
			}
			if len(attempts) != 1 || attempts[0].State != persistedAbandoned || attempts[0].Cause != causeBootUnleased {
				t.Errorf("file attempts = %+v, want wf_1 abandoned with cause boot_unleased", attempts)
			}
			if n := tc.written(); n != 1 {
				t.Errorf("%d writes, want one boot write", n)
			}
			if es.raw.Status.BoundAbandons != 0 || !es.raw.Status.FailingSince.IsZero() {
				t.Errorf("status %+v, want nothing counted", es.raw.Status)
			}
			if ran, _ := tc.fx.calls(); len(ran) != 0 {
				t.Errorf("cancels run before engine ready = %v, want none", ran)
			}
			tc.run()
			tc.answerCancels(nil)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels after engine ready = %v, want one for wf_1", ran)
			}
			if !tc.relaunchable(r.ID, "k2") || tc.relaunchable(r.ID, "k1") {
				t.Error("want only k2 launchable")
			}
		},
	},
	{
		name:    "52 abandoned on disk, cancel not landed, boot: E7 with A29",
		setup:   abandonedOnDisk,
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cancelsOut != 1 {
				t.Fatalf("record = %+v, want abandoned-cancelling with its cancel owed again", rec)
			}
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes, want none", n)
			}
			if ran, _ := tc.fx.calls(); len(ran) != 0 {
				t.Errorf("cancels run before engine ready = %v, want none", ran)
			}
			tc.run()
			tc.answerCancels(nil)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels after engine ready = %v, want one for wf_1", ran)
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelled {
				t.Errorf("record after the answer = %+v, want abandoned-cancelled", rec)
			}
		},
	},
	{
		name: "52 abandoned on disk, cancel landed, boot: E7 with A30",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			abandonedOnDisk(tc, r)
			tc.withRecord(r.ID, "wf_1", func(rec *record) {
				rec.state, rec.cancelLandedAt, rec.cancelsOut = stateAbandonedCancelled, time.Now(), 0
			})
			tc.persist(r.ID)
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelled ||
				rec.cancelLandedAt.IsZero() {
				t.Fatalf("record = %+v, want abandoned-cancelled with its landing kept", rec)
			}
			tc.run()
			tc.answerCancels(nil)
			if ran, _ := tc.fx.calls(); len(ran) != 0 {
				t.Errorf("cancels = %v, want nothing sent", ran)
			}
		},
	},
	{
		name: "53 a second triggered lease on the rule and a queued key, boot: E7 with A31",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.persist(r.ID)
			tc.lease(r.ID, "k1", "wf_0")
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_0")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeBootStale {
				t.Fatalf("record = %+v, want wf_0 abandoned-cancelling, cause boot_stale", rec)
			}
			if _, attempts := tc.file(r.ID); len(attempts) != 1 || attempts[0].WorkflowID != "wf_0" {
				t.Errorf("file attempts = %+v, want the boot_stale record in E7's write", attempts)
			}
			if !slices.Equal(tc.cancels(), []string{"wf_0"}) {
				t.Errorf("cancels queued = %v, want wf_0", tc.cancels())
			}
			tc.ConfirmEvent(r.ID, "k1", "wf_0")
			if es, _ := tc.memory(r.ID); !slices.Equal(keysOf(es.queue), []string{"k1"}) || len(es.launched) != 0 {
				t.Errorf("after its run_start: queue %v, launched %+v; want k1 still queued, nothing confirmed",
					keysOf(es.queue), es.launched)
			}
			tc.run()
			tc.answerCancels(nil)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_0"}) {
				t.Errorf("cancels after engine ready = %v, want wf_0", ran)
			}
		},
	},
	{
		name: "54 a triggered lease whose key is in launched, boot: E7 with A36",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			invoking(tc, r)
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
			tc.lease(r.ID, "k1", "wf_1")
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if tc.BootHeld("wf_1") || tc.recordOf(r.ID, "wf_1") != nil {
				t.Error("wf_1 is boot-held or recorded, want neither")
			}
			if n := tc.written(); n != 0 || len(tc.cancels()) != 0 {
				t.Errorf("%d writes, cancels %v; want no write and no cancel", n, tc.cancels())
			}
		},
	},
	{
		name: "55 a leased pending record and a second lease on its key, the population changed, boot: A27, A31, then E2",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			leasedPending(tc, r)
			tc.lease(r.ID, "k1", "wf_0")
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.BoundAbandons = 1 })
			tc.persist(r.ID)
			moved := *r
			moved.When.Event = cloneSpec(r.When.Event)
			moveRepo(&moved)
			if _, _, err := tc.store.Replace(tc.t.Context(), r.ID, &moved, 1); err != nil {
				tc.t.Fatalf("Setup: Replace: %v", err)
			}
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			for wf, cause := range map[string]string{"wf_1": causeEdit, "wf_0": causeBootStale} {
				if rec := tc.recordOf(r.ID, wf); rec == nil || rec.state != stateAbandonedCancelling || rec.cause != cause {
					t.Errorf("record %s = %+v, want abandoned-cancelling, cause %s", wf, rec, cause)
				}
			}
			if tc.BootHeld("wf_1") {
				t.Error("BootHeld(wf_1) = true, want the rebaseline to take it out of the set")
			}
			es, _ := tc.file(r.ID)
			def, _ := tc.store.Get(r.ID)
			if len(es.queue) != 0 || len(es.launched) != 0 || len(es.superseded) != 0 ||
				es.raw.Status.BoundAbandons != 0 || es.raw.Population != launchrule.Population(&def) {
				t.Errorf("file queue %v, launched %+v, superseded %+v, status %+v, population %q; want the reset to the new population",
					keysOf(es.queue), es.launched, es.superseded, es.raw.Status, es.raw.Population)
			}
			if n := tc.written(); n != 1 {
				t.Errorf("%d writes, want one", n)
			}
			tc.run()
			tc.answerCancels(nil)
			ran, _ := tc.fx.calls()
			slices.Sort(ran)
			if !slices.Equal(ran, []string{"wf_0", "wf_1"}) {
				t.Errorf("cancels after engine ready = %v, want wf_0 and wf_1", ran)
			}
			tc.ConfirmEvent(r.ID, "k1", "wf_1")
			if es, _ := tc.memory(r.ID); len(es.launched) != 0 {
				t.Errorf("a later run_start confirmed %+v, want nothing", es.launched)
			}
		},
	},
	{
		name: "56 a state file whose rule is gone, a leased pending record and an abandoned one, boot: E7 with E3",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c2"))
			abandonedOnDisk(tc, r)
			tc.name(r.ID, "k2", "wf_2")
			tc.lease(r.ID, "k2", "wf_2")
			if _, err := tc.store.Delete(tc.t.Context(), r.ID); err != nil {
				tc.t.Fatalf("Setup: Delete: %v", err)
			}
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			assertNoStateFile(t, tc, r.ID)
			got := tc.cancels()
			slices.Sort(got)
			if !slices.Equal(got, []string{"wf_1", "wf_2"}) {
				t.Errorf("cancels queued = %v, want wf_1 and wf_2", got)
			}
			if tc.BootHeld("wf_2") {
				t.Error("BootHeld(wf_2) = true, want a deleted rule's record never boot-held")
			}
			tc.run()
			tc.answerCancels(nil)
			if _, ok := tc.loaded()[r.ID]; ok {
				t.Error("the deleted rule's value outlived its records' answers, want A32 to drop it")
			}
			assertNoStateFile(t, tc, r.ID)
		},
	},
	{
		name: "59 a completed baseline whose population is the rule's, boot: E7 with no E2",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.update(r.ID, anyGeneration, func(es *eventState) bool {
				es.cursor[Feed{Selector: testRepo}.RepoID()] = &repoCursor{Runs: &runCursor{BaselineAt: time.Now()}}
				return true
			})
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			before, _ := os.ReadFile(statePath(tc, r.ID))
			tc.restart()
			if after, _ := os.ReadFile(statePath(tc, r.ID)); string(after) != string(before) {
				tc.t.Error("the boot rewrote the state file, want it untouched")
			}
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if n := tc.written(); n != 0 || len(tc.cancels()) != 0 {
				t.Errorf("%d writes, cancels %v; want neither", n, tc.cancels())
			}
			snap, _, err := tc.snapshot(r.ID)
			if err != nil || snap.cursor[Feed{Selector: testRepo}.RepoID()] == nil {
				t.Errorf("loaded cursor = %+v (%v), want the baseline kept", snap.cursor, err)
			}
		},
	},
	{
		name: "60 a state file with an empty population and a cursor, boot: E7, not an E2",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.update(r.ID, anyGeneration, func(es *eventState) bool {
				es.cursor[Feed{Selector: testRepo}.RepoID()] = &repoCursor{Runs: &runCursor{BaselineAt: time.Now()}}
				return true
			})
			st, err := tc.store.State(r.ID)
			if err != nil {
				tc.t.Fatalf("Setup: State: %v", err)
			}
			st.Population = ""
			if err := tc.writes.StateWriter.WriteState(tc.t.Context(), r.ID, &st); err != nil {
				tc.t.Fatalf("Setup: WriteState: %v", err)
			}
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if n := tc.written(); n != 0 {
				t.Errorf("%d writes at the boot, want none", n)
			}
			snap, _, err := tc.snapshot(r.ID)
			if err != nil || snap.cursor[Feed{Selector: testRepo}.RepoID()] == nil || !slices.Equal(keysOf(snap.queue), []string{"k1"}) {
				t.Errorf("loaded cursor %+v, queue %v (%v); want both kept", snap.cursor, keysOf(snap.queue), err)
			}
			tc.persist(r.ID)
			es, _ := tc.file(r.ID)
			if es.raw.Population != launchrule.Population(r) {
				t.Errorf("next write's population = %q, want the rule's %q", es.raw.Population, launchrule.Population(r))
			}
		},
	},
	{
		name: "65 a newest rule's file with three queued entries of one coalesce key, boot: E7's Q",
		spec: newest,
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c1"), held("k3", "c1"))
			tc.persist(r.ID)
		},
		deliver: restart,
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.file(r.ID)
			want := []supersededEntry{{Key: "k1", By: "k3"}, {Key: "k2", By: "k3"}}
			if !slices.Equal(keysOf(es.queue), []string{"k3"}) || !slices.Equal(es.superseded, want) {
				t.Errorf("file queue %v, superseded %+v; want k3 alone and both older keys naming it", keysOf(es.queue), es.superseded)
			}
			if n := tc.written(); n != 1 {
				t.Errorf("%d writes, want the one boot write", n)
			}
			tc.name(r.ID, "k3", "wf_3")
			tc.ConfirmEvent(r.ID, "k3", "wf_3")
			es, _ = tc.file(r.ID)
			for _, s := range es.superseded {
				if s.WorkflowID != "wf_3" {
					t.Errorf("superseded %+v, want it to gain wf_3", s)
				}
			}
		},
	},
	{
		name: "71 no record, delete with the removal failing: E3, then the next boot's E3",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.persist(r.ID)
			tc.writes.mu.Lock()
			tc.writes.removeErr = errRemoveFailed
			tc.writes.mu.Unlock()
		},
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			deleteRule(tc.t, tc.Confirmer, r.ID)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if ops := tc.log.AttrValuesExact(logMsgNotWritten, "op"); !slices.Equal(ops, []string{"remove"}) {
				t.Errorf("ERROR %q op values = %v, want one op=remove", logMsgNotWritten, ops)
			}
			if _, err := os.Stat(statePath(tc, r.ID)); err != nil {
				t.Fatalf("state file after the failed removal: %v, want it still there", err)
			}
			tc.restart()
			assertNoStateFile(t, tc, r.ID)
		},
	},
}

func cloneSpec(ev *launchrule.EventSpec) *launchrule.EventSpec {
	c := *ev
	return &c
}
