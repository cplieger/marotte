package trigger

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// overForge is a harness whose run rule took its baseline at pollStart over a forge holding *runs.
func overForge(t *testing.T, ev *launchrule.EventSpec) (h *harness, ruleID string, runs *[]forgeapi.Run) {
	t.Helper()
	h = newHarness(t)
	r := h.create(ev)
	runs = &[]forgeapi.Run{}
	h.src.forge = runs
	h.poll()
	return h, r.ID, runs
}

// pollAt polls once at t.
func (h *harness) pollAt(t time.Time) {
	h.t.Helper()
	h.now = t
	h.poll()
}

// lastSince is the bound the latest run call sent.
func (h *harness) lastSince() time.Time {
	h.t.Helper()
	for _, c := range slices.Backward(h.src.recorded()) {
		if c.list == "runs" {
			return c.since
		}
	}
	h.t.Fatal("no run call was made")
	return time.Time{}
}

func (h *harness) gaps(ruleID string) []launchrule.Gap {
	h.t.Helper()
	return h.state(ruleID).raw.Status.Gaps
}

func (h *harness) gapsOf(ruleID, cause string) []launchrule.Gap {
	h.t.Helper()
	var out []launchrule.Gap
	for _, g := range h.gaps(ruleID) {
		if g.Cause == cause {
			out = append(out, g)
		}
	}
	return out
}

func TestDowntimeWithinWindowCaught(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	h.pollAt(pollStart.Add(time.Hour))
	*runs = append(*runs, run(41, forgeapi.CheckPassing, pollStart.Add(2*time.Hour)))
	h.pollAt(pollStart.Add(12 * time.Hour))
	if got := inputOf(h.queued(id), "run_id"); !eq(got, []string{"41"}) {
		t.Errorf("after 11 hours down queued %v, want run 41", got)
	}
	if g := h.gaps(id); len(g) != 0 {
		t.Errorf("gaps = %+v, want none inside the window", g)
	}
}

func TestDowntimePastWindowRecordsGap(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	for _, at := range []time.Duration{40 * time.Hour, 80 * time.Hour} {
		h.pollAt(pollStart.Add(at))
	}
	last := pollStart.Add(100 * time.Hour)
	h.pollAt(last)
	back := last.Add(60 * time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckPassing, back.Add(-time.Hour)))
	h.pollAt(back)
	h.poll()
	g := h.gaps(id)
	if len(g) != 1 || g[0].Cause != launchrule.GapOutage || !g[0].From.Equal(last) || !g[0].To.Equal(back) {
		t.Fatalf("gaps = %+v, want one outage from the last complete check to the return", g)
	}
	if calls := h.src.recorded(); !calls[len(calls)-2].since.Equal(horizon(back)) {
		t.Errorf("the walk after the outage sent since %v, want the horizon %v", calls[len(calls)-2].since, horizon(back))
	}
	if got := inputOf(h.queued(id), "run_id"); !eq(got, []string{"41"}) {
		t.Errorf("queued %v, want run 41, inside the horizon", got)
	}
}

func TestRunWalkKeepsBoundAcrossHour(t *testing.T) {
	h := newHarness(t)
	r := h.create(runSpec("success"))
	h.runPage()
	h.pollAt(pollStart.Add(-30 * time.Hour))
	capped := &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap}
	h.runPage(run(41, forgeapi.CheckFailing, pollStart.Add(-time.Hour)))
	h.src.runs[0].Next, h.src.runs[0].Partial = "c1", capped
	h.runPage(run(42, forgeapi.CheckPassing, pollStart.Add(-time.Hour)))
	clock := pollStart.Add(50 * time.Minute)
	p := &Poller{src: h.src, c: h.c, now: func() time.Time {
		t := clock
		clock = clock.Add(20 * time.Minute)
		return t
	}}
	p.Poll(t.Context(), Feeds(h.store.List())[0])

	calls := h.src.recorded()[1:]
	if len(calls) != 2 || calls[1].after != "c1" || !calls[0].since.Equal(calls[1].since) {
		t.Fatalf("calls = %+v, want two with one bound, the second resuming c1", calls)
	}
	if want := pollStart.Add(-30 * time.Hour).Truncate(time.Hour); !calls[0].since.Equal(want) {
		t.Errorf("since = %v, want %v", calls[0].since, want)
	}
	if got := inputOf(h.queued(r.ID), "run_id"); !eq(got, []string{"42"}) {
		t.Errorf("queued %v, want run 42 from the second page", got)
	}
}

func TestRunSteadyWindowAfterCatchUp(t *testing.T) {
	h, _, _ := overForge(t, runSpec("success"))
	h.pollAt(pollStart.Add(48 * time.Hour))
	back := pollStart.Add(78 * time.Hour)
	h.pollAt(back)
	if got, want := h.lastSince(), back.Add(-54*time.Hour); !got.Equal(want) {
		t.Errorf("the first walk after a 30-hour outage sent since %v, want 54 hours back, %v", got, want)
	}
	next := back.Add(5 * time.Minute)
	h.pollAt(next)
	if got, want := h.lastSince(), floorHour(back.Add(-runReach)); !got.Equal(want) {
		t.Errorf("the next walk sent since %v, want about 25 hours back, %v", got, want)
	}
}

func TestPendingRunHoldsBoundBack(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	created := pollStart.Add(time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckPending, created))
	h.pollAt(created.Add(40 * time.Hour))
	(*runs)[0].State = forgeapi.CheckPassing
	h.pollAt(created.Add(44 * time.Hour))
	if got := h.lastSince(); !got.Equal(floorHour(created)) {
		t.Errorf("since = %v, want the pending run's creation hour %v", got, floorHour(created))
	}
	if got := inputOf(h.queued(id), "run_id"); !eq(got, []string{"41"}) {
		t.Errorf("queued %v, want run 41, which ended while marotte was down", got)
	}
}

func TestPendingRunAgingOutRecordsGap(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	created := pollStart.Add(time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckPending, created))
	for _, at := range []time.Duration{30 * time.Hour, 60 * time.Hour, 70 * time.Hour} {
		h.pollAt(created.Add(at))
	}
	(*runs)[0].State = forgeapi.CheckPassing
	back := created.Add(74 * time.Hour)
	h.pollAt(back)
	g := h.gaps(id)
	if len(g) != 1 || g[0].Cause != launchrule.GapAgedOut || g[0].RunID != 41 || g[0].QueueFull {
		t.Fatalf("gaps = %+v, want one aged_out naming run 41 still running", g)
	}
	if q := h.queued(id); len(q) != 0 {
		t.Errorf("queued %v, want nothing for a run past the horizon", keysOf(q))
	}
	if e := h.cursorOf(id).Runs.Runs[41]; e != nil {
		t.Errorf("run 41 is still in the map: %+v", e)
	}
}

func TestOverflowedRunAgingOutRecordsGap(t *testing.T) {
	h, id, _ := overForge(t, runSpec("success"))
	created := pollStart.Add(time.Hour)
	es := h.state(id)
	es.cursor[Feed{Selector: testRepo}.RepoID()].Runs.Runs = map[int64]*runEntry{
		41: {CreatedAt: created, State: "passing", Hold: holdAtForge},
	}
	h.seed(id, &es)
	h.pollAt(created.Add(74 * time.Hour))
	g := h.gapsOf(id, launchrule.GapAgedOut)
	if len(g) != 1 || g[0].RunID != 41 || !g[0].QueueFull {
		t.Errorf("gaps = %+v, want one aged_out naming run 41 as held while the queue was full", g)
	}
}

func TestUnlistedPendingRunKept(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	created := pollStart.Add(time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckPending, created))
	h.pollAt(created.Add(time.Hour))
	*runs = nil
	at := created.Add(2 * time.Hour)
	h.pollAt(at)
	c := h.cursorOf(id).Runs
	if e := c.Runs[41]; e == nil || !e.Unlisted || e.Hold != holdPending {
		t.Fatalf("run 41 = %+v, want kept pending and marked unlisted", e)
	}
	if got := runNotes(c, at); !eq(got, []string{"run 41 is no longer listed"}) {
		t.Errorf("notes = %q, want run 41 named as no longer listed", got)
	}
}

func TestResultWindowRecordsGapNotResumed(t *testing.T) {
	h := newHarness(t)
	r := h.create(runSpec("success"))
	h.runPage()
	h.poll()
	at := h.now
	h.src.runs = append(h.src.runs, forgeapi.Page[forgeapi.Run]{
		Items:   []forgeapi.Run{run(41, forgeapi.CheckPassing, pollStart.Add(time.Minute))},
		Partial: &forgeapi.Partial{Reason: forgeapi.PartialResultWindow, Fetched: 1000, OmittedAtLeast: 45},
	})
	h.poll()
	if calls := h.src.recorded(); len(calls) != 2 || calls[1].after != "" {
		t.Errorf("calls = %+v, want the result-window page read once and never resumed", calls)
	}
	if c := h.cursorOf(r.ID).Runs; !c.LastComplete.Equal(at) {
		t.Errorf("last_complete = %v, want %v: a result window counts as complete", c.LastComplete, at)
	}
	g := h.gaps(r.ID)
	if len(g) != 1 || g[0].Cause != launchrule.GapResultWindow || g[0].OmittedAtLeast != 45 {
		t.Errorf("gaps = %+v, want one result_window omitting at least 45", g)
	}
	if got := inputOf(h.queued(r.ID), "run_id"); !eq(got, []string{"41"}) {
		t.Errorf("queued %v, want the run the window listed", got)
	}
}

func TestResultWindowExtendsOverlappingGap(t *testing.T) {
	logs := captureLogs(t)
	h := newHarness(t)
	r := h.create(runSpec("success"))
	h.runPage()
	h.poll()
	window := func(id int64, created time.Time, omitted int) {
		h.src.runs = append(h.src.runs, forgeapi.Page[forgeapi.Run]{
			Items:   []forgeapi.Run{run(id, forgeapi.CheckPassing, created)},
			Partial: &forgeapi.Partial{Reason: forgeapi.PartialResultWindow, Fetched: 1000, OmittedAtLeast: omitted},
		})
	}
	// Clean walks first, so each windowed walk's bound sits past the baseline and moves with last_complete.
	for _, at := range []time.Duration{20 * time.Hour, 40 * time.Hour} {
		h.pollAt(pollStart.Add(at))
	}
	crowded := pollStart.Add(40*time.Hour + 30*time.Minute)
	for i, at := range []time.Duration{41 * time.Hour, 43 * time.Hour, 45 * time.Hour} {
		window(41, crowded, 45+15*i)
		h.pollAt(pollStart.Add(at))
	}
	g := h.gapsOf(r.ID, launchrule.GapResultWindow)
	if len(g) != 1 || !g[0].To.Equal(pollStart.Add(45*time.Hour)) || g[0].OmittedAtLeast != 75 {
		t.Errorf("gaps = %+v, want one result_window to the last poll omitting at least 75", g)
	}
	if n := strings.Count(logs.String(), logMsgCatchUpGap); n != 1 {
		t.Errorf("logged %d catch-up gaps over one crowded stretch, want 1", n)
	}

	for _, at := range []time.Duration{60 * time.Hour, 80 * time.Hour} {
		h.pollAt(pollStart.Add(at))
	}
	window(42, pollStart.Add(80*time.Hour+30*time.Minute), 10)
	h.pollAt(pollStart.Add(81 * time.Hour))
	if g := h.gapsOf(r.ID, launchrule.GapResultWindow); len(g) != 2 {
		t.Errorf("gaps = %+v, want a second result_window after a clean stretch", g)
	}
	if n := strings.Count(logs.String(), logMsgCatchUpGap); n != 2 {
		t.Errorf("logged %d catch-up gaps, want 2", n)
	}
}

func TestRerunWithinReachFires(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	created := pollStart.Add(time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckFailing, created))
	h.pollAt(created.Add(30 * time.Minute))
	h.pollAt(created.Add(20*time.Hour - 5*time.Minute))
	(*runs)[0].State = forgeapi.CheckPassing
	h.pollAt(created.Add(20 * time.Hour))
	if got := inputOf(h.queued(id), "run_id"); !eq(got, []string{"41"}) {
		t.Errorf("queued %v, want the re-run of run 41, created within 24 hours of the last check", got)
	}
}

func TestRerunPastReachNotRead(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	created := pollStart.Add(time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckFailing, created))
	h.pollAt(created.Add(30 * time.Minute))
	h.pollAt(created.Add(26*time.Hour - 5*time.Minute))
	(*runs)[0].State = forgeapi.CheckPassing
	h.pollAt(created.Add(26 * time.Hour))
	if q := h.queued(id); len(q) != 0 {
		t.Errorf("queued %v, want nothing: run 41 was created more than 24 hours before the last check", keysOf(q))
	}
	if got := h.lastSince(); !got.After(created) {
		t.Errorf("since = %v, want past the run's creation %v", got, created)
	}
}

func TestFiredKeptUntilFlooredHorizon(t *testing.T) {
	h := newHarness(t)
	r := h.create(runSpec("success"))
	h.runPage()
	h.poll()
	start := pollStart.Add(100*time.Hour + 15*time.Minute)
	fired := start.Add(-runHorizon - 10*time.Minute)
	es := h.state(r.ID)
	es.cursor[Feed{Selector: testRepo}.RepoID()].Runs.Fired = map[int64]time.Time{41: fired}
	h.seed(r.ID, &es)
	early := horizon(start).Add(-5 * time.Minute)
	h.runPage(run(41, forgeapi.CheckPassing, fired), run(40, forgeapi.CheckPassing, early))
	h.pollAt(start)

	if got := h.lastSince(); !got.Equal(horizon(start)) {
		t.Errorf("since = %v, want the horizon %v", got, horizon(start))
	}
	if q := h.queued(r.ID); len(q) != 0 {
		t.Errorf("queued %v, want nothing: 41 already fired, and 40 was created before the horizon", keysOf(q))
	}
	c := h.cursorOf(r.ID).Runs
	if _, kept := c.Fired[41]; !kept {
		t.Error("run 41 left fired before the hour-floored horizon")
	}
	if _, in := c.Fired[40]; in || c.Runs[40] != nil {
		t.Errorf("run 40, created before the horizon, was recorded: fired %v, runs %v", c.Fired, c.Runs)
	}
}

func TestUnmatchablePendingRunDoesNotHoldBound(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	waiting := run(41, forgeapi.CheckPending, pollStart.Add(time.Hour))
	waiting.Trigger = forgeapi.TriggerPullRequest
	*runs = append(*runs, waiting)
	h.pollAt(pollStart.Add(48 * time.Hour))
	next := pollStart.Add(48*time.Hour + 5*time.Minute)
	h.pollAt(next)
	if got, want := h.lastSince(), floorHour(pollStart.Add(48*time.Hour-runReach)); !got.Equal(want) {
		t.Errorf("since = %v, want last_complete less 24 hours, %v", got, want)
	}
	if c := h.cursorOf(id).Runs; len(c.Runs) != 0 || len(h.gaps(id)) != 0 {
		t.Errorf("runs %v, gaps %v; want neither for a run no filter admits", c.Runs, h.gaps(id))
	}
}

func TestSharedFeedBoundOnlyForAdmittedRuns(t *testing.T) {
	other := runSpec("success")
	other.Filter.Workflow = ".github/workflows/other.yaml"
	for name, tc := range map[string]struct {
		workflow string
		want     time.Time
	}{
		"admitted by the second rule": {".github/workflows/other.yaml", floorHour(pollStart.Add(time.Hour))},
		"admitted by neither":         {".github/workflows/third.yaml", floorHour(pollStart.Add(48*time.Hour - runReach))},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.create(runSpec("success"))
			h.create(other)
			runs := []forgeapi.Run{run(41, forgeapi.CheckPending, pollStart.Add(time.Hour))}
			runs[0].WorkflowPath = tc.workflow
			h.src.forge = &runs
			h.poll()
			h.pollAt(pollStart.Add(48 * time.Hour))
			h.pollAt(pollStart.Add(48*time.Hour + 5*time.Minute))
			if got := h.lastSince(); !got.Equal(tc.want) {
				t.Errorf("the shared walk sent since %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnknownStateRunHeldPendingAndNamed(t *testing.T) {
	h, id, runs := overForge(t, runSpec("success"))
	*runs = append(*runs, run(41, forgeapi.CheckUnknown, pollStart.Add(time.Hour)))
	first := pollStart.Add(2 * time.Hour)
	h.pollAt(first)
	c := h.cursorOf(id).Runs
	if e := c.Runs[41]; e == nil || e.Hold != holdPending || !e.UnknownSince.Equal(first) {
		t.Fatalf("run 41 = %+v, want held pending, unknown since %v", e, first)
	}
	if got := runNotes(c, first); len(got) != 0 {
		t.Errorf("notes at first sight = %q, want none", got)
	}
	later := first.Add(24 * time.Hour)
	h.pollAt(later)
	c = h.cursorOf(id).Runs
	if got := runNotes(c, later); !eq(got, []string{"run 41 reads an unknown state"}) {
		t.Errorf("notes after 24 hours = %q, want run 41 named", got)
	}
}
