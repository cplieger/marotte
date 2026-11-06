package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// StateStore is the rule state a poll reads and writes, one rule's file per write.
type StateStore interface {
	State(ruleID string) (launchrule.State, error)
	WriteState(ctx context.Context, ruleID string, st *launchrule.State) error
}

// FeedRules is one feed and the enabled event rules that share it, in creation order.
type FeedRules struct {
	Feed  Feed
	Rules []launchrule.Rule
}

// Feeds groups the enabled event rules of rules, given in creation order, by feed. A feed reads the selector of
// its earliest-created rule.
func Feeds(rules []launchrule.Rule) []FeedRules {
	var out []FeedRules
	index := map[string]int{}
	for i := range rules {
		ev := rules[i].When.Event
		if ev == nil || !rules[i].Enabled {
			continue
		}
		f := Feed{Connection: ev.Connection, Selector: ev.Repo}
		if j, ok := index[f.Key()]; ok {
			out[j].Rules = append(out[j].Rules, rules[i])
			continue
		}
		index[f.Key()] = len(out)
		out = append(out, FeedRules{Feed: f, Rules: []launchrule.Rule{rules[i]}})
	}
	return out
}

// Poller reads feeds and queues what matched, one state write per rule and poll.
type Poller struct {
	src   FeedSource
	store StateStore
	now   func() time.Time
	marks *launchMarks
}

// NewPoller returns a Poller reading through src and keeping rule state in store.
//
//deadset:ignore DS1102 -- the trigger runner polls through it; until that lands only the tests do
func NewPoller(src FeedSource, store StateStore) *Poller {
	return &Poller{src: src, store: store, now: time.Now, marks: newLaunchMarks()}
}

// reads is what one poll read of a feed, each list walked at most once whatever its rules.
type reads struct {
	issues *Walk[forgeapi.Issue]
	prs    *Walk[forgeapi.PullRequest]
	runs   *Walk[forgeapi.Run]
	// since and horizon are the run walk's bound and H, both fixed when it starts.
	since   time.Time
	horizon time.Time
}

// Poll reads fr's lists once and applies them to every rule on it. A rule whose state cannot be read is skipped,
// and its file left as it is.
func (p *Poller) Poll(ctx context.Context, fr FeedRules) {
	start := p.now().UTC()
	rules := make([]*pollRule, 0, len(fr.Rules))
	for i := range fr.Rules {
		if pr := p.load(&fr.Rules[i], fr.Feed, start); pr != nil {
			rules = append(rules, pr)
		}
	}
	if len(rules) == 0 {
		return
	}
	rd := p.read(ctx, fr.Feed, rules, start)
	for _, pr := range rules {
		pr.apply(&rd)
		pr.logFull()
		p.write(ctx, pr)
	}
	slog.Debug(logMsgPoll, "connection", fr.Feed.Connection, "repo", fr.Feed.Selector,
		"reads", rd.calls(), "since", rd.since, "took", p.now().Sub(start))
}

func (p *Poller) load(r *launchrule.Rule, f Feed, start time.Time) *pollRule {
	st, err := p.store.State(r.ID)
	if err != nil {
		slog.Debug("trigger poll skipped a rule", "rule_id", r.ID, "error", err)
		return nil
	}
	es, err := decodeState(&st)
	if err != nil {
		slog.Error("trigger poll skipped a rule: its state does not decode", "rule_id", r.ID, "error", err)
		return nil
	}
	before, err := json.Marshal(&st)
	if err != nil {
		return nil
	}
	return &pollRule{rule: r, ev: r.When.Event, feed: f, start: start, st: es, before: before, marks: p.marks}
}

func (p *Poller) read(ctx context.Context, f Feed, rules []*pollRule, start time.Time) reads {
	var rd reads
	var needIssues, needPRs, needRuns bool
	for _, pr := range rules {
		switch {
		case pr.ev.Kind == launchrule.KindRunConcluded:
			needRuns = true
		case itemPart(pr.ev.Kind) == partIssue:
			needIssues = true
		default:
			needPRs = true
		}
	}
	if needIssues {
		w := walkIssues(ctx, p.src, f)
		rd.issues = &w
	}
	if needPRs {
		w := walkPRs(ctx, p.src, f)
		rd.prs = &w
	}
	if needRuns {
		rd.horizon = horizon(start)
		for _, pr := range rules {
			if pr.ev.Kind == launchrule.KindRunConcluded {
				pr.ageOut(rd.horizon)
			}
		}
		rd.since = runBound(rules, start, rd.horizon)
		w := walkRuns(ctx, p.src, f, rd.since)
		rd.runs = &w
	}
	return rd
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

// write stores pr's state when the poll changed it.
func (p *Poller) write(ctx context.Context, pr *pollRule) {
	st, err := pr.st.encode()
	if err == nil {
		var after []byte
		if after, err = json.Marshal(&st); err == nil && bytes.Equal(after, pr.before) {
			return
		}
	}
	if err == nil {
		err = p.store.WriteState(ctx, pr.rule.ID, &st)
	}
	if err != nil {
		slog.Error(logMsgNotWritten, "file", "launch-state/"+pr.rule.ID+".json", "op", "write",
			"rule_id", pr.rule.ID, "error", err)
	}
}

// pollRule is one rule's part of one poll.
type pollRule struct {
	rule *launchrule.Rule
	ev   *launchrule.EventSpec
	feed Feed
	// start is the poll's start, the instant every decision of the poll is taken at.
	start time.Time
	st    eventState
	// before is the state as loaded, so a poll that changed nothing writes nothing.
	before []byte
	// gain is this poll's minted label-gain instant, zero until its first gain.
	gain  time.Time
	marks *launchMarks
	// full is set once the poll left a match unqueued.
	full bool
}

func (pr *pollRule) labeled() bool {
	return pr.ev.Kind == launchrule.KindIssueLabeled || pr.ev.Kind == launchrule.KindPRLabeled
}

// repoCursor answers the feed repository's part of the rule's cursor, created empty.
func (pr *pollRule) repoCursor() *repoCursor {
	id := pr.feed.RepoID()
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
		pr.applyItems(items, rd.issues.Complete())
	default:
		kind, _ := connectionParts(pr.feed.Connection)
		pr.applyItems(prItems(rd.prs, kind == "gitlab"), rd.prs.Complete())
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
func prItems(w *Walk[forgeapi.PullRequest], gitlab bool) []item {
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
