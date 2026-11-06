package trigger

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// runReach is how far before the last complete run walk a walk reads, which is what catches a re-run: GitHub
// keeps a run's created_at across re-runs.
const runReach = 24 * time.Hour

// runHorizon bounds every run walk: a weekend outage plus the Monday morning.
const runHorizon = 72 * time.Hour

// unknownNamedAfter is how long a run reads an unknown state before its rule's row names it.
const unknownNamedAfter = 24 * time.Hour

func floorHour(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

// horizon is a walk's H, fixed when it starts and used for every horizon decision of that walk. It is
// hour-floored like the bound and never later than it, so a run in fired stays there for as long as any bound
// can return it.
func horizon(start time.Time) time.Time { return floorHour(start.Add(-runHorizon)) }

// runBound is a feed's run walk bound, fixed for the walk: the earliest any of its run rules needs. It is
// hour-floored, so the walk's URLs repeat for up to an hour and an unchanged page answers 304, and every call of
// a walk crossing an hour sends the bound its continuation was minted under.
func runBound(rules []*pollRule, start, h time.Time) time.Time {
	bound := floorHour(start)
	for _, pr := range rules {
		if pr.ev.Kind == launchrule.KindRunConcluded {
			bound = minTime(bound, pr.runBound(start, h))
		}
	}
	return bound
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// runBound is the rule's own bound: runReach before its last complete walk, extended to every admitted run still
// pending or held at the forge, and never before the horizon or its baseline.
func (pr *pollRule) runBound(start, h time.Time) time.Time {
	rc := pr.st.cursor[pr.feed.RepoID()]
	if rc == nil || rc.Runs == nil {
		return floorHour(start)
	}
	c := rc.Runs
	b := c.LastComplete.Add(-runReach)
	for _, e := range c.Runs {
		if e.Hold != holdOutside {
			b = minTime(b, e.CreatedAt)
		}
	}
	b = floorHour(b)
	for _, floor := range []time.Time{h, floorHour(c.BaselineAt)} {
		if b.Before(floor) {
			b = floor
		}
	}
	return b
}

// ageOut prunes, at a walk's start, every run created before the horizon: from fired and the outside runs
// silently, and a pending or held run with a gap naming it, since it was never seen to end or never launched.
func (pr *pollRule) ageOut(h time.Time) {
	rc := pr.st.cursor[pr.feed.RepoID()]
	if rc == nil || rc.Runs == nil {
		return
	}
	c := rc.Runs
	maps.DeleteFunc(c.Fired, func(_ int64, created time.Time) bool { return created.Before(h) })
	for _, id := range slices.Sorted(maps.Keys(c.Runs)) {
		e := c.Runs[id]
		if !e.CreatedAt.Before(h) {
			continue
		}
		delete(c.Runs, id)
		if e.Hold != holdOutside {
			pr.recordGap(&launchrule.Gap{
				Cause: launchrule.GapAgedOut, From: e.CreatedAt, To: pr.start, RunID: id,
				QueueFull: e.Hold == holdAtForge,
			})
		}
	}
}

func (pr *pollRule) recordGap(g *launchrule.Gap) {
	pr.st.raw.Status.Gaps = append(pr.st.raw.Status.Gaps, *g)
	attrs := append(pr.attrs(), "cause", g.Cause, "from", g.From, "to", g.To)
	switch {
	case g.RunID != 0:
		attrs = append(attrs, "run_id", g.RunID)
	case g.Cause == launchrule.GapResultWindow:
		attrs = append(attrs, "omitted_at_least", g.OmittedAtLeast)
	}
	slog.Warn(logMsgCatchUpGap, attrs...)
}

// runWalkComplete counts a walk the route's result window ended as complete: it carries no continuation, and the
// same read would answer the same.
func runWalkComplete(w *Walk[forgeapi.Run]) bool {
	return w.Complete() || (w.Err == nil && w.Partial.Reason == forgeapi.PartialResultWindow)
}

// noteRunWalk records what a complete walk says about the rule's runs: a long outage, a result window, and each
// pending run the walk's bound reached and the forge no longer listed.
func (pr *pollRule) noteRunWalk(c *runCursor, w *Walk[forgeapi.Run], bound, h time.Time, listed map[int64]bool) {
	if c.LastComplete.Add(-runReach).Before(h) {
		pr.recordGap(&launchrule.Gap{Cause: launchrule.GapOutage, From: c.LastComplete, To: pr.start})
	}
	if w.Partial != nil && w.Partial.Reason == forgeapi.PartialResultWindow {
		pr.noteResultWindow(bound, w.Partial.OmittedAtLeast)
	}
	for id, e := range c.Runs {
		if e.Hold == holdPending && !e.CreatedAt.Before(bound) {
			e.Unlisted = !listed[id]
		}
	}
}

// noteResultWindow records a walk the result window ended. A crowded second stays inside every walk until the
// bound passes it, so a result_window gap the walk overlaps is extended in place and not logged again.
func (pr *pollRule) noteResultWindow(bound time.Time, omitted int) {
	gaps := pr.st.raw.Status.Gaps
	for i := len(gaps) - 1; i >= 0; i-- {
		if g := &gaps[i]; g.Cause == launchrule.GapResultWindow && !g.To.Before(bound) {
			g.To = pr.start
			g.OmittedAtLeast = max(g.OmittedAtLeast, omitted)
			return
		}
	}
	pr.recordGap(&launchrule.Gap{Cause: launchrule.GapResultWindow, From: bound, To: pr.start, OmittedAtLeast: omitted})
}

// runNotes are the row's remarks on a rule's watched runs at now: a run reading an unknown state for a day, and a
// pending run the forge no longer lists. Sorted by run id.
func runNotes(c *runCursor, now time.Time) []string {
	var notes []string
	for _, id := range slices.SortedFunc(maps.Keys(c.Runs), cmp.Compare[int64]) {
		e := c.Runs[id]
		switch {
		case e.Unlisted:
			notes = append(notes, fmt.Sprintf("run %d is no longer listed", id))
		case !e.UnknownSince.IsZero() && now.Sub(e.UnknownSince) >= unknownNamedAfter:
			notes = append(notes, fmt.Sprintf("run %d reads an unknown state", id))
		}
	}
	return notes
}
