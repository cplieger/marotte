package trigger

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The lists a feed reads, which tag a read's context and key its page counts and its GitLab pool.
const (
	listIssues = "issues"
	listPRs    = "prs"
	listRuns   = "runs"
)

// The pools a trigger read is counted in: GitHub keeps REST and graphql apart, and every other family has one counter.
const (
	poolCore    = "core"
	poolGraphQL = "graphql"
	poolAPI     = "api"
)

// readTag marks a request as the trigger poller's read of one list; pages counts its responses below 500, so a
// retried 5xx is not an extra page.
type readTag struct {
	list  string
	pages atomic.Int32
}

type readTagKey struct{}

// withRead tags ctx as the trigger poller's read of list.
func withRead(ctx context.Context, list string) (context.Context, *readTag) {
	tag := &readTag{list: list}
	return context.WithValue(ctx, readTagKey{}, tag), tag
}

func tagOf(ctx context.Context) *readTag {
	tag, _ := ctx.Value(readTagKey{}).(*readTag)
	return tag
}

// Budget is the trigger poller's own spend and the forge's reported pools, per connection, in memory. One Budget
// serves every connection; its observers and the poller share it.
type Budget struct {
	now   func() time.Time
	conns map[string]*connBudget
	// pages is each feed list's page count on its last complete walk, keyed by feedListKey.
	pages map[string]int
	// backedOff is, per feed list being skipped (feedListKey), the pool its back-off line named, so the resume line
	// names the same pool even when the list's cap pool differs from it.
	backedOff map[string]string
	mu        sync.Mutex
}

// NewBudget returns an empty budget.
func NewBudget() *Budget {
	return &Budget{now: time.Now, conns: map[string]*connBudget{}, pages: map[string]int{}, backedOff: map[string]string{}}
}

type connBudget struct {
	spent map[string][]spend
	pools map[string]*poolState
	// listPools is, on GitLab, the pools the last response to each list named: its throttle and that throttle's parent.
	listPools map[string][]string
	// waits is, per waitKey, when a read may run again after the forge refused or deferred one.
	waits map[string]time.Time
}

type spend struct {
	at time.Time
	n  int
}

type poolState struct {
	reset     time.Time
	limit     int
	remaining int
}

func (b *Budget) conn(id string) *connBudget {
	cb := b.conns[id]
	if cb == nil {
		cb = &connBudget{
			spent: map[string][]spend{}, pools: map[string]*poolState{},
			listPools: map[string][]string{}, waits: map[string]time.Time{},
		}
		b.conns[id] = cb
	}
	return cb
}

// Observe is connection conn's forgeapi attempt observer. It reads only the request and the response headers, since
// forgeapi sends a read a hook changes around its cache (forgeapi options.go, WithAttemptObserver).
func (b *Budget) Observe(conn string) func(next http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(req)
			if resp != nil {
				b.observe(conn, req, resp)
			}
			return resp, err
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (b *Budget) observe(conn string, req *http.Request, resp *http.Response) {
	family, _ := connectionParts(conn)
	tag := tagOf(req.Context())
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	cb := b.conn(conn)
	// A 5xx is retried for the same page, so it is charged below but not counted as a page.
	if tag != nil && resp.StatusCode < http.StatusInternalServerError {
		tag.pages.Add(1)
	}
	switch family {
	case kindGitHub:
		cb.recordGitHub(resp.Header)
		switch {
		case tag == nil:
		case strings.HasSuffix(req.URL.Path, "/graphql"):
			cb.charge(poolGraphQL, prListPagePoints, now)
		case resp.StatusCode != http.StatusNotModified:
			cb.charge(poolCore, 1, now)
		}
		return
	case kindGitLab:
		if name := cb.recordGitLab(resp.Header); name != "" && tag != nil {
			cb.listPools[tag.list] = append([]string{name}, gitlabParent(name)...)
		}
	default:
		cb.recordForgejo(resp.Header, now)
	}
	if tag != nil {
		cb.charge(poolAPI, 1, now)
	}
}

func (cb *connBudget) charge(pool string, n int, now time.Time) {
	cb.spent[pool] = append(cb.spent[pool], spend{at: now, n: n})
}

// spentIn prunes pool's spends older than an hour and answers the rest's sum.
func (cb *connBudget) spentIn(pool string, now time.Time) int {
	ss := cb.spent[pool]
	i := 0
	for i < len(ss) && !ss[i].at.After(now.Add(-time.Hour)) {
		i++
	}
	ss = ss[i:]
	cb.spent[pool] = ss
	total := 0
	for _, s := range ss {
		total += s.n
	}
	return total
}

// capFreesAt is when enough of pool's spend ages out to bring it under budgetCap.
func (cb *connBudget) capFreesAt(pool string, total int) time.Time {
	for _, s := range cb.spent[pool] {
		total -= s.n
		if total < budgetCap {
			return s.at.Add(time.Hour)
		}
	}
	return time.Time{}
}

func headerInt(h http.Header, name string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get(name)))
	return n, err == nil
}

func headerEpoch(h http.Header, name string) time.Time {
	s, err := strconv.ParseInt(strings.TrimSpace(h.Get(name)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

// recordPool keeps a response's report of pool, read the way forgeapi's family wire.go pool and signal read it.
func (cb *connBudget) recordPool(h http.Header, pool, limitH, remainingH, resetH string) {
	remaining, ok := headerInt(h, remainingH)
	if pool == "" || !ok {
		return
	}
	limit, _ := headerInt(h, limitH)
	cb.pools[pool] = &poolState{limit: max(limit, 0), remaining: remaining, reset: headerEpoch(h, resetH)}
}

func (cb *connBudget) recordGitHub(h http.Header) {
	cb.recordPool(h, strings.TrimSpace(h.Get("X-RateLimit-Resource")),
		"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset")
}

// recordGitLab keeps the throttle a response names and answers its name.
func (cb *connBudget) recordGitLab(h http.Header) string {
	name := strings.TrimSpace(h.Get("RateLimit-Name"))
	cb.recordPool(h, name, "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset")
	return name
}

// gitlabParent is the throttle a request answered under name also counted against, as forgeapi's gitlab parentPool
// reads it (https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/#response-headers).
func gitlabParent(name string) []string {
	switch name {
	case "throttle_authenticated_api", "throttle_authenticated_packages_api",
		"throttle_authenticated_files_api", "throttle_authenticated_deprecated_api":
		return nil
	}
	return []string{"throttle_authenticated_api"}
}

// recordForgejo reads Forgejo's `RateLimit: r=<remaining>;t=<seconds to reset>`, as forgeapi's gitea signal does;
// Gitea sends none. The header names no limit, so the limit is the highest remaining seen since the last reset, a
// lower bound on the quota.
func (cb *connBudget) recordForgejo(h http.Header, now time.Time) {
	raw := h.Get("RateLimit")
	if raw == "" {
		return
	}
	remaining, seconds, haveRemaining := 0, 0, false
	for part := range strings.SplitSeq(raw, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if !ok || err != nil {
			continue
		}
		switch key {
		case "r":
			remaining, haveRemaining = n, true
		case "t":
			seconds = n
		}
	}
	if !haveRemaining {
		return
	}
	ps := cb.pools[poolAPI]
	if ps == nil || !now.Before(ps.reset) {
		ps = &poolState{}
		cb.pools[poolAPI] = ps
	}
	ps.remaining, ps.limit = remaining, max(ps.limit, remaining)
	ps.reset = now.Add(time.Duration(seconds) * time.Second)
}
