package trigger

import (
	"slices"
	"strings"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// runReach is how far before the last complete run walk a walk reads, which is what catches a re-run: GitHub
// keeps a run's created_at across re-runs.
const runReach = 24 * time.Hour

// runBound is the since bound of a feed's run walk, the earliest any of its run rules needs, hour-floored so
// the walk's URLs repeat for up to an hour and an unchanged page answers 304.
func runBound(rules []*pollRule, start time.Time) time.Time {
	bound := start
	for _, pr := range rules {
		rc := pr.st.cursor[pr.feed.RepoID()]
		if pr.ev.Kind != launchrule.KindRunConcluded || rc == nil || rc.Runs == nil {
			continue
		}
		bound = minTime(bound, rc.Runs.LastComplete.Add(-runReach))
	}
	return bound.Truncate(time.Hour)
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// applyRuns matches one run walk. A run enters the rule's map only when it passes the static filters, so only
// its result is left to decide; a run is keyed on its id, never its attempt, so it fires once, when one of its
// attempts first concludes with a result in the filter.
func (pr *pollRule) applyRuns(w *Walk[forgeapi.Run]) {
	rc := pr.repoCursor()
	complete := w.Complete()
	if rc.Runs == nil {
		if complete {
			pr.baselineRuns(rc, len(w.Items))
		}
		return
	}
	c := rc.Runs
	for i := range w.Items {
		r := &w.Items[i]
		if r.CreatedAt.Before(c.BaselineAt) || !pr.admitsRun(r) {
			continue
		}
		if _, fired := c.Fired[r.Ref.ID]; fired {
			continue
		}
		result := resultOf(r.State)
		if result != "" && slices.Contains(pr.ev.Filter.Result, result) {
			pr.enqueueRun(r, result)
			delete(c.Runs, r.Ref.ID)
			if c.Fired == nil {
				c.Fired = map[int64]time.Time{}
			}
			c.Fired[r.Ref.ID] = r.CreatedAt
			continue
		}
		hold := holdOutside
		if result == "" {
			hold = holdPending
		}
		if c.Runs == nil {
			c.Runs = map[int64]*runEntry{}
		}
		c.Runs[r.Ref.ID] = &runEntry{CreatedAt: r.CreatedAt, State: r.State.String(), Hold: hold}
	}
	if complete {
		c.LastComplete = pr.start
	}
}

// resultOf maps a run's state onto a filter.result word; a pending or unknown state has none and never matches.
func resultOf(s forgeapi.CheckState) string {
	switch s {
	case forgeapi.CheckPassing:
		return "success"
	case forgeapi.CheckFailing:
		return "failure"
	case forgeapi.CheckNeutral:
		return "neutral"
	default:
		return ""
	}
}

// admitsRun applies the static filters: workflow, branch and run_triggers. A trigger forgeapi could not map
// reads unknown, which no filter names, so it fails closed.
func (pr *pollRule) admitsRun(r *forgeapi.Run) bool {
	f := &pr.ev.Filter
	if f.Workflow != "" && r.WorkflowPath != f.Workflow {
		return false
	}
	if f.Branch != "" && pr.runBranch(r.Branch) != f.Branch {
		return false
	}
	return r.Trigger != forgeapi.TriggerUnknown && slices.Contains(f.RunTriggers, r.Trigger.String())
}

// runBranch is a run's branch as the filter compares it. GitLab spells a pipeline's ref as a branch on some rows
// and a refs/ path on others (forgeapi's ListRuns contract): refs/heads/ is stripped, and any other refs/ path is
// no branch.
func (pr *pollRule) runBranch(branch string) string {
	if kind, _ := connectionParts(pr.feed.Connection); kind != "gitlab" {
		return branch
	}
	if b, ok := strings.CutPrefix(branch, "refs/heads/"); ok {
		return b
	}
	if strings.HasPrefix(branch, "refs/") {
		return ""
	}
	return branch
}

func (pr *pollRule) enqueueRun(r *forgeapi.Run, result string) {
	repo := foldRepo(pr.feed.Selector)
	_, host := connectionParts(pr.feed.Connection)
	key := runKey(pr.rule.ID, repo, r.Ref.ID)
	in := pr.eventInputs(key, runEventID(host, repo, r.Ref.ID, result))
	pr.enqueue(&queueEntry{
		Key: key, CoalesceKey: runCoalesceKey(pr.feed.RepoID(), r.WorkflowPath, r.Branch), ObservedAt: pr.start,
		Inputs: pr.runInputs(in, r, result),
	})
}
