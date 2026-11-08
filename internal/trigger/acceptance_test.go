package trigger

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// The acceptance cases of the trigger design's two examples, end to end: a real store and Confirmer, the Runner
// polling a fake forge, and the launcher starting runs on a fake engine.

const weeklyRun = 41414016210

func weeklyEventID(id int64) string {
	return fmt.Sprintf("github.com/%s/run/%d/success", weeklyRepo, id)
}

func runIDs(ls []launch) []string {
	out := make([]string, 0, len(ls))
	for i := range ls {
		out = append(out, ls[i].inputs[inputRunID])
	}
	return out
}

func launchedIssues(ls []launch) []string {
	out := make([]string, 0, len(ls))
	for i := range ls {
		out = append(out, ls[i].inputs[inputIssueNumber])
	}
	return out
}

// watching boots a process over the Weekly rule, its baseline taken, and a Weekly run created a minute later that
// the next polls read as pending.
func watching(a *acceptance, id int64) (launchrule.Rule, time.Time) {
	r := a.weeklyRule()
	a.boot()
	advance(time.Minute)
	created := time.Now()
	a.setRun(weekly(id, forgeapi.CheckPending, created))
	advance(10 * time.Minute)
	return r, created
}

func TestAC1_1(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		r, created := watching(a, weeklyRun)
		advance(2 * time.Hour)
		a.setRun(weekly(weeklyRun, forgeapi.CheckPassing, created))
		ended := time.Now()
		advance(pollTick + time.Second)
		got := a.eng.launches()
		if len(got) != 1 {
			t.Fatalf("launches = %+v, want one within one interval of the success", got)
		}
		if took := got[0].at.Sub(ended); took > pollTick {
			t.Errorf("launched %v after the success, want within one interval", took)
		}
		in := got[0].inputs
		if in["wait"] != "off" || in[inputRunID] != strconv.Itoa(weeklyRun) || in["event_id"] != weeklyEventID(weeklyRun) {
			t.Errorf("inputs = %v, want wait=off, run_id %d and event_id %s", in, weeklyRun, weeklyEventID(weeklyRun))
		}
		if label := EventLabel(in); label != "run "+strconv.Itoa(weeklyRun) {
			t.Errorf("run label = %q, want run %d", label, weeklyRun)
		}
		advance(24 * time.Hour)
		if n := len(a.eng.launches()); n != 1 {
			t.Errorf("%d launches a day later, want the one", n)
		}
		if st := a.view(r.ID).Status; st.LastRunID != got[0].wf {
			t.Errorf("row's last run = %q, want %s", st.LastRunID, got[0].wf)
		}
	})
}

func TestAC1_2(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		_, created := watching(a, 7001)
		a.setRun(weekly(7001, forgeapi.CheckFailing, created))
		advance(time.Hour)
		if got := a.eng.launches(); len(got) != 0 {
			t.Fatalf("launches after the failure = %+v, want none", got)
		}
		a.setRun(weekly(7001, forgeapi.CheckPending, created))
		advance(time.Hour)
		a.setRun(weekly(7001, forgeapi.CheckPassing, created))
		advance(pollTick + time.Second)
		advance(24 * time.Hour)
		got := a.eng.launches()
		if !eq(runIDs(got), []string{"7001"}) {
			t.Fatalf("launched run ids = %v, want 7001 once, on the re-run's success", runIDs(got))
		}
		if id := got[0].inputs["event_id"]; id != weeklyEventID(7001) {
			t.Errorf("event_id = %q, want %s", id, weeklyEventID(7001))
		}
	})
}

func TestAC1_3(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		before := time.Now().Add(-3 * time.Hour)
		a.setRun(weekly(7001, forgeapi.CheckPassing, before))
		a.setRun(weekly(7002, forgeapi.CheckFailing, before.Add(time.Minute)))
		a.weeklyRule()
		a.boot()
		advance(24 * time.Hour)
		if got := a.eng.launches(); len(got) != 0 {
			t.Errorf("launches = %+v, want none for runs concluded before the save", got)
		}
	})
}

func TestAC1_4(t *testing.T) {
	t.Run("down 48 hours", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, created := watching(a, 7001)
			a.restart()
			a.setRun(weekly(7001, forgeapi.CheckPassing, created))
			advance(48 * time.Hour)
			a.boot()
			if got := a.eng.launches(); !eq(runIDs(got), []string{"7001"}) {
				t.Errorf("launched run ids after the first poll = %v, want 7001", runIDs(got))
			}
			advance(time.Hour)
			if n := len(a.eng.launches()); n != 1 {
				t.Errorf("%d launches an hour on, want the one", n)
			}
		})
	})
	t.Run("down 80 hours", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, created := watching(a, 7001)
			a.restart()
			a.setRun(weekly(7001, forgeapi.CheckPassing, created))
			advance(80 * time.Hour)
			a.boot()
			advance(time.Hour)
			if got := a.eng.launches(); len(got) != 0 {
				t.Errorf("launches = %+v, want none past the horizon", got)
			}
			wantAgedOut(t, a, r.ID, 7001)
		})
	})
}

// wantAgedOut checks the row and the log name run id as still running at its last check and aged out.
func wantAgedOut(t *testing.T, a *acceptance, ruleID string, id int64) {
	t.Helper()
	gaps := a.view(ruleID).Status.Gaps
	if !slices.ContainsFunc(gaps, func(g launchrule.Gap) bool {
		return g.Cause == launchrule.GapAgedOut && g.RunID == id && !g.QueueFull
	}) {
		t.Errorf("gaps = %+v, want an aged_out gap naming run %d as still running", gaps, id)
	}
	if !slices.Contains(a.tc.log.AttrValuesExact(logMsgCatchUpGap, "run_id"), strconv.FormatInt(id, 10)) {
		t.Errorf("no WARN %q names run %d", logMsgCatchUpGap, id)
	}
	if !slices.Contains(a.tc.log.AttrValuesExact(logMsgCatchUpGap, "cause"), launchrule.GapAgedOut) {
		t.Errorf("no WARN %q with cause aged_out", logMsgCatchUpGap)
	}
}

func TestAC1_5(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		a.eng.handRun(weeklyRecipe, "wf_hand")
		r, created := watching(a, 7001)
		a.setRun(weekly(7001, forgeapi.CheckPassing, created))
		advance(pollTick + time.Second)
		if got := a.eng.launches(); len(got) != 0 {
			t.Fatalf("launches while the hand run is live = %+v, want none", got)
		}
		want := "waiting for the running " + weeklyRecipe + " run to finish"
		if st := a.view(r.ID).Status; st.RowText != want {
			t.Errorf("row text = %q, want %q", st.RowText, want)
		}
		if held := len(a.view(r.ID).Queue); held != 1 {
			t.Errorf("%d events held, want 1", held)
		}
		advance(time.Hour)
		a.eng.finish("wf_hand")
		advance(pollTick + time.Second)
		advance(time.Hour)
		if got := a.eng.launches(); !eq(runIDs(got), []string{"7001"}) {
			t.Errorf("launched run ids after the hand run ended = %v, want 7001 once", runIDs(got))
		}
	})
}

func TestAC1_6(t *testing.T) {
	t.Run("two successes behind a busy recipe launch the newer", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.eng.handRun(weeklyRecipe, "wf_hand")
			r, created := watching(a, 8001)
			dispatch := weekly(8001, forgeapi.CheckPassing, created)
			dispatch.Trigger = forgeapi.TriggerManual
			a.setRun(dispatch)
			advance(pollTick)
			a.setRun(weekly(8002, forgeapi.CheckPending, time.Now()))
			advance(pollTick)
			a.setRun(weekly(8002, forgeapi.CheckPassing, time.Now().Add(-pollTick)))
			advance(pollTick)
			a.eng.finish("wf_hand")
			advance(pollTick + time.Second)
			advance(time.Hour)
			got := a.eng.launches()
			if !eq(runIDs(got), []string{"8002"}) {
				t.Fatalf("launched run ids = %v, want only the newer, 8002", runIDs(got))
			}
			wantSuperseded(t, a, r.ID, 8001, 8002, got[0].wf)
		})
	})
	t.Run("a success matching while the older attempt is in flight", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			spec := weeklySpec()
			spec.IntervalMin = 2
			r := a.rule(weeklyRecipe, nil, spec)
			a.boot()
			advance(time.Minute)
			a.setRun(weekly(8001, forgeapi.CheckPassing, time.Now()))
			a.eng.plan(launchPlan{dropStart: true})
			advance(2 * time.Minute)
			if got := a.eng.launches(); !eq(runIDs(got), []string{"8001"}) {
				t.Fatalf("launched run ids = %v, want 8001 in flight", runIDs(got))
			}
			a.setRun(weekly(8002, forgeapi.CheckPassing, time.Now()))
			advance(2 * time.Minute)
			if held := len(a.view(r.ID).Queue); held != 2 {
				t.Errorf("%d events held while 8001's attempt is in flight, want both", held)
			}
			advance(launchConfirmBound + launchBackoff[0])
			advance(time.Hour)
			got := a.eng.launches()
			if !eq(runIDs(got), []string{"8001", "8002"}) {
				t.Fatalf("launched run ids = %v, want 8001's lost attempt, then only 8002", runIDs(got))
			}
			wantSuperseded(t, a, r.ID, 8001, 8002, got[1].wf)
		})
	})
}

// wantSuperseded checks the older run's key is superseded by the newer's, which launched as wf.
func wantSuperseded(t *testing.T, a *acceptance, ruleID string, older, newer int64, wf string) {
	t.Helper()
	repo := foldRepo(weeklyRepo)
	es, _ := a.tc.memory(ruleID)
	want := supersededEntry{Key: runKey(ruleID, repo, older), By: runKey(ruleID, repo, newer), WorkflowID: wf}
	if !slices.Contains(es.superseded, want) {
		t.Errorf("superseded = %+v, want %+v", es.superseded, want)
	}
}

func TestAC1_7(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		a.weeklyRule()
		a.boot()
		advance(time.Minute)
		ci := weekly(7001, forgeapi.CheckPassing, time.Now())
		ci.WorkflowPath, ci.Trigger = ciWorkflow, forgeapi.TriggerPush
		a.setRun(ci)
		advance(24 * time.Hour)
		if got := a.eng.launches(); len(got) != 0 {
			t.Errorf("launches = %+v, want none for another workflow", got)
		}
	})
}

// launchedWith has Weekly run 7001 succeed with its launch going as plan, and answers the rule and the launch, the
// clock within a second of it.
func launchedWith(t *testing.T, a *acceptance, plan launchPlan) (launchrule.Rule, launch) {
	t.Helper()
	r, created := watching(a, 7001)
	a.eng.plan(plan)
	a.setRun(weekly(7001, forgeapi.CheckPassing, created))
	return r, a.awaitLaunches(1)[0]
}

func sameEvent(t *testing.T, got []launch) {
	t.Helper()
	if len(got) != 2 || got[1].inputs["event_id"] != got[0].inputs["event_id"] {
		t.Errorf("launches = %+v, want the event launched a second time with the same event_id", got)
	}
}

func TestAC1_8(t *testing.T) {
	t.Run("killed after the run_start, before its write, and not resumed", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, first := launchedWith(t, a, launchPlan{dropStart: true})
			a.restart()
			a.start()
			advance(10 * time.Minute)
			if !a.tc.BootHeld(first.wf) {
				t.Errorf("%s is not boot-held, want the orphan sweep to leave it alone", first.wf)
			}
			a.engineReady()
			advanceTo(time.Now().Add(launchConfirmBound - time.Second))
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels inside the bound from engine ready = %v, want none", got)
			}
			advance(time.Hour)
			if got := a.eng.cancelled(); !eq(got, []string{first.wf}) {
				t.Errorf("cancels = %v, want %s's at its bound", got, first.wf)
			}
			sameEvent(t, a.eng.launches())
		})
	})
	resumed := func(t *testing.T, a *acceptance, beforeRunner bool) {
		t.Helper()
		r, first := launchedWith(t, a, launchPlan{dropStart: true})
		a.restart()
		if beforeRunner {
			a.eng.start(first.wf)
			a.boot()
		} else {
			a.boot()
			advance(time.Minute)
			a.eng.start(first.wf)
		}
		advance(time.Hour)
		if got := a.eng.cancelled(); len(got) != 0 {
			t.Errorf("cancels = %v, want none", got)
		}
		if got := a.eng.launches(); len(got) != 1 {
			t.Errorf("launches = %+v, want no second", got)
		}
		if st := a.view(r.ID).Status; st.LastRunID != first.wf {
			t.Errorf("row's last run = %q, want %s confirmed", st.LastRunID, first.wf)
		}
	}
	t.Run("resumed, its run_start before the runner is constructed", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) { resumed(t, a, true) })
	})
	t.Run("resumed, its run_start after the runner is constructed", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) { resumed(t, a, false) })
	})
	t.Run("killed between the attempt's write and the lease's", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, first := launchedWith(t, a, launchPlan{dies: diesAfterName})
			a.restart()
			a.boot()
			advance(launchBackoff[0] + time.Second)
			if got := a.eng.cancelled(); !eq(got, []string{first.wf}) {
				t.Errorf("cancels = %v, want %s's once the engine is ready", got, first.wf)
			}
			if !slices.Contains(a.tc.log.AttrValuesExact(logMsgLaunchAbandoned, "cause"), causeBootUnleased) {
				t.Errorf("no INFO %q with cause boot_unleased", logMsgLaunchAbandoned)
			}
			got := a.eng.launches()
			sameEvent(t, got)
			if calls := a.eng.taken(); len(calls) != len(got) {
				t.Errorf("calls = %+v, want none refused as busy", calls)
			}
		})
	})
	t.Run("killed between the lease's write and the invoke's reply", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, first := launchedWith(t, a, launchPlan{dies: diesAfterLease})
			a.restart()
			a.boot()
			if !a.tc.BootHeld(first.wf) {
				t.Errorf("%s is not boot-held, want its fresh bound", first.wf)
			}
			advance(launchConfirmBound - time.Second)
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels inside the bound = %v, want none", got)
			}
			advance(time.Hour)
			if got := a.eng.cancelled(); !eq(got, []string{first.wf}) {
				t.Errorf("cancels = %v, want %s's at its bound", got, first.wf)
			}
			sameEvent(t, a.eng.launches())
		})
	})
	t.Run("killed between workflow/new and the attempt's write", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{dies: diesAfterNew})
			a.restart()
			a.boot()
			advance(pollTick + time.Second)
			want := "run " + first.wf + " is blocking " + weeklyRecipe + " and this rule has no record of launching it"
			if st := a.view(r.ID).Status; st.RowText != want {
				t.Errorf("row text = %q, want %q", st.RowText, want)
			}
			wantBlocked(t, r.ID, "unnamed_run")
			if got, _ := a.tc.log.AttrValueExact(logMsgRunNotStarted, "class"); got != notStartedBlocked {
				t.Errorf("WARN %q class = %q, want blocked", logMsgRunNotStarted, got)
			}
			advance(time.Hour)
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels = %v, want the unnamed run left alone", got)
			}
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches = %+v, want the event held behind the unnamed run", got)
			}
		})
	})
}

func TestAC1_9(t *testing.T) {
	t.Run("an invoke refused in band", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{dropStart: true, reply: refusal})
			if got, _ := a.tc.log.AttrValueExact(logMsgLaunchAbandoned, "cause"); got != causeInvokeError {
				t.Errorf("INFO %q cause = %q, want invoke_error", logMsgLaunchAbandoned, got)
			}
			if got := a.eng.cancelled(); !eq(got, []string{first.wf}) {
				t.Errorf("cancels = %v, want %s's at once", got, first.wf)
			}
			if _, ok := a.tc.leases.Get(first.wf); ok {
				t.Errorf("%s still has its lease, want its cancel's terminal frame to release it", first.wf)
			}
			if st := a.view(r.ID).Status; st.LastRunID == first.wf {
				t.Errorf("row's last run = %s, want no outcome of the refused run", st.LastRunID)
			}
			advanceTo(first.at.Add(launchBackoff[0] - time.Second))
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches inside the backoff's first step = %+v, want none again", got)
			}
			advance(2 * time.Second)
			sameEvent(t, a.eng.launches())
		})
	})
	t.Run("an invoke reply lost, its run_start 2 s later", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{dropStart: true, reply: context.DeadlineExceeded})
			advanceTo(first.at.Add(2 * time.Second))
			a.eng.start(first.wf)
			advance(time.Hour)
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels = %v, want none", got)
			}
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches = %+v, want no second", got)
			}
			if st := a.view(r.ID).Status; st.LastRunID != first.wf || !st.FailingSince.IsZero() {
				t.Errorf("status %+v, want %s confirmed and nothing counted", st, first.wf)
			}
		})
	})
	t.Run("an invoke reply lost and no run_start", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			lost := launchPlan{dropStart: true, reply: context.DeadlineExceeded}
			a.eng.planRest(lost)
			r, first := launchedWith(t, a, lost)
			advanceTo(first.at.Add(launchConfirmBound - time.Second))
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels inside the bound = %v, want none", got)
			}
			advance(2 * time.Second)
			if got := a.eng.cancelled(); !eq(got, []string{first.wf}) {
				t.Errorf("cancels at the bound = %v, want %s's", got, first.wf)
			}
			if got, _ := a.tc.log.AttrValueExact(logMsgLaunchAbandoned, "cause"); got != causeBound {
				t.Errorf("INFO %q cause = %q, want bound", logMsgLaunchAbandoned, got)
			}
			advance(24 * time.Hour)
			if got := a.eng.launches(); len(got) != 2 {
				t.Errorf("launches = %+v, want two, the second expiry blocking the rule", got)
			}
			if st := a.view(r.ID).Status; st.Blocked == "" {
				t.Errorf("status %+v, want the rule blocked", st)
			}
			wantBlocked(t, r.ID, "launch_failed")
		})
	})
	t.Run("an invoke reply lost after its run_start", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{reply: context.DeadlineExceeded})
			advance(time.Hour)
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels = %v, want none", got)
			}
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches = %+v, want no second", got)
			}
			if _, ok := a.tc.leases.Get(first.wf); !ok {
				t.Errorf("%s has no lease, want it kept", first.wf)
			}
			if st := a.view(r.ID).Status; !st.FailingSince.IsZero() {
				t.Errorf("failing since %v, want no 24-hour clock", st.FailingSince)
			}
		})
	})
	t.Run("an invoke answered and then rejected", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.eng.planRest(launchPlan{dropStart: true})
			r, first := launchedWith(t, a, launchPlan{dropStart: true})
			advanceTo(first.at.Add(launchConfirmBound + time.Second))
			want := "run " + first.wf + " was not confirmed within 5 minutes; cancelled it and launching the event again"
			if st := a.view(r.ID).Status; st.RowText != want {
				t.Errorf("row text = %q, want %q", st.RowText, want)
			}
			a.eng.start(first.wf)
			if st := a.view(r.ID).Status; st.LastRunID == first.wf {
				t.Errorf("a late run_start confirmed the abandoned %s", first.wf)
			}
			advance(time.Hour)
			got := a.eng.launches()
			if len(got) != 2 {
				t.Fatalf("launches = %+v, want two", got)
			}
			want = "run " + got[1].wf + " did not report its start twice; marotte stopped retrying this event. " +
				"Check the engine, then resume the rule"
			if st := a.view(r.ID).Status; st.Blocked != want {
				t.Errorf("blocked = %q, want %q", st.Blocked, want)
			}
			wantBlocked(t, r.ID, "launch_failed")
		})
	})
}

func TestAC1_10(t *testing.T) {
	outage := func(t *testing.T, a *acceptance, pending time.Duration) launchrule.Rule {
		t.Helper()
		r, created := watching(a, 7001)
		advance(pending)
		a.restart()
		a.setRun(weekly(7001, forgeapi.CheckPassing, created))
		advance(4 * time.Hour)
		a.boot()
		advance(time.Hour)
		return r
	}
	t.Run("pending 40 hours, then a 4-hour outage", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			outage(t, a, 40*time.Hour)
			if got := a.eng.launches(); !eq(runIDs(got), []string{"7001"}) {
				t.Errorf("launched run ids = %v, want 7001 once", runIDs(got))
			}
		})
	})
	t.Run("pending 70 hours, then a 4-hour outage", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r := outage(t, a, 70*time.Hour)
			if got := a.eng.launches(); len(got) != 0 {
				t.Errorf("launches = %+v, want none past the horizon", got)
			}
			wantAgedOut(t, a, r.ID, 7001)
		})
	})
}

func TestAC1_11(t *testing.T) {
	t.Run("restart-paused and not resumed", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{})
			a.restart()
			a.start()
			if a.tc.BootHeld(first.wf) {
				t.Fatalf("%s is boot-held, want the orphan sweep to take its confirmed run", first.wf)
			}
			const reason = "stopped because the server restarted while it was running"
			a.eng.vanish(first.wf)
			_ = a.tc.leases.Release(t.Context(), first.wf)
			a.tc.RecordOutcome(r.ID, first.wf, launchrule.Outcome{Status: launchrule.StatusFailed, Reason: reason})
			a.engineReady()
			advance(24 * time.Hour)
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches = %+v, want the event not launched again", got)
			}
			if st := a.view(r.ID).Status; st.LastRunID != first.wf || st.LastStatus != launchrule.StatusFailed ||
				st.LastReason != reason {
				t.Errorf("status %+v, want the sweep's outcome on %s", st, first.wf)
			}
		})
	})
	t.Run("resumed by KAS, its later run_start changes nothing", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{})
			a.restart()
			a.boot()
			a.eng.start(first.wf)
			advance(24 * time.Hour)
			if got := a.eng.cancelled(); len(got) != 0 {
				t.Errorf("cancels = %v, want none", got)
			}
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches = %+v, want no second", got)
			}
			if st := a.view(r.ID).Status; st.LastRunID != first.wf || st.LastStatus != launchrule.StatusStarted {
				t.Errorf("status %+v, want %s still started", st, first.wf)
			}
		})
	})
}

func TestAC1_12(t *testing.T) {
	t.Run("a lost run_start launches the event again", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := launchedWith(t, a, launchPlan{dropStart: true})
			got := a.awaitLaunches(2)
			sameEvent(t, got)
			a.tc.RecordEnding(r.ID, first.wf, launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "cancelled"})
			if st := a.view(r.ID).Status; st.LastRunID != got[1].wf || st.LastStatus != launchrule.StatusStarted {
				t.Errorf("status %+v, want the cancelled run's ending on no row", st)
			}
			a.eng.finish(got[1].wf)
			if st := a.view(r.ID).Status; st.LastRunID != got[1].wf || st.LastStatus != launchrule.StatusCompleted {
				t.Errorf("status %+v, want the second run's outcome", st)
			}
		})
	})
	t.Run("every run_start lost: two runs, never a third", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.eng.planRest(launchPlan{dropStart: true})
			r, _ := launchedWith(t, a, launchPlan{dropStart: true})
			advance(48 * time.Hour)
			if got := a.eng.launches(); len(got) != 2 {
				t.Errorf("launches = %+v, want two", got)
			}
			if st := a.view(r.ID).Status; st.Blocked == "" {
				t.Errorf("status %+v, want the rule blocked", st)
			}
		})
	})
	t.Run("a lease that cannot be persisted", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, first := launchedWith(t, a, launchPlan{leaseFails: true})
			if got := a.eng.cancelled(); !eq(got, []string{first.wf}) {
				t.Errorf("cancels = %v, want %s's", got, first.wf)
			}
			advanceTo(first.at.Add(launchBackoff[0] - time.Second))
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches inside the backoff = %+v, want none again", got)
			}
			advance(2 * time.Second)
			sameEvent(t, a.eng.launches())
		})
	})
}

// wantCancelRetried checks the rule counts the busys of its abandoned run wf, whose cancel has not landed.
func wantCancelRetried(t *testing.T, a *acceptance, ruleID, wf string) {
	t.Helper()
	before := len(a.eng.cancelled())
	advance(2 * pollTick)
	if n := len(a.eng.cancelled()); n < before+2 {
		t.Errorf("%d cancels after two poll intervals, want one resend at each over %d", n, before)
	}
	st := a.view(ruleID).Status
	want := "run " + wf + " was abandoned by this rule and its cancel has not landed; retrying"
	if st.RowText != want || st.FailingSince.IsZero() || st.Blocked != "" {
		t.Errorf("status %+v, want the busys counted, row text %q and no block yet", st, want)
	}
}

func TestAC1_13(t *testing.T) {
	t.Run("a leased run whose cancel KAS refuses", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.eng.refuseCancels(errCancelRefused)
			r, first := launchedWith(t, a, launchPlan{dropStart: true})
			advanceTo(first.at.Add(launchConfirmBound + launchBackoff[0] + time.Second))
			wantCancelRetried(t, a, r.ID, first.wf)
			if _, ok := a.tc.leases.Get(first.wf); !ok {
				t.Errorf("%s has no lease, want it kept while its cancel has not landed", first.wf)
			}
			advance(24 * time.Hour)
			if st := a.view(r.ID).Status; st.Blocked == "" {
				t.Errorf("status %+v, want the rule blocked after 24 hours", st)
			}
			wantBlocked(t, r.ID, "launch_failed")
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches = %+v, want none past the abandoned run", got)
			}
		})
	})
	t.Run("an unleased run whose cancel fails", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.eng.refuseCancels(errCancelRefused)
			r, first := launchedWith(t, a, launchPlan{dies: diesAfterName})
			a.restart()
			a.boot()
			advance(launchBackoff[0] + time.Second)
			if rec := a.tc.recordOf(r.ID, first.wf); rec == nil || rec.cause != causeBootUnleased {
				t.Fatalf("record of %s = %+v, want abandoned with cause boot_unleased", first.wf, rec)
			}
			wantCancelRetried(t, a, r.ID, first.wf)
			a.eng.vanish(first.wf)
			advance(pollTick + time.Second)
			if rec := a.tc.recordOf(r.ID, first.wf); rec != nil {
				t.Errorf("record of %s = %+v, want it dropped once the run list no longer shows it", first.wf, rec)
			}
			sameEvent(t, a.eng.launches())
		})
	})
}

func TestAC2_1(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		r := a.reportRule()
		other := a.rule("animap-report-copy", nil, reportSpec())
		a.boot()
		a.setIssues(report(12, validLabel, testLabel))
		advance(pollTick + time.Second)
		advance(24 * time.Hour)
		got := a.eng.launches()
		if len(got) != 2 {
			t.Fatalf("launches = %+v, want one per rule", got)
		}
		ids := map[string]string{}
		for i := range got {
			in := got[i].inputs
			ids[got[i].ruleID] = in["event_id"]
			if in[inputIssueNumber] != "12" {
				t.Errorf("issue_number = %q, want 12", in[inputIssueNumber])
			}
			for name, v := range in {
				if strings.Contains(v, "canary") {
					t.Errorf("input %s = %q carries the issue's text", name, v)
				}
			}
		}
		for _, id := range []string{r.ID, other.ID} {
			prefix := "github.com/" + testRepo + "/issue/12/labeled/" + id + "/"
			if !strings.HasPrefix(ids[id], prefix) || len(ids[id]) == len(prefix) {
				t.Errorf("rule %s event_id = %q, want %s<minted gain>", id, ids[id], prefix)
			}
		}
	})
}

func TestAC2_2(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		a.reportRule()
		a.boot()
		a.setIssues(report(12, validLabel))
		advance(24 * time.Hour)
		if got := a.eng.launches(); len(got) != 0 {
			t.Errorf("launches = %+v, want none without report:fast", got)
		}
	})
}

// threeGained boots a process over the report rule with the next launch going as plan, has issues 3, 1 and 2 gain
// the label in one poll, and answers the head's launch, the clock within a second of it.
func threeGained(t *testing.T, a *acceptance, plan launchPlan) launch {
	t.Helper()
	a.reportRule()
	a.boot()
	a.eng.plan(plan)
	a.setIssues(report(3, testLabel), report(1, testLabel), report(2, testLabel))
	return a.awaitLaunches(1)[0]
}

// finishEach ends each started run of the report recipe at the next poll interval, until n launches created a run.
func finishEach(t *testing.T, a *acceptance, n int) []launch {
	t.Helper()
	for range 4 * n {
		if len(a.eng.launches()) >= n {
			break
		}
		if wf := a.eng.liveRun(reportRecipe); wf != "" && a.eng.started(wf) {
			a.eng.finish(wf)
		}
		advance(pollTick + time.Second)
	}
	advance(time.Hour)
	return a.eng.launches()
}

func TestAC2_3(t *testing.T) {
	t.Run("three gains launch in issue order, one live run at a time", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			threeGained(t, a, launchPlan{})
			advance(time.Hour)
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches while the head's run is live = %+v, want the head only", got)
			}
			if got := finishEach(t, a, 3); !eq(launchedIssues(got), []string{"1", "2", "3"}) {
				t.Errorf("launched issues = %v, want 1, 2, 3", launchedIssues(got))
			}
		})
	})
	t.Run("a restart with the head's attempt unconfirmed", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			head := threeGained(t, a, launchPlan{dropStart: true})
			a.restart()
			a.boot()
			if !a.tc.BootHeld(head.wf) {
				t.Errorf("%s is not boot-held, want its fresh bound", head.wf)
			}
			advance(launchConfirmBound - time.Second)
			if got := a.eng.launches(); len(got) != 1 {
				t.Errorf("launches while the head is held = %+v, want nothing behind it", got)
			}
			got := finishEach(t, a, 4)
			if !eq(launchedIssues(got), []string{"1", "1", "2", "3"}) {
				t.Fatalf("launched issues = %v, want the head twice, then 2 and 3", launchedIssues(got))
			}
			sameEvent(t, got[:2])
		})
	})
	t.Run("the head abandoned at its bound, with no restart", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			head := threeGained(t, a, launchPlan{dropStart: true})
			advanceTo(head.at.Add(launchConfirmBound + time.Second))
			calls := len(a.eng.taken())
			advanceTo(head.at.Add(launchConfirmBound + launchBackoff[0] - time.Second))
			if n := len(a.eng.taken()); n != calls {
				t.Errorf("%d calls during the head's backoff, want none past the %d before it", n, calls)
			}
			got := finishEach(t, a, 4)
			if !eq(launchedIssues(got), []string{"1", "1", "2", "3"}) {
				t.Fatalf("launched issues = %v, want the head twice, then 2 and 3", launchedIssues(got))
			}
			sameEvent(t, got[:2])
		})
	})
}

// gained boots a process over the report rule, has issue 5 gain the label and its run finish, and answers the
// launch.
func gained(t *testing.T, a *acceptance) (launchrule.Rule, launch) {
	t.Helper()
	r := a.reportRule()
	a.boot()
	a.setIssues(report(5, testLabel))
	advance(pollTick + time.Second)
	got := a.eng.launches()
	if len(got) != 1 {
		t.Fatalf("launches after the gain = %+v, want one", got)
	}
	a.eng.finish(got[0].wf)
	return r, got[0]
}

// regained has issue 5 gain the label again and answers the launches since first.
func regained(t *testing.T, a *acceptance, first launch) []launch {
	t.Helper()
	a.setIssues(report(5, testLabel))
	advance(pollTick + time.Second)
	advance(time.Hour)
	got := a.eng.launches()
	if len(got) != 2 {
		t.Fatalf("launches = %+v, want the first and one for the new gain", got)
	}
	if got[1].inputs["event_id"] == first.inputs["event_id"] {
		t.Errorf("the new gain's event_id %q is the first launch's", got[1].inputs["event_id"])
	}
	return got
}

func TestAC2_4(t *testing.T) {
	t.Run("removed and added again", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, first := gained(t, a)
			a.setIssues(report(5))
			advance(time.Hour)
			if n := len(a.eng.launches()); n != 1 {
				t.Errorf("%d launches while the label is off, want the first only", n)
			}
			regained(t, a, first)
		})
	})
	t.Run("the rule deleted and re-created", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := gained(t, a)
			deleteRule(t, a.tc.Confirmer, r.ID)
			a.reportRule()
			advance(time.Hour)
			a.setIssues(report(5))
			advance(pollTick + time.Second)
			regained(t, a, first)
		})
	})
	t.Run("a rebaseline edit", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			r, first := gained(t, a)
			moved := func(repo string) func(*launchrule.Rule) {
				return func(r *launchrule.Rule) { r.When.Event.Repo = repo }
			}
			if err := changeRule(t, a.tc.Confirmer, r.ID, 0, moved("cplieger/animap-copy")); err != nil {
				t.Fatalf("Setup: edit: %v", err)
			}
			advance(time.Hour)
			if err := changeRule(t, a.tc.Confirmer, r.ID, 0, moved(testRepo)); err != nil {
				t.Fatalf("Setup: edit back: %v", err)
			}
			a.setIssues(report(5))
			advance(time.Hour)
			regained(t, a, first)
		})
	})
	t.Run("a closed item's entry dropped after 30 days", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			_, first := gained(t, a)
			a.setIssues()
			advance(31 * 24 * time.Hour)
			regained(t, a, first)
		})
	})
}

func TestAC2_5(t *testing.T) {
	t.Run("labelled while down", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.reportRule()
			a.boot()
			a.restart()
			a.setIssues(report(7, testLabel))
			advance(2 * time.Hour)
			a.boot()
			advance(time.Hour)
			if got := a.eng.launches(); !eq(launchedIssues(got), []string{"7"}) {
				t.Errorf("launched issues = %v, want 7 once", launchedIssues(got))
			}
		})
	})
	t.Run("labelled and closed while down", func(t *testing.T) {
		inAcceptance(t, func(t *testing.T, a *acceptance) {
			a.reportRule()
			a.boot()
			a.restart()
			a.setIssues(report(7, testLabel))
			advance(time.Hour)
			a.setIssues()
			advance(time.Hour)
			a.boot()
			advance(time.Hour)
			if got := a.eng.launches(); len(got) != 0 {
				t.Errorf("launches = %+v, want none", got)
			}
		})
	})
}

func TestAC2_6(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		a.eng.handRun(reportRecipe, "wf_hand")
		r := a.reportRule()
		a.boot()
		items := make([]forgeapi.Issue, 0, 21)
		var want []string
		for n := 1; n <= 21; n++ {
			items = append(items, report(n, testLabel))
			want = append(want, strconv.Itoa(n))
		}
		a.setIssues(items...)
		advance(pollTick + time.Second)
		if held := len(a.view(r.ID).Queue); held != 20 {
			t.Errorf("%d events held, want the cap of 20", held)
		}
		advance(time.Hour)
		a.eng.finish("wf_hand")
		for range 12 * 60 {
			if wf := a.eng.liveRun(reportRecipe); wf != "" {
				a.eng.finish(wf)
			}
			advance(time.Minute)
		}
		if got := a.eng.launches(); !eq(launchedIssues(got), want) {
			t.Errorf("launched issues = %v, want all 21 in order", launchedIssues(got))
		}
		if n := a.tc.log.CountExact(logMsgQueueFull); n != 1 {
			t.Errorf("%d WARN %q lines, want 1", n, logMsgQueueFull)
		}
	})
}

func TestAC2_7(t *testing.T) {
	inAcceptance(t, func(t *testing.T, a *acceptance) {
		a.setIssues(report(3, testLabel), report(4, validLabel, testLabel))
		a.reportRule()
		a.boot()
		advance(24 * time.Hour)
		if got := a.eng.launches(); len(got) != 0 {
			t.Errorf("launches = %+v, want none for issues labelled before the save", got)
		}
	})
}
