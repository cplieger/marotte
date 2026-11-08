package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// feedRules is one feed and the enabled event rules that share it, in creation order.
type feedRules struct {
	Feed  Feed
	Rules []launchrule.Rule
}

// groupFeeds groups the enabled event rules of rules, given in creation order, by feed. A feed reads the selector of
// its earliest-created rule.
func groupFeeds(rules []launchrule.Rule) []feedRules {
	var out []feedRules
	index := map[string]int{}
	for i := range rules {
		ev := rules[i].When.Event
		if ev == nil || !rules[i].Enabled {
			continue
		}
		f := Feed{Connection: ev.Connection, Selector: ev.Repo}
		if j, ok := index[f.key()]; ok {
			out[j].Rules = append(out[j].Rules, rules[i])
			continue
		}
		index[f.key()] = len(out)
		out = append(out, feedRules{Feed: f, Rules: []launchrule.Rule{rules[i]}})
	}
	return out
}

// poller reads feeds and queues what matched, applying each rule's part through the Confirmer, which writes it.
type poller struct {
	src    FeedSource
	c      *Confirmer
	budget *Budget
	now    func() time.Time
}

// newPoller returns a poller reading through src within b and keeping rule state in c.
func newPoller(src FeedSource, c *Confirmer, b *Budget) *poller {
	return &poller{src: src, c: c, budget: b, now: time.Now}
}

// reads is what one poll read of a feed, each list walked at most once whatever its rules.
type reads struct {
	issues *listWalk[forgeapi.Issue]
	prs    *listWalk[forgeapi.PullRequest]
	runs   *listWalk[forgeapi.Run]
	// paused is each list the budget skipped, and why.
	paused map[string]*backoff
	// since and horizon are the run walk's bound and H, both fixed when it starts.
	since   time.Time
	horizon time.Time
}

// poll reads fr's lists once and applies them to every rule on it. A rule whose state cannot be read is skipped,
// and its file left as it is.
func (p *poller) poll(ctx context.Context, fr feedRules) {
	if run := p.begin(ctx, fr); run != nil {
		p.apply(run)
	}
}

// pollRun is one poll of a feed whose reads are back and not yet applied.
type pollRun struct {
	rd    reads
	start time.Time
	feed  Feed
	rules []*pollRule
}

// begin plans each rule's part on a snapshot of its state and reads the feed; nil when no rule is left to poll.
func (p *poller) begin(ctx context.Context, fr feedRules) *pollRun {
	start := p.now().UTC()
	rules := make([]*pollRule, 0, len(fr.Rules))
	for i := range fr.Rules {
		if pr := p.load(&fr.Rules[i], fr.Feed, start); pr != nil {
			rules = append(rules, pr)
		}
	}
	if len(rules) == 0 {
		return nil
	}
	return &pollRun{start: start, feed: fr.Feed, rules: rules, rd: p.read(ctx, fr.Feed, rules, start)}
}

// apply hands each rule its part of the reads, dropped for a rule edited since its snapshot.
func (p *poller) apply(run *pollRun) {
	for _, pr := range run.rules {
		p.c.update(pr.rule.ID, pr.gen, func(es *eventState) bool { return pr.applyTo(es, &run.rd) })
	}
	slog.Debug(logMsgPoll, "connection", run.feed.Connection, "repo", run.feed.Selector,
		"reads", run.rd.calls(), "since", run.rd.since, "took", p.now().Sub(run.start))
}

// load plans the rule's part of the poll on a copy of its state; the poll's changes are applied to the rule's
// current value, under its mutex, once the reads are back.
func (p *poller) load(r *launchrule.Rule, f Feed, start time.Time) *pollRule {
	if _, err := p.c.store.State(r.ID); err != nil {
		slog.Debug(logMsgRuleSkipped, "rule_id", r.ID, "error", err)
		return nil
	}
	es, gen, err := p.c.snapshot(r.ID)
	if errors.Is(err, errRuleUnreadable) {
		slog.Debug(logMsgRuleSkipped, "rule_id", r.ID, "error", err)
		return nil
	}
	if err != nil {
		slog.Error(logMsgStateBad, "rule_id", r.ID, "error", err)
		return nil
	}
	return &pollRule{rule: r, ev: r.When.Event, feed: f, start: start, st: es, gen: gen, marks: p.c.marks}
}

func (p *poller) read(ctx context.Context, f Feed, rules []*pollRule, start time.Time) reads {
	rd := reads{paused: map[string]*backoff{}}
	need := map[string]bool{}
	for _, pr := range rules {
		need[listOf(pr.ev.Kind)] = true
	}
	for _, list := range []string{listIssues, listPRs, listRuns} {
		if !need[list] {
			p.budget.forget(f, list)
		}
	}
	if need[listIssues] {
		rd.issues = readList(ctx, p, f, listIssues, &rd, func(ctx context.Context) listWalk[forgeapi.Issue] {
			return walkIssues(ctx, p.src, f)
		})
	}
	if need[listPRs] {
		rd.prs = readList(ctx, p, f, listPRs, &rd, func(ctx context.Context) listWalk[forgeapi.PullRequest] {
			return walkPRs(ctx, p.src, f)
		})
	}
	if need[listRuns] {
		rd.horizon = horizon(start)
		rd.since = runBound(rules, start, rd.horizon)
		rd.runs = readList(ctx, p, f, listRuns, &rd, func(ctx context.Context) listWalk[forgeapi.Run] {
			return walkRuns(ctx, p.src, f, rd.since)
		})
	}
	return rd
}

// readList walks f's list unless the budget holds it, in which case the walk is partial and reads nothing.
func readList[T any](ctx context.Context, p *poller, f Feed, list string, rd *reads,
	walkList func(context.Context) listWalk[T],
) *listWalk[T] {
	if bo := p.budget.hold(f, list); bo != nil {
		rd.paused[list] = bo
		return &listWalk[T]{Err: errBackedOff}
	}
	lctx, tag := withRead(ctx, list)
	w := walkList(lctx)
	complete := w.complete() || (w.Err == nil && w.Partial.Reason == forgeapi.PartialResultWindow)
	p.budget.walked(f, list, max(int(tag.pages.Load()), w.Calls), complete, w.Err, w.Partial)
	return &w
}

func (rd *reads) calls() int {
	n := 0
	if rd.issues != nil {
		n += rd.issues.Calls
	}
	if rd.prs != nil {
		n += rd.prs.Calls
	}
	if rd.runs != nil {
		n += rd.runs.Calls
	}
	return n
}

// pollRule is one rule's part of one poll.
type pollRule struct {
	rule  *launchrule.Rule
	ev    *launchrule.EventSpec
	marks *launchMarks
	// start is the poll's start, the instant every decision of the poll is taken at.
	start time.Time
	// gain is this poll's minted label-gain instant, zero until its first gain.
	gain time.Time
	feed Feed
	st   eventState
	// gen is the rule's generation when the poll took its snapshot.
	gen int
	// full is set once the poll left a match unqueued.
	full bool
}

// applyTo applies the poll's reads to the rule's current state es, answering whether they changed it.
func (pr *pollRule) applyTo(es *eventState, rd *reads) bool {
	before, err := stateJSON(es)
	pr.st = *es
	if pr.ev.Kind == launchrule.KindRunConcluded {
		pr.ageOut(rd.horizon)
	}
	pr.apply(rd)
	pr.logFull()
	pr.st.raw.Status.Paused = ""
	if bo := rd.paused[listOf(pr.ev.Kind)]; bo != nil {
		pr.st.raw.Status.Paused = bo.rowText()
	} else {
		pr.st.checkedAt = pr.start
		pr.showFull()
		pr.noteBaselineStop(rd)
	}
	*es = pr.st
	after, aerr := stateJSON(es)
	return err != nil || aerr != nil || !bytes.Equal(before, after)
}

// noteBaselineStop says where and why this poll's walk stopped short while the rule has no baseline yet.
func (pr *pollRule) noteBaselineStop(rd *reads) {
	pr.st.baselineStop = ""
	if rc := pr.st.cursor[pr.feed.repoID()]; rc != nil && !rc.empty() {
		return
	}
	var calls int
	var partial *forgeapi.Partial
	switch listOf(pr.ev.Kind) {
	case listIssues:
		calls, partial = stoppedShort(rd.issues)
	case listPRs:
		calls, partial = stoppedShort(rd.prs)
	default:
		calls, partial = stoppedShort(rd.runs)
	}
	if partial != nil {
		pr.st.baselineStop = baselineStopText(calls, partial.Reason)
	}
}

// stoppedShort answers a walk's call count and the marker it stopped on, or a nil marker for a walk that read to
// the end or failed, since a failed walk's last page says nothing of why it stopped.
func stoppedShort[T any](w *listWalk[T]) (int, *forgeapi.Partial) {
	if w == nil || w.Err != nil {
		return 0, nil
	}
	return w.Calls, w.Partial
}

func baselineStopText(calls int, reason forgeapi.PartialReason) string {
	pages := fmt.Sprintf("%d pages", calls)
	if calls == 1 {
		pages = "1 page"
	}
	return "the last walk stopped after " + pages + ": " + partialText(reason)
}

func partialText(r forgeapi.PartialReason) string {
	switch r {
	case forgeapi.PartialBudget:
		return "the forge client's request budget ran out"
	case forgeapi.PartialRateLimited:
		return "the forge's rate limit held the walk back"
	case forgeapi.PartialGraphQLPartial:
		return "the forge answered a page with errors"
	case forgeapi.PartialPaginationCap:
		return "the forge's page limit was reached"
	case forgeapi.PartialResultWindow:
		return "the forge lists no rows past its result window"
	default:
		return "the forge did not say why"
	}
}

func stateJSON(es *eventState) ([]byte, error) {
	st, err := es.encode()
	if err != nil {
		return nil, err
	}
	return json.Marshal(&st)
}

func (pr *pollRule) labeled() bool {
	return pr.ev.Kind == launchrule.KindIssueLabeled || pr.ev.Kind == launchrule.KindPRLabeled
}

// repoCursor answers the feed repository's part of the rule's cursor, created empty.
func (pr *pollRule) repoCursor() *repoCursor {
	id := pr.feed.repoID()
	rc := pr.st.cursor[id]
	if rc == nil {
		rc = &repoCursor{}
		pr.st.cursor[id] = rc
	}
	return rc
}

func (pr *pollRule) attrs() []any {
	return []any{"rule_id", pr.rule.ID, "recipe", pr.rule.Name, "repo", pr.ev.Repo, "kind", string(pr.ev.Kind)}
}

func (pr *pollRule) apply(rd *reads) {
	switch {
	case pr.ev.Kind == launchrule.KindRunConcluded:
		pr.applyRuns(rd.runs, rd.since, rd.horizon)
	case itemPart(pr.ev.Kind) == partIssue:
		items := issueItems(rd.issues.Items)
		pr.applyItems(items, rd.issues.complete())
	default:
		kind, _ := connectionParts(pr.feed.Connection)
		pr.applyItems(prItems(rd.prs, kind == kindGitLab), rd.prs.complete())
	}
}

func (pr *pollRule) applyItems(items []item, complete bool) {
	if pr.labeled() {
		pr.applyLabeled(items, complete)
		return
	}
	pr.applyOpened(items, complete)
}

// item is a labeled or opened kind's view of one issue or pull request row.
type item struct {
	author string
	// target is a pull request's target branch, empty for an issue.
	target string
	labels []forgeapi.Label
	number int
	// omitted is a cut row's lower bound on the labels it left out.
	omitted int
	// cut marks a row whose label set may be cut: an absent label on it is no observation.
	cut bool
}

func issueItems(in []forgeapi.Issue) []item {
	out := make([]item, 0, len(in))
	for i := range in {
		out = append(out, item{number: in[i].Ref.Number, author: in[i].Author, labels: in[i].Labels})
	}
	return out
}

// prItems reads a pull request walk's rows. A row's labels may be cut in exactly two cases: a GitLab row marked
// PartialPaginationCap, where the marker means its label connection names a further page, and any row of a call
// whose page is PartialGraphQLPartial, whose document answered data with errors forgeapi could not place on a row.
// Every other row is whole, whatever its own marker: on GitHub and Gitea a row's PartialPaginationCap is the
// check fold, and its labels are the whole set.
func prItems(w *listWalk[forgeapi.PullRequest], gitlab bool) []item {
	pageCut := w.Partial != nil && w.Partial.Reason == forgeapi.PartialGraphQLPartial
	out := make([]item, 0, len(w.Items))
	for i := range w.Items {
		p := &w.Items[i]
		it := item{number: p.Ref.Number, author: p.Author, labels: p.Labels, target: p.TargetBranch}
		switch {
		case pageCut && i >= w.lastFrom:
			it.cut, it.omitted = true, w.Partial.OmittedAtLeast
		case gitlab && p.Partial != nil && p.Partial.Reason == forgeapi.PartialPaginationCap:
			it.cut, it.omitted = true, p.Partial.OmittedAtLeast
		}
		out = append(out, it)
	}
	return out
}

// admitsItem applies the rule's static filter: a pull request kind's filter.branch is its target branch.
func (pr *pollRule) admitsItem(it *item) bool {
	return pr.ev.Filter.Branch == "" || it.target == pr.ev.Filter.Branch
}
