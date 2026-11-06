package trigger

import (
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

// baselined is a harness whose run rule took its baseline at pollStart.
func baselined(t *testing.T, results ...string) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	r := h.create(runSpec(results...))
	h.runPage()
	h.poll()
	return h, r.ID
}

func TestRunKeyedOnIDNotAttempt(t *testing.T) {
	h, id := baselined(t, "success")
	created := pollStart.Add(time.Minute)
	a, b := run(41, forgeapi.CheckPassing, created), run(42, forgeapi.CheckPassing, created)
	a.Ref.Number, b.Ref.Number = 7, 7
	h.runPage(a, b)
	h.runPage(a, b)
	h.poll()
	h.poll()
	q := h.queued(id)
	if got := inputOf(q, "run_id"); !eq(got, []string{"41", "42"}) {
		t.Fatalf("queued runs %v, want 41 and 42 once each though both are run number 7", got)
	}
	if got, want := q[0].Inputs["event_id"], "github.com/cplieger/animap/run/41/success"; got != want {
		t.Errorf("event_id = %q, want %q", got, want)
	}
	in := q[0].Inputs
	if in["result"] != "success" || in["head_sha"] != testSHA || in["workflow"] != testWorkflow {
		t.Errorf("inputs = %v, want result success, the head sha and the rule's workflow", in)
	}
}

func TestRunFailedThenRerunSucceeds(t *testing.T) {
	h, id := baselined(t, "success")
	created := pollStart.Add(time.Minute)
	h.runPage(run(41, forgeapi.CheckPending, created))
	h.runPage(run(41, forgeapi.CheckFailing, created))
	h.runPage(run(41, forgeapi.CheckPending, created))
	h.runPage(run(41, forgeapi.CheckPassing, created))
	h.poll()
	h.poll()
	if q := h.queued(id); len(q) != 0 {
		t.Fatalf("the failed run queued %v, want nothing on a success rule", keysOf(q))
	}
	if e := h.cursorOf(id).Runs.Runs[41]; e == nil || e.Hold != holdOutside {
		t.Errorf("run 41 after failing = %+v, want kept as outside for a re-run", e)
	}
	h.poll()
	h.poll()
	if got := inputOf(h.queued(id), "run_id"); !eq(got, []string{"41"}) {
		t.Errorf("after the re-run succeeded queued %v, want run 41 once", got)
	}
}

func TestRunFailureRuleIgnoresRerunFailure(t *testing.T) {
	h, id := baselined(t, "failure")
	created := pollStart.Add(time.Minute)
	h.runPage(run(41, forgeapi.CheckFailing, created))
	h.runPage(run(41, forgeapi.CheckPending, created))
	h.runPage(run(41, forgeapi.CheckFailing, created))
	for range 3 {
		h.poll()
	}
	if got := inputOf(h.queued(id), "result"); !eq(got, []string{"failure"}) {
		t.Errorf("queued results %v, want the first failure only", got)
	}
	if _, fired := h.cursorOf(id).Runs.Fired[41]; !fired {
		t.Error("run 41 is not in fired")
	}
}

func TestRunCreatedBeforeBaselineIgnored(t *testing.T) {
	h, id := baselined(t, "success")
	h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(-time.Minute)))
	h.poll()
	if q := h.queued(id); len(q) != 0 {
		t.Errorf("a run created before the baseline queued %v, want nothing", keysOf(q))
	}
	if e := h.cursorOf(id).Runs.Runs[41]; e != nil {
		t.Errorf("a run created before the baseline entered the map: %+v", e)
	}
}

func TestRunTriggersDefaultExcludesPullRequest(t *testing.T) {
	h, id := baselined(t, "success")
	created := pollStart.Add(time.Minute)
	pr := run(41, forgeapi.CheckPending, created)
	pr.Trigger = forgeapi.TriggerPullRequest
	unknown := run(43, forgeapi.CheckPassing, created)
	unknown.Trigger = forgeapi.TriggerUnknown
	push := run(42, forgeapi.CheckPassing, created)
	h.runPage(pr, push, unknown)
	pr.State = forgeapi.CheckPassing
	h.runPage(pr, push, unknown)
	h.poll()
	h.poll()
	if got := inputOf(h.queued(id), "run_id"); !eq(got, []string{"42"}) {
		t.Errorf("queued runs %v, want the push run only", got)
	}
	runs := h.cursorOf(id).Runs.Runs
	if runs[41] != nil || runs[43] != nil {
		t.Errorf("run map = %v, want neither the pull-request run nor the unknown-trigger run in it", runs)
	}
}

func TestRunBranchFilterOnGitLabRefs(t *testing.T) {
	h := newHarness(t)
	ev := runSpec("success")
	ev.Connection = "gitlab:gitlab.com"
	ev.Filter.Workflow = ""
	r := h.create(ev)
	h.runPage()
	h.poll()
	created := pollStart.Add(time.Minute)
	heads, mr, plain := run(41, forgeapi.CheckPassing, created), run(42, forgeapi.CheckPassing, created),
		run(43, forgeapi.CheckPassing, created)
	heads.Branch, mr.Branch = "refs/heads/main", "refs/merge-requests/3/head"
	heads.WorkflowPath, mr.WorkflowPath, plain.WorkflowPath = "", "", ""
	h.runPage(heads, mr, plain)
	h.poll()
	if got := inputOf(h.queued(r.ID), "run_id"); !eq(got, []string{"41", "43"}) {
		t.Errorf("queued runs %v, want refs/heads/main and main, not the merge-request ref", got)
	}
}
