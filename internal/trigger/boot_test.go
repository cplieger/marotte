package trigger

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/runlease"
)

// The boot sweep reads BootHeld as soon as the engine's first frame can arrive, so the load is done when
// NewConfirmer returns, before Run and before the engine is ready.
func TestBootLoadTakenBeforeSweep(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		leasedPending(tc, &r)
		tc.restart()
		if !tc.BootHeld("wf_1") {
			t.Error("BootHeld(wf_1) = false when NewConfirmer returned, want true")
		}
		if tc.BootHeld("wf_other") {
			t.Error("BootHeld(wf_other) = true for a run no record names, want false")
		}
	})
}

func TestBootLoadRunStartConfirms(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		leasedPending(tc, &r)
		tc.restart()
		tc.run()
		tc.engineReady()
		advance(time.Minute)
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		advance(launchConfirmBound)
		es, attempts := tc.file(r.ID)
		if len(es.queue) != 0 || len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" || len(attempts) != 0 {
			t.Errorf("file queue %v, launched %+v, attempts %+v; want k1 confirmed under wf_1 and no record",
				keysOf(es.queue), es.launched, attempts)
		}
		if ran, _ := tc.fx.calls(); len(ran) != 0 {
			t.Errorf("cancels = %v, want none", ran)
		}
	})
}

func TestBootLoadNoRunStartRelaunches(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		leasedPending(tc, &r)
		tc.restart()
		tc.run()
		tc.engineReady()
		tc.woken()
		advance(launchConfirmBound + time.Second)
		if !tc.relaunchable(r.ID, "k1") {
			t.Error("k1 is not launchable once more after the boot-held bound")
		}
		if !tc.woken() {
			t.Error("the launcher was not woken")
		}
		if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
			t.Errorf("cancels = %v, want the boot-held run's", ran)
		}
	})
}

// A boot-held entry is its key's attempt, so a newest rule's poll queues a newer match beside it rather than
// superseding it.
func TestBootLoadEntriesHeldBeforeFirstPoll(t *testing.T) {
	ev := runSpec("success")
	ev.Hold = &launchrule.Hold{Policy: launchrule.HoldNewest, Cap: 10}
	h := newHarness(t)
	leases := runlease.NewMemory()
	h.leases = leases
	id := h.create(ev).ID
	h.runPage()
	h.poll()
	created := pollStart.Add(time.Minute)
	h.runPage(run(41, forgeapi.CheckPassing, created))
	h.poll()
	k41 := h.queued(id)[0].Key
	if err := h.c.NameAttempt(id, k41, "wf_41"); err != nil {
		t.Fatalf("Setup: NameAttempt: %v", err)
	}
	if err := leases.Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_41", Recipe: "weekly", Origin: runlease.OriginTriggered, RuleID: id, EventKey: k41,
	}); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	h.reopen()
	if !h.c.BootHeld("wf_41") {
		t.Fatal("wf_41 is not boot-held after the reopen")
	}
	h.runPage(run(42, forgeapi.CheckPassing, created.Add(time.Minute)))
	h.poll()
	es := h.state(id)
	if len(es.queue) != 2 || es.queue[0].Key != k41 || es.queue[0].Live != "wf_41" || len(es.superseded) != 0 {
		t.Errorf("after the first poll: queue %+v, superseded %+v; want 42 queued beside the boot-held 41", es.queue, es.superseded)
	}
}

// The record on disk is the newest attempt of its key, so an older triggered lease on the same key is abandoned
// and only the record's run is boot-held.
func TestBootLoadTwoLeasesOneKeyKeepsNewest(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.lease(r.ID, "k1", "wf_old")
		leasedPending(tc, &r)
		tc.restart()
		if !tc.BootHeld("wf_1") || tc.BootHeld("wf_old") {
			t.Errorf("BootHeld wf_1 %v, wf_old %v; want only the record's run held", tc.BootHeld("wf_1"), tc.BootHeld("wf_old"))
		}
		if rec := tc.recordOf(r.ID, "wf_old"); rec == nil || rec.cause != causeBootStale {
			t.Errorf("wf_old record = %+v, want abandoned boot_stale", rec)
		}
		if es, _ := tc.memory(r.ID); len(es.queue) != 1 || es.queue[0].Live != "wf_1" {
			t.Errorf("queue = %+v, want k1 naming wf_1", es.queue)
		}
	})
}

func TestBootUnleasedAttemptCancelled(t *testing.T) {
	t.Run("the run is cancelled after engine ready and its key queued again", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			invoking(tc, &r)
			tc.restart()
			tc.run()
			advance(time.Minute)
			if ran, _ := tc.fx.calls(); len(ran) != 0 {
				t.Errorf("cancels before engine ready = %v, want none", ran)
			}
			tc.answerCancels(nil)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels after engine ready = %v, want wf_1", ran)
			}
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.cause != causeBootUnleased {
				t.Errorf("record = %+v, want cause boot_unleased", rec)
			}
			if !tc.relaunchable(r.ID, "k1") {
				t.Error("k1 is not launchable again")
			}
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 || !st.FailingSince.IsZero() {
				t.Errorf("status %+v, want nothing counted", st)
			}
		})
	})
	t.Run("the event launches again after the backoff", func(t *testing.T) { t.Skip("needs 5.9") })
}

func TestBootBoundStartsAtEngineReady(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		leasedPending(tc, &r)
		tc.restart()
		tc.run()
		advance(2 * launchConfirmBound)
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateBootHeld {
			t.Fatalf("record 10 minutes before engine ready = %+v, want still boot-held", rec)
		}
		tc.engineReady()
		advance(launchConfirmBound - time.Second)
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateBootHeld {
			t.Fatalf("record just inside the bound = %+v, want still boot-held", rec)
		}
		advance(2 * time.Second)
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || !rec.state.abandoned() {
			t.Errorf("record past the bound from engine ready = %+v, want abandoned", rec)
		}
	})
}

func TestBootCancelsWaitForEngineReady(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		abandonedOnDisk(tc, &r)
		tc.restart()
		tc.run()
		advance(time.Hour)
		if ran, _ := tc.fx.calls(); len(ran) != 0 {
			t.Errorf("cancels before engine ready = %v, want none", ran)
		}
		tc.answerCancels(nil)
		if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
			t.Errorf("cancels after engine ready = %v, want the owed one", ran)
		}
	})
}

// A stop between a rebaselining PUT's definitions write and its state write leaves the old population's state;
// the boot load applies the rebaseline the stop interrupted.
func TestPopulationMismatchAppliesRebaseline(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		minted := time.Now().Add(-time.Hour)
		tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c2"))
		tc.setStatus(r.ID, func(st *launchrule.Status) { st.LastMinted, st.BoundAbandons = minted, 1 })
		tc.persist(r.ID)
		moved := r
		moved.When.Event = cloneSpec(r.When.Event)
		moveRepo(&moved)
		if _, _, err := tc.store.Replace(t.Context(), r.ID, &moved, 2); err != nil {
			t.Fatalf("Setup: Replace: %v", err)
		}
		tc.restart()
		es, _ := tc.file(r.ID)
		st := es.raw.Status
		if len(es.queue) != 0 || es.raw.Population != launchrule.Population(&moved) || st.BoundAbandons != 0 ||
			!st.LastMinted.Equal(minted) {
			t.Errorf("file queue %v, population %q, status %+v; want the reset to %q with last_minted kept",
				keysOf(es.queue), es.raw.Population, st, launchrule.Population(&moved))
		}
	})
}

func TestOrphanStateFileCancelsItsRuns(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		kept := tc.createRule(runSpec("failure"))
		tc.persist(kept.ID)
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.name(r.ID, "k1", "wf_1")
		tc.lease(r.ID, "k1", "wf_1")
		if _, err := tc.store.Delete(t.Context(), r.ID); err != nil {
			t.Fatalf("Setup: Delete: %v", err)
		}
		const clockOrphan = "AAAAAAAAAAAAAAAAAAAAAA"
		if err := os.WriteFile(statePath(tc, clockOrphan), []byte(`{"status":{"last_run_id":"wf_c"}}`), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		tc.restart()
		assertNoStateFile(t, tc, r.ID)
		assertNoStateFile(t, tc, clockOrphan)
		if _, err := os.Stat(statePath(tc, kept.ID)); err != nil {
			t.Errorf("the live rule's state file: %v, want it kept", err)
		}
		tc.run()
		tc.answerCancels(nil)
		if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
			t.Errorf("cancels = %v, want the orphan's run cancelled", ran)
		}
	})
}
