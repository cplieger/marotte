package trigger

import (
	"slices"
	"strings"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

// applyRuns matches one run walk read under bound. A run enters the rule's map only when it passes the static
// filters, so only its result is left to decide; a run is keyed on its id, never its attempt, so it fires once,
// when one of its attempts first concludes with a result in the filter.
func (pr *pollRule) applyRuns(w *Walk[forgeapi.Run], bound, h time.Time) {
	rc := pr.repoCursor()
	complete := runWalkComplete(w)
	if rc.Runs == nil {
		if complete {
			pr.baselineRuns(rc, len(w.Items))
		}
		return
	}
	c := rc.Runs
	listed := map[int64]bool{}
	for i := range w.Items {
		r := &w.Items[i]
		if r.CreatedAt.Before(h) || r.CreatedAt.Before(c.BaselineAt) || !pr.admitsRun(r) {
			continue
		}
		listed[r.Ref.ID] = true
		if _, fired := c.Fired[r.Ref.ID]; !fired {
			pr.matchRun(c, r)
		}
	}
	if complete {
		pr.noteRunWalk(c, w, bound, h, listed)
		c.LastComplete = pr.start
	}
}

func (pr *pollRule) matchRun(c *runCursor, r *forgeapi.Run) {
	result := resultOf(r.State)
	if result != "" && slices.Contains(pr.ev.Filter.Result, result) {
		pr.enqueueRun(r, result)
		delete(c.Runs, r.Ref.ID)
		if c.Fired == nil {
			c.Fired = map[int64]time.Time{}
		}
		c.Fired[r.Ref.ID] = r.CreatedAt
		return
	}
	if c.Runs == nil {
		c.Runs = map[int64]*runEntry{}
	}
	e := &runEntry{CreatedAt: r.CreatedAt, State: r.State.String(), Hold: holdOutside}
	if result == "" {
		e.Hold = holdPending
	}
	if r.State == forgeapi.CheckUnknown {
		e.UnknownSince = pr.start
		if prev := c.Runs[r.Ref.ID]; prev != nil && !prev.UnknownSince.IsZero() {
			e.UnknownSince = prev.UnknownSince
		}
	}
	c.Runs[r.Ref.ID] = e
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
