package trigger

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/marotte/internal/launchrule"
)

// prListPagePoints is what GitHub charges the graphql pool for one page of forgeapi's PRList document, as
// testdata/prlist-cost.json records it (TestProbePRListCost regenerates it). GitHub prices a document from the page
// sizes it asks for, not the rows it returns, so a page costs this on any repository
// (https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api).
const prListPagePoints = 2

// budgetCap is the trigger poller's hourly spend per connection and pool: 12 % of GitHub's 5,000, so the rest of the
// user's quota stays with the other applications acting for him.
const budgetCap = 600

// floorPercent is the share of a forge pool under which a read waits for the pool's reset. On GitHub it is 1,000,
// above forgeapi's mutation reserve of 50.
const floorPercent = 20

// The pages a feed list not yet read whole is priced at. A run walk's size is the one a repository's name does not
// predict, hence 3.
var unreadPages = map[string]int{listIssues: 1, listPRs: 1, listRuns: 3}

// The reasons a read is skipped, as the backed-off line names them.
const (
	reasonForgeBudget   = "forge_budget"
	reasonRateLimited   = "rate_limited"
	reasonTriggerBudget = "trigger_budget"
)

// errBackedOff ends a list walk the budget skipped; the walk is partial and read again after the back-off.
var errBackedOff = errors.New("trigger poll backed off")

// backoff is why a feed list's read was skipped and when it may run.
type backoff struct {
	until     time.Time
	pool      string
	reason    string
	remaining int
	spent     int
}

// rowText is the line a paused rule's row reads.
func (bo *backoff) rowText() string {
	at := bo.until.Local().Format("15:04")
	switch bo.reason {
	case reasonTriggerBudget:
		return fmt.Sprintf("paused: the trigger budget for %s is spent (%s this hour), resumes %s", bo.pool, grouped(bo.spent), at)
	case reasonForgeBudget:
		return fmt.Sprintf("paused for the forge budget: %s has %s %s left, resumes %s", bo.pool, grouped(bo.remaining), unitOf(bo.pool), at)
	}
	return fmt.Sprintf("paused: the forge is rate limiting %s, resumes %s", bo.pool, at)
}

// grouped writes n with a comma between groups of three digits, the way the sentences a user reads write counts.
func grouped(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if n < 0 {
		sign, s = "-", s[1:]
	}
	var out strings.Builder
	out.WriteString(sign)
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func unitOf(pool string) string {
	if pool == poolGraphQL {
		return "points"
	}
	return "requests"
}

// capPool is the pool a list's reads are counted in on the connection's family.
func capPool(conn, list string) string {
	if family, _ := connectionParts(conn); family != kindGitHub {
		return poolAPI
	}
	if list == listPRs {
		return poolGraphQL
	}
	return poolCore
}

// floorPools are the forge pools a list's read draws on: fixed on GitHub, the ones the list's last response named on
// GitLab (none before its first), and the one pool elsewhere.
func (cb *connBudget) floorPools(conn, list string) []string {
	switch family, _ := connectionParts(conn); family {
	case kindGitHub:
		return []string{capPool(conn, list)}
	case kindGitLab:
		return cb.listPools[list]
	}
	return []string{poolAPI}
}

// waitKey is what the forge's refusal or deferral of list's read holds: the list on GitLab, whose governor defers
// each throttle apart (forgeapi internal/transport governor.go admit), and the cap pool elsewhere, since GitHub's
// core serves issues and runs alike.
func waitKey(conn, list string) string {
	if family, _ := connectionParts(conn); family == kindGitLab {
		return keyenc.Join(poolAPI, list)
	}
	return capPool(conn, list)
}

// refused is list's rate_limited back-off until until, naming the floor pool with the fewest requests left, or the
// first floor pool (the cap pool when none is known) while no response reported one.
func (cb *connBudget) refused(conn, list string, until time.Time) *backoff {
	bo := &backoff{pool: capPool(conn, list), reason: reasonRateLimited, until: until}
	names := cb.floorPools(conn, list)
	if len(names) > 0 {
		bo.pool = names[0]
	}
	seen := false
	for _, name := range names {
		if ps := cb.pools[name]; ps != nil && (!seen || ps.remaining < bo.remaining) {
			bo.pool, bo.remaining, seen = name, ps.remaining, true
		}
	}
	return bo
}

func feedListKey(f Feed, list string) string { return keyenc.Join(f.key(), list) }

// hold answers why f's read of list must wait, nil when it may run, logging a feed pool's first skip and its end:
// the lists of a feed sharing a pool log one back-off between them.
func (b *Budget) hold(f Feed, list string) *backoff {
	b.mu.Lock()
	defer b.mu.Unlock()
	bo := b.check(f.Connection, list)
	key := feedListKey(f, list)
	held, wasHeld := b.backedOff[key]
	if wasHeld && (bo == nil || bo.pool != held) {
		delete(b.backedOff, key)
		if !b.feedHolds(f, held) {
			slog.Info(logMsgResumed, "connection", f.Connection, "repo", f.Selector, "pool", held)
		}
	}
	if bo != nil && (!wasHeld || bo.pool != held) {
		if !b.feedHolds(f, bo.pool) {
			slog.Warn(logMsgBackedOff, "connection", f.Connection, "repo", f.Selector, "pool", bo.pool,
				"reason", bo.reason, "remaining", bo.remaining, "resumes_at", bo.until)
		}
		b.backedOff[key] = bo.pool
	}
	return bo
}

// forget drops the back-off of f's list that no rule reads any more, logging its pool's end when no other list of f
// is skipped for it; without it the stale entry keeps feedHolds true and silences the feed's lines for that pool.
func (b *Budget) forget(f Feed, list string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := feedListKey(f, list)
	held, ok := b.backedOff[key]
	if !ok {
		return
	}
	delete(b.backedOff, key)
	if !b.feedHolds(f, held) {
		slog.Info(logMsgResumed, "connection", f.Connection, "repo", f.Selector, "pool", held)
	}
}

// feedHolds reports whether a list of f is skipped for pool.
func (b *Budget) feedHolds(f Feed, pool string) bool {
	for _, list := range []string{listIssues, listPRs, listRuns} {
		if held, ok := b.backedOff[feedListKey(f, list)]; ok && held == pool {
			return true
		}
	}
	return false
}

func (b *Budget) check(conn, list string) *backoff {
	now := b.now()
	cb := b.conn(conn)
	pool := capPool(conn, list)
	if until := cb.waits[waitKey(conn, list)]; now.Before(until) {
		return cb.refused(conn, list, until)
	}
	if spent := cb.spentIn(pool, now); spent >= budgetCap {
		return &backoff{pool: pool, reason: reasonTriggerBudget, spent: spent, until: cb.capFreesAt(pool, spent)}
	}
	for _, name := range cb.floorPools(conn, list) {
		ps := cb.pools[name]
		if ps == nil || ps.reset.IsZero() || !now.Before(ps.reset) {
			continue
		}
		if ps.remaining*100 < ps.limit*floorPercent {
			return &backoff{pool: name, reason: reasonForgeBudget, remaining: ps.remaining, until: ps.reset}
		}
	}
	return nil
}

// walked records what a read of f's list ended with: its page count when it was whole, and the forge's refusal or
// its governor's deferral, which hold the list's waitKey until the wait the forge named or its floor pools' reset.
func (b *Budget) walked(f Feed, list string, pages int, complete bool, err error, partial *forgeapi.Partial) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if complete {
		b.pages[feedListKey(f, list)] = max(pages, 1)
	}
	cb := b.conn(f.Connection)
	var reset time.Time
	for _, name := range cb.floorPools(f.Connection, list) {
		if ps := cb.pools[name]; ps != nil && ps.reset.After(reset) {
			reset = ps.reset
		}
	}
	var until time.Time
	fe, isForge := errors.AsType[*forgeapi.Error](err)
	switch {
	case isForge && fe.Kind == forgeapi.KindRateLimited && fe.RetryAfter > 0:
		until = b.now().Add(fe.RetryAfter)
	case isForge && fe.Kind == forgeapi.KindRateLimited, partial != nil && partial.Reason == forgeapi.PartialRateLimited:
		until = reset
	}
	if until.After(b.now()) {
		cb.waits[waitKey(f.Connection, list)] = until
	}
}

// pagesOf is f's list's page count for the estimate.
func (b *Budget) pagesOf(f Feed, list string) int {
	if n, ok := b.pages[feedListKey(f, list)]; ok {
		return n
	}
	return unreadPages[list]
}

// spent is conn's trigger spend in pool over the hour before now.
func (b *Budget) spent(conn, pool string, now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn(conn).spentIn(pool, now)
}

// EstimateError is Estimate's refusal; Text is the sentence the saving user reads.
type EstimateError struct{ Text string }

func (e *EstimateError) Error() string { return e.Text }

// Estimate refuses cand when, saved over the rule replaces names (empty for a new rule) beside the other enabled event
// rules, its connection's worst-case hour would pass the trigger budget in a pool cand adds to, with an *EstimateError
// naming the pool, the hour's spend and the interval that would fit.
func (b *Budget) Estimate(rules []launchrule.Rule, cand *launchrule.Rule, replaces string) error {
	ev := cand.When.Event
	if ev == nil || !cand.Enabled {
		return nil
	}
	var others []launchrule.Rule
	for i := range rules {
		if rules[i].ID != replaces && rules[i].When.Event != nil && rules[i].When.Event.Connection == ev.Connection {
			others = append(others, rules[i])
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	without := b.hourly(others)
	with := b.hourly(append(others, *cand))
	for _, pool := range []string{poolCore, poolGraphQL, poolAPI} {
		if with[pool] <= budgetCap || with[pool] <= without[pool] {
			continue
		}
		_, host := connectionParts(ev.Connection)
		text := fmt.Sprintf("These rules could spend %s %s %s an hour on %s; the trigger budget is %s.",
			grouped(int(math.Ceil(with[pool]))), pool, unitOf(pool), host, grouped(budgetCap))
		if fit := b.fittingInterval(others, cand, pool); fit > 0 {
			return &EstimateError{Text: fmt.Sprintf("%s Set this one to %d minutes or more.", text, fit)}
		}
		return &EstimateError{Text: text + " Slow or disable another rule on this connection first."}
	}
	return nil
}

func (b *Budget) fittingInterval(others []launchrule.Rule, cand *launchrule.Rule, pool string) int {
	try := *cand
	ev := *cand.When.Event
	try.When.Event = &ev
	for m := ev.IntervalMin + 1; m <= launchrule.MaxIntervalMin; m++ {
		ev.IntervalMin = m
		if b.hourly(append(others, try))[pool] <= budgetCap {
			return m
		}
	}
	return 0
}

// hourly is the worst-case hour of rules per pool: each feed polls 60 ÷ its interval times, each poll reading every
// list its rules need at the list's page count, and a PRList page costs prListPagePoints.
func (b *Budget) hourly(rules []launchrule.Rule) map[string]float64 {
	out := map[string]float64{}
	for _, fr := range groupFeeds(rules) {
		polls := float64(time.Hour) / float64(feedInterval(fr.Rules))
		for _, list := range listsOf(fr.Rules) {
			cost := float64(b.pagesOf(fr.Feed, list))
			pool := capPool(fr.Feed.Connection, list)
			if pool == poolGraphQL {
				cost *= prListPagePoints
			}
			out[pool] += polls * cost
		}
	}
	return out
}

// listsOf is the lists a feed's rules read.
func listsOf(rules []launchrule.Rule) []string {
	need := map[string]bool{}
	for i := range rules {
		need[listOf(rules[i].When.Event.Kind)] = true
	}
	var out []string
	for _, list := range []string{listIssues, listPRs, listRuns} {
		if need[list] {
			out = append(out, list)
		}
	}
	return out
}

func listOf(kind launchrule.Kind) string {
	switch {
	case kind == launchrule.KindRunConcluded:
		return listRuns
	case itemPart(kind) == partIssue:
		return listIssues
	}
	return listPRs
}
