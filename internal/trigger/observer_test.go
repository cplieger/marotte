package trigger

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

const (
	gitlabConn  = "gitlab:gitlab.com"
	forgejoConn = "codeberg:codeberg.org"
	giteaConn   = "gitea:gitea.example.org"
)

// answerWith is a wire that answers every request with status and header, and counts what it was sent.
type answerWith struct {
	header http.Header
	status int
	sent   int
}

func (a *answerWith) RoundTrip(req *http.Request) (*http.Response, error) {
	a.sent++
	return &http.Response{StatusCode: a.status, Header: a.header.Clone(), Request: req, Body: http.NoBody}, nil
}

// send passes one request for path through b's attempt observer on conn, tagged as list when list is set.
func send(t *testing.T, b *Budget, conn, list, method, path string, wire http.RoundTripper) {
	t.Helper()
	ctx := context.Background()
	if list != "" {
		ctx, _ = withRead(ctx, list)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.example.org"+path, http.NoBody)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	resp, err := b.Observe(conn)(wire).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
}

func githubHeader(pool string, limit, remaining int, reset time.Time) http.Header {
	h := http.Header{}
	h.Set("X-RateLimit-Resource", pool)
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	return h
}

func gitlabHeader(pool string, limit, remaining int, reset time.Time) http.Header {
	h := http.Header{}
	h.Set("RateLimit-Name", pool)
	h.Set("RateLimit-Limit", strconv.Itoa(limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	return h
}

func budgetAt(now *time.Time) *Budget {
	b := NewBudget()
	b.now = func() time.Time { return *now }
	return b
}

func TestObserverCountsBilledNotNotModified(t *testing.T) {
	now := pollStart
	b := budgetAt(&now)
	reset := now.Add(time.Hour)
	ok := &answerWith{status: http.StatusOK, header: githubHeader(poolCore, 5000, 4000, reset)}
	notModified := &answerWith{status: http.StatusNotModified, header: githubHeader(poolCore, 5000, 4000, reset)}
	send(t, b, testConn, listIssues, http.MethodGet, "/repos/o/r/issues", ok)
	send(t, b, testConn, listIssues, http.MethodGet, "/repos/o/r/issues", notModified)
	send(t, b, testConn, "", http.MethodGet, "/repos/o/r/issues", ok)
	if got := b.spent(testConn, poolCore, now); got != 1 {
		t.Errorf("core spent = %d after a billed read, a 304 and another caller's read, want 1", got)
	}
	if got := b.spent(gitlabConn, poolAPI, now); got != 0 {
		t.Errorf("another connection's spend = %d, want 0", got)
	}
}

func TestObserverCountsRetriedAttempts(t *testing.T) {
	now := pollStart
	b := budgetAt(&now)
	wire := &answerWith{status: http.StatusBadGateway, header: http.Header{}}
	ctx, _ := withRead(context.Background(), listRuns)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.org/repos/o/r/actions/runs", http.NoBody)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	rt := b.Observe(testConn)(wire)
	for range 3 {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_ = resp.Body.Close()
	}
	if got := b.spent(testConn, poolCore, now); got != 3 {
		t.Errorf("core spent = %d after one request tried three times, want 3", got)
	}
}

func TestGraphQLPagesChargedInPoints(t *testing.T) {
	now := pollStart
	b := budgetAt(&now)
	wire := &answerWith{status: http.StatusOK, header: githubHeader(poolGraphQL, 5000, 4990, now.Add(time.Hour))}
	send(t, b, testConn, listPRs, http.MethodPost, "/graphql", wire)
	if got := b.spent(testConn, poolGraphQL, now); got != prListPagePoints {
		t.Errorf("graphql spent = %d after one PRList page, want %d", got, prListPagePoints)
	}
	if got := b.spent(testConn, poolCore, now); got != 0 {
		t.Errorf("core spent = %d after one PRList page, want 0", got)
	}
}

// budgetHarness is a harness polling through a budget on the harness's clock.
func budgetHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.budget = budgetAt(&h.now)
	return h
}

func callsOf(src *fakeSource, list string) int {
	n := 0
	for _, c := range src.recorded() {
		if c.list == list {
			n++
		}
	}
	return n
}

func (h *harness) status(ruleID string) launchrule.Status {
	h.t.Helper()
	return h.state(ruleID).raw.Status
}

func clockOf(t time.Time) string { return t.Local().Format("15:04") }

func TestTriggerBudgetPausesFeed(t *testing.T) {
	h := budgetHarness(t)
	logs := captureLogs(t)
	r := h.create(runSpec("success"))
	wire := &answerWith{status: http.StatusOK, header: http.Header{}}
	for range budgetCap {
		send(t, h.budget, testConn, listIssues, http.MethodGet, "/repos/o/r/issues", wire)
	}
	h.poll()
	h.poll()
	if n := callsOf(h.src, listRuns); n != 0 {
		t.Fatalf("%d run reads with the core trigger budget spent, want 0", n)
	}
	want := "paused: the trigger budget for core is spent (600 this hour), resumes " + clockOf(pollStart.Add(time.Hour))
	if got := h.status(r.ID).Paused; got != want {
		t.Errorf("Paused = %q, want %q", got, want)
	}
	if n := strings.Count(logs.String(), logMsgBackedOff); n != 1 {
		t.Errorf("%d %q lines over two skipped polls, want 1:\n%s", n, logMsgBackedOff, logs)
	}
	if !strings.Contains(logs.String(), "reason=trigger_budget") || !strings.Contains(logs.String(), "pool=core") {
		t.Errorf("the backed-off line names no reason or pool:\n%s", logs)
	}
	h.now = pollStart.Add(time.Hour + time.Minute)
	h.poll()
	if n := callsOf(h.src, listRuns); n != 1 {
		t.Errorf("%d run reads once the hour passed, want 1", n)
	}
	if got := h.status(r.ID).Paused; got != "" {
		t.Errorf("Paused = %q after the read, want empty", got)
	}
	if !strings.Contains(logs.String(), logMsgResumed) {
		t.Errorf("no %q line at the back-off's end:\n%s", logMsgResumed, logs)
	}
}

func prRuleSpec() *launchrule.EventSpec {
	return &launchrule.EventSpec{
		Connection: testConn, Repo: testRepo, Kind: launchrule.KindPROpened,
		Filter: launchrule.Filter{Authors: []string{"alice"}},
	}
}

func TestFloorReadsOwnPool(t *testing.T) {
	h := budgetHarness(t)
	runs, prs := h.create(runSpec("success")), h.create(prRuleSpec())
	reset := pollStart.Add(30 * time.Minute)
	send(t, h.budget, testConn, "", http.MethodGet, "/user", &answerWith{status: http.StatusOK, header: githubHeader(poolCore, 5000, 412, reset)})
	h.poll()
	if n := callsOf(h.src, listRuns); n != 0 {
		t.Errorf("%d run reads with core at 412 of 5000, want 0", n)
	}
	if n := callsOf(h.src, listPRs); n != 1 {
		t.Errorf("%d PR reads with graphql unseen, want 1", n)
	}
	want := "paused for the forge budget: core has 412 requests left, resumes " + clockOf(reset)
	if got := h.status(runs.ID).Paused; got != want {
		t.Errorf("run rule Paused = %q, want %q", got, want)
	}
	if got := h.status(prs.ID).Paused; got != "" {
		t.Errorf("PR rule Paused = %q, want empty", got)
	}
}

func TestFloorIgnoresOtherCallersLastPool(t *testing.T) {
	h := budgetHarness(t)
	h.create(runSpec("success"))
	reset := pollStart.Add(30 * time.Minute)
	send(t, h.budget, testConn, "", http.MethodGet, "/user", &answerWith{status: http.StatusOK, header: githubHeader(poolCore, 5000, 900, reset)})
	send(t, h.budget, testConn, "", http.MethodPost, "/graphql", &answerWith{status: http.StatusOK, header: githubHeader(poolGraphQL, 5000, 5000, reset)})
	h.poll()
	if n := callsOf(h.src, listRuns); n != 0 {
		t.Errorf("%d run reads after a full graphql answer followed a low core one, want 0", n)
	}
}

func TestGitLabFloorFromRateLimitHeaders(t *testing.T) {
	h := budgetHarness(t)
	spec := func(kind launchrule.Kind) *launchrule.EventSpec {
		ev := &launchrule.EventSpec{Connection: gitlabConn, Repo: testRepo, Kind: kind}
		if kind == launchrule.KindRunConcluded {
			ev.Filter = launchrule.Filter{Branch: "main", Result: []string{"success"}}
		}
		if kind == launchrule.KindIssueLabeled {
			ev.Filter = launchrule.Filter{Label: testLabel}
		}
		if kind == launchrule.KindPROpened {
			ev.Filter = launchrule.Filter{Authors: []string{"alice"}}
		}
		return ev
	}
	issues := h.create(spec(launchrule.KindIssueLabeled))
	h.create(spec(launchrule.KindRunConcluded))
	h.create(spec(launchrule.KindPROpened))
	reset := pollStart.Add(time.Minute)
	send(t, h.budget, gitlabConn, listIssues, http.MethodGet, "/projects/1/issues",
		&answerWith{status: http.StatusOK, header: gitlabHeader("burst_traffic_per_user_free_plan", 100, 10, reset)})
	send(t, h.budget, gitlabConn, listRuns, http.MethodGet, "/projects/1/pipelines",
		&answerWith{status: http.StatusOK, header: gitlabHeader("throttle_authenticated_api", 2000, 1900, reset)})
	h.poll()
	if n := callsOf(h.src, listIssues); n != 0 {
		t.Errorf("%d issue reads with the burst pool at 10 of 100, want 0", n)
	}
	if n := callsOf(h.src, listRuns); n != 1 {
		t.Errorf("%d run reads with their pool at 1900 of 2000, want 1", n)
	}
	if n := callsOf(h.src, listPRs); n != 1 {
		t.Errorf("%d merge request reads with no pool answered yet, want 1", n)
	}
	want := "paused for the forge budget: burst_traffic_per_user_free_plan has 10 requests left, resumes " + clockOf(reset)
	if got := h.status(issues.ID).Paused; got != want {
		t.Errorf("issue rule Paused = %q, want %q", got, want)
	}
}

func TestForgejoFloorFromParsedSignal(t *testing.T) {
	h := budgetHarness(t)
	ev := labeledSpec()
	ev.Connection = forgejoConn
	h.create(ev)
	forgejo := func(r int) *answerWith {
		hd := http.Header{}
		hd.Set("RateLimit", "r="+strconv.Itoa(r)+";t=600")
		return &answerWith{status: http.StatusOK, header: hd}
	}
	send(t, h.budget, forgejoConn, "", http.MethodGet, "/api/v1/user", forgejo(1000))
	send(t, h.budget, forgejoConn, "", http.MethodGet, "/api/v1/user", forgejo(150))
	h.poll()
	if n := callsOf(h.src, listIssues); n != 0 {
		t.Errorf("%d issue reads at 150 after a highest remaining of 1000, want 0", n)
	}
	h.now = pollStart.Add(11 * time.Minute)
	h.poll()
	if n := callsOf(h.src, listIssues); n != 1 {
		t.Errorf("%d issue reads after the reset, want 1", n)
	}

	g := budgetHarness(t)
	gev := labeledSpec()
	gev.Connection = giteaConn
	g.create(gev)
	for range 3 {
		send(t, g.budget, giteaConn, listIssues, http.MethodGet, "/api/v1/repos/o/r/issues", &answerWith{status: http.StatusOK, header: http.Header{}})
	}
	g.poll()
	if n := callsOf(g.src, listIssues); n != 1 {
		t.Errorf("%d Gitea issue reads with no budget signal, want 1", n)
	}
}

func TestLowForgeBudgetSkipsUntilReset(t *testing.T) {
	h := budgetHarness(t)
	logs := captureLogs(t)
	r := h.create(labeledSpec())
	reset := pollStart.Add(12 * time.Minute)
	send(t, h.budget, testConn, "", http.MethodGet, "/user", &answerWith{status: http.StatusOK, header: githubHeader(poolCore, 5000, 999, reset)})
	h.poll()
	h.poll()
	h.poll()
	if n := callsOf(h.src, listIssues); n != 0 {
		t.Errorf("%d issue reads before the reset, want 0", n)
	}
	if got := h.status(r.ID).Paused; !strings.HasPrefix(got, "paused for the forge budget: core has 999 requests left") {
		t.Errorf("Paused = %q before the reset", got)
	}
	h.poll()
	if n := callsOf(h.src, listIssues); n != 1 {
		t.Errorf("%d issue reads at the reset, want 1", n)
	}
	if n := strings.Count(logs.String(), logMsgBackedOff); n != 1 {
		t.Errorf("%d %q lines, want 1", n, logMsgBackedOff)
	}
	if n := strings.Count(logs.String(), logMsgResumed); n != 1 {
		t.Errorf("%d %q lines, want 1", n, logMsgResumed)
	}
}

func TestRateLimitedWaitsRetryAfter(t *testing.T) {
	h := budgetHarness(t)
	h.create(runSpec("success"))
	h.src.errs = map[string]error{listRuns: &forgeapi.Error{Kind: forgeapi.KindRateLimited, RetryAfter: 12 * time.Minute}}
	h.poll()
	h.src.errs = nil
	h.poll()
	h.poll()
	if n := callsOf(h.src, listRuns); n != 1 {
		t.Errorf("%d run reads inside the 12 minutes the forge asked for, want 1", n)
	}
	h.poll()
	if n := callsOf(h.src, listRuns); n != 2 {
		t.Errorf("%d run reads once the wait passed, want 2", n)
	}

	p := budgetHarness(t)
	p.create(labeledSpec())
	reset := pollStart.Add(7 * time.Minute)
	send(t, p.budget, testConn, "", http.MethodGet, "/user", &answerWith{status: http.StatusOK, header: githubHeader(poolCore, 5000, 3000, reset)})
	p.src.issues = []forgeapi.Page[forgeapi.Issue]{{Partial: &forgeapi.Partial{Reason: forgeapi.PartialRateLimited}}}
	p.poll()
	p.poll()
	if n := callsOf(p.src, listIssues); n != 1 {
		t.Errorf("%d issue reads after a deferred read and before the reset, want 1", n)
	}
	p.poll()
	if n := callsOf(p.src, listIssues); n != 2 {
		t.Errorf("%d issue reads after the reset, want 2", n)
	}
}

func TestBackoffPastWindowRecordsGap(t *testing.T) {
	h := budgetHarness(t)
	r := h.create(runSpec("success"))
	h.runPage()
	h.poll()
	reset := h.now.Add(50 * time.Hour)
	send(t, h.budget, testConn, "", http.MethodGet, "/user", &answerWith{status: http.StatusOK, header: githubHeader(poolCore, 5000, 10, reset)})
	h.poll()
	if gaps := h.status(r.ID).Gaps; len(gaps) != 0 {
		t.Fatalf("Setup: gaps %+v while backing off", gaps)
	}
	h.now = reset.Add(time.Minute)
	h.runPage()
	h.poll()
	gaps := h.status(r.ID).Gaps
	if len(gaps) != 1 || gaps[0].Cause != launchrule.GapOutage {
		t.Errorf("gaps = %+v after a 50-hour back-off, want one outage gap", gaps)
	}
}

func TestSinceRoundedToHour(t *testing.T) {
	h := newHarness(t)
	h.create(runSpec("success"))
	h.now = pollStart.Add(5 * time.Minute)
	h.runPage()
	h.poll()
	h.now = pollStart.Add(35 * time.Minute)
	h.runPage()
	h.poll()
	var since []time.Time
	for _, c := range h.src.recorded() {
		if c.list == listRuns {
			since = append(since, c.since)
		}
	}
	if len(since) != 2 || !since[0].Equal(since[1]) || !since[0].Equal(since[0].Truncate(time.Hour)) {
		t.Errorf("run walk bounds = %v, want one hour-floored bound twice", since)
	}
}

func TestFeedsStaggered(t *testing.T) {
	var feeds []feedRules
	for _, repo := range []string{"o/a", "o/b", "o/c"} {
		ev := labeledSpec()
		ev.Repo, ev.IntervalMin = repo, 6
		feeds = append(feeds, feedRules{Feed: Feed{Connection: testConn, Selector: repo}, Rules: []launchrule.Rule{{When: launchrule.When{Event: ev}}}})
	}
	s := newFeedSchedule()
	if got := len(s.due(feeds, pollStart)); got != 1 {
		t.Fatalf("%d feeds due at the start, want 1", got)
	}
	for i, want := range []time.Duration{0, 2 * time.Minute, 4 * time.Minute} {
		if got := s.feeds[feeds[i].Feed.key()].next.Sub(pollStart); got != want {
			t.Errorf("feed %d polls first at +%v, want +%v", i, got, want)
		}
	}
}

func TestEstimatePricesFeedsPerPool(t *testing.T) {
	now := pollStart
	b := budgetAt(&now)
	var rules []launchrule.Rule
	for i := range 16 {
		ev := runSpec("success")
		ev.Repo, ev.IntervalMin = "o/r"+strconv.Itoa(i), 5
		rules = append(rules, launchrule.Rule{ID: "r" + strconv.Itoa(i), Enabled: true, When: launchrule.When{Event: ev}})
	}
	next := runSpec("success")
	next.Repo, next.IntervalMin = "o/next", 5
	cand := launchrule.Rule{Enabled: true, When: launchrule.When{Event: next}}
	err := b.Estimate(rules, &cand, "")
	want := "These rules could spend 612 core requests an hour on github.com; the trigger budget is 600. Set this one to 8 minutes or more."
	if err == nil || err.Error() != want {
		t.Errorf("Estimate = %v, want %q", err, want)
	}
	if err := b.Estimate(rules[:15], &cand, ""); err != nil {
		t.Errorf("Estimate of 16 rules = %v, want nil", err)
	}
	if err := b.Estimate(rules, &rules[3], "r3"); err != nil {
		t.Errorf("Estimate of an unchanged rule = %v, want nil", err)
	}
	pr := prRuleSpec()
	pr.IntervalMin = 2
	prs := launchrule.Rule{Enabled: true, When: launchrule.When{Event: pr}}
	if err := b.Estimate(rules, &prs, ""); err != nil {
		t.Errorf("Estimate of a PR rule beside full core = %v, want nil: it spends graphql", err)
	}
}
