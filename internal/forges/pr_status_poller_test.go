package forges

// Push when a pull request you opened flips green or red.
//
// The two gates promise zero cost with nothing pending, so they are asserted as
// ABSENCE OF WORK rather than as absence of a notification: with the gate closed
// the source is never consulted at all, and with no open PR the per-PR pass never runs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
)

var testConn = PRConnection{ID: "github:github.com", Account: "bob", WebBase: "https://github.com"}

func rowOf(pr *watchedPR) PR {
	return PR{RepoID: repoIDOf(pr.Repo), Repo: pr.Repo, Title: pr.Title, Number: pr.Number, Action: PRAction{Checks: pr.Check}}
}

func authoredRead(conn PRConnection, page scopePage, prs []watchedPR) ConnectionRead {
	page.Scope = authoredScope
	for i := range prs {
		page.Rows = append(page.Rows, rowOf(&prs[i]))
	}
	clone := CloneRepo{Dir: "marotte", ForgeID: conn.ID, RepoID: repoIDOf("cplieger/marotte")}
	return ConnectionRead{Conn: conn, Clones: []CloneRepo{clone}, Pages: []scopePage{page}}
}

// fakeSource answers one complete page of prs (or err) for testConn and counts how
// often it was asked, which is what makes "no forge work" assertable.
type fakeSource struct {
	err   error
	prs   []watchedPR
	calls int
}

func (f *fakeSource) Read(context.Context, bool, func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	f.calls++
	return []ConnectionRead{authoredRead(testConn, scopePage{Err: f.err}, f.prs)}
}

type sentPush struct {
	title   string
	body    string
	kind    marotte.PushKind
	subject marotte.PushSubject
}

type fakeNotifier struct {
	sent []sentPush
}

func (f *fakeNotifier) Notify(_ context.Context, n *marotte.NotificationPayload) {
	f.sent = append(f.sent, sentPush{title: n.Title, body: n.Body, kind: n.Kind, subject: n.PushSubject})
}

type fakeGate struct {
	push    bool
	present bool
	asked   int
}

func (g *fakeGate) Open() Gate {
	g.asked++
	return Gate{Present: g.present, Push: g.push}
}

func newTestPoller(src PRSource, n PRNotifier, g *fakeGate) *PRStatusPoller {
	p := NewPRStatusPoller(src, n, g.Open)
	// Only Run reads these; sweep is driven directly.
	p.tick = time.Millisecond
	p.discovery = time.Millisecond
	return p
}

func pr(number int, check string) watchedPR {
	return watchedPR{
		ForgeID: "github:github.com",
		Repo:    "cplieger/marotte",
		Number:  number,
		Title:   "A change",
		Check:   check,
	}
}

func checkOf(p *PRStatusPoller, number int) string {
	w := pr(number, "")
	return p.seen[marotte.PRSubject(w.ForgeID, repoIDOf(w.Repo), w.Number).Key].check
}

// TestPoller_DoesNoForgeWorkWithTheGateClosed is gate 1, and the assertion is on
// the SOURCE, not on the notifications: each source call spends forge requests from
// the user's shared quota, so a tick that listed first and consulted the gate second
// would spend them while nobody is looking and nobody asked to be told.
func TestPoller_DoesNoForgeWorkWithTheGateClosed(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(1, checkFailing)}}
	n := &fakeNotifier{}
	g := &fakeGate{push: false}
	p := newTestPoller(src, n, g)

	p.sweep(t.Context())
	p.sweep(t.Context())

	if src.calls != 0 {
		t.Errorf("the source was consulted %d times with the gate closed; the tick must cost nothing", src.calls)
	}
	if len(n.sent) != 0 {
		t.Errorf("pushed %d notifications with the gate closed", len(n.sent))
	}
	if g.asked != 2 {
		t.Errorf("the gate was consulted %d times over two sweeps, want 2", g.asked)
	}
}

// TestPoller_DoesNoPerPRWorkWithNoOpenPR is gate 2: one source call, then nothing.
func TestPoller_DoesNoPerPRWorkWithNoOpenPR(t *testing.T) {
	src := &fakeSource{prs: nil}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})

	p.sweep(t.Context())

	if src.calls != 1 {
		t.Errorf("source calls = %d, want 1", src.calls)
	}
	if len(n.sent) != 0 {
		t.Errorf("pushed %d notifications with no open PR", len(n.sent))
	}
	if len(p.seen) != 0 {
		t.Errorf("kept %d state entries with no open PR", len(p.seen))
	}
}

// TestPoller_FirstSightingSeedsSilently is the boot rule: without it every restart
// would announce every open PR, and a PR that went green while the container was
// down would arrive as a fresh alert hours late.
func TestPoller_FirstSightingSeedsSilently(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(7, checkPassing)}}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})

	p.sweep(t.Context())

	if len(n.sent) != 0 {
		t.Errorf("a first sighting pushed %d notifications: %+v", len(n.sent), n.sent)
	}
	if got := checkOf(p, 7); got != checkPassing {
		t.Errorf("state after seeding = %q, want %q", got, checkPassing)
	}
}

// TestPoller_PushesOnASettledFlip is the feature.
func TestPoller_PushesOnASettledFlip(t *testing.T) {
	cases := []struct {
		name     string
		from     string
		to       string
		wantBody string
	}{
		{name: "PendingToPassing", from: checkPending, to: checkPassing, wantBody: "Checks passed"},
		{name: "PendingToFailing", from: checkPending, to: checkFailing, wantBody: "Checks failed"},
		{name: "PassingToFailing", from: checkPassing, to: checkFailing, wantBody: "Checks failed"},
		{name: "FailingToPassing", from: checkFailing, to: checkPassing, wantBody: "Checks passed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{prs: []watchedPR{pr(42, tc.from)}}
			n := &fakeNotifier{}
			p := newTestPoller(src, n, &fakeGate{push: true})
			p.sweep(t.Context()) // seed

			src.prs = []watchedPR{pr(42, tc.to)}
			p.sweep(t.Context())

			if len(n.sent) != 1 {
				t.Fatalf("sent %d notifications for a %s -> %s flip, want 1: %+v",
					len(n.sent), tc.from, tc.to, n.sent)
			}
			got := n.sent[0]
			if got.kind != marotte.PushKindPRStatus {
				t.Errorf("kind = %q, want %q", got.kind, marotte.PushKindPRStatus)
			}
			if !strings.Contains(got.body, tc.wantBody) {
				t.Errorf("body %q does not say %q", got.body, tc.wantBody)
			}
			if got.title != "cplieger/marotte #42" {
				t.Errorf("title = %q, want the repo and number", got.title)
			}
		})
	}
}

// TestPoller_DoesNotPushIntoPendingOrNoVerdict: a run STARTING is not news (the
// user caused it seconds earlier by pushing) and unknown or neutral is not a
// verdict, so none of them interrupts.
func TestPoller_DoesNotPushIntoPendingOrNoVerdict(t *testing.T) {
	for _, to := range []string{checkPending, forgeapi.CheckUnknown.String(), forgeapi.CheckNeutral.String()} {
		t.Run("to_"+to, func(t *testing.T) {
			src := &fakeSource{prs: []watchedPR{pr(3, checkPassing)}}
			n := &fakeNotifier{}
			p := newTestPoller(src, n, &fakeGate{push: true})
			p.sweep(t.Context())

			src.prs = []watchedPR{pr(3, to)}
			p.sweep(t.Context())

			if len(n.sent) != 0 {
				t.Errorf("pushed on a flip into %q: %+v", to, n.sent)
			}
			// The state still MOVES, so the next flip back to passing is a change
			// rather than a repeat that gets swallowed.
			if got := checkOf(p, 3); got != to {
				t.Errorf("state = %q, want %q", got, to)
			}
		})
	}
}

// TestPoller_UnchangedVerdictIsSilent: a PR sitting green for an hour is sixty
// ticks and zero notifications.
func TestPoller_UnchangedVerdictIsSilent(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(9, checkPending)}}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})
	p.sweep(t.Context())
	src.prs = []watchedPR{pr(9, checkFailing)}
	p.sweep(t.Context())
	for range 10 {
		p.sweep(t.Context())
	}
	if len(n.sent) != 1 {
		t.Errorf("sent %d notifications for one flip followed by ten unchanged ticks", len(n.sent))
	}
}

// TestPoller_SubjectIsPerPR is the coalescing-tag half of the reuse: two PRs
// flipping inside one window must occupy their own tray slots.
func TestPoller_SubjectIsPerPR(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(1, checkPending), pr(2, checkPending)}}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})
	p.sweep(t.Context())

	src.prs = []watchedPR{pr(1, checkPassing), pr(2, checkFailing)}
	p.sweep(t.Context())

	if len(n.sent) != 2 {
		t.Fatalf("sent %d notifications for two flips, want 2: %+v", len(n.sent), n.sent)
	}
	if n.sent[0].subject.Key == n.sent[1].subject.Key {
		t.Errorf("two PRs share a subject key %q, so one would replace the other in the tray",
			n.sent[0].subject.Key)
	}
	for _, s := range n.sent {
		if !strings.HasPrefix(s.subject.Key, marotte.PRSubjectPrefix) {
			t.Errorf("subject %q lacks the PR prefix the client routes on", s.subject.Key)
		}
		if s.subject.ChatID != "" {
			t.Errorf("a PR notification carries a chat id (%q); it has no chat behind it", s.subject.ChatID)
		}
	}
}

// TestSweep_SubjectCarriesTheCanonicalRepoID: the subject is the repository's id,
// the one the PRs tab's rows carry, so the notice's click lands on its row; the
// display path names the repository in the body only, and a change to its
// spelling between sweeps is the same subject.
func TestSweep_SubjectCarriesTheCanonicalRepoID(t *testing.T) {
	const wantKey = "pr:github:github.com:v1.63706c69656765722f6d61726f747465#42"
	cases := []struct {
		name        string
		seedPath    string
		flipPath    string
		wantInTitle string
	}{
		{name: "DisplayPathDiffersFromTheID", seedPath: "CPlieger/Marotte", flipPath: "CPlieger/Marotte", wantInTitle: "CPlieger/Marotte #42"},
		{name: "DisplayPathRespeltBetweenSweeps", seedPath: "cplieger/marotte", flipPath: "CPlieger/Marotte", wantInTitle: "CPlieger/Marotte #42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := pr(42, checkPending)
			seed.Repo = tc.seedPath
			src := &fakeSource{prs: []watchedPR{seed}}
			n := &fakeNotifier{}
			p := newTestPoller(src, n, &fakeGate{push: true})
			p.sweep(t.Context())

			flip := pr(42, checkPassing)
			flip.Repo = tc.flipPath
			src.prs = []watchedPR{flip}
			p.sweep(t.Context())

			if len(n.sent) != 1 {
				t.Fatalf("sweep(seed %q, flip %q) sent %d notifications, want 1", tc.seedPath, tc.flipPath, len(n.sent))
			}
			if got := n.sent[0].subject.Key; got != wantKey {
				t.Errorf("subject key = %q, want %q", got, wantKey)
			}
			if got := n.sent[0].title; got != tc.wantInTitle {
				t.Errorf("title = %q, want %q", got, tc.wantInTitle)
			}
		})
	}
}

// TestPoller_ForgetsAClosedPR keeps the state bounded by OPEN pull requests rather
// than by every PR the process ever saw.
func TestPoller_ForgetsAClosedPR(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(1, checkPassing), pr(2, checkPassing)}}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})
	p.sweep(t.Context())
	if len(p.seen) != 2 {
		t.Fatalf("state entries = %d, want 2", len(p.seen))
	}
	src.prs = []watchedPR{pr(1, checkPassing)}
	p.sweep(t.Context())
	if len(p.seen) != 1 {
		t.Errorf("state entries = %d after a PR merged, want 1", len(p.seen))
	}
}

// TestPoller_ClosedGateResetsTheState: a gate that opens later must not announce
// every flip that happened while it was closed.
func TestPoller_ClosedGateResetsTheState(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(5, checkPending)}}
	n := &fakeNotifier{}
	g := &fakeGate{push: true}
	p := newTestPoller(src, n, g)
	p.sweep(t.Context())

	g.push = false
	p.sweep(t.Context())
	if len(p.seen) != 0 || len(p.walks) != 0 {
		t.Errorf("state survived a closed gate: %+v, walks %+v", p.seen, p.walks)
	}

	g.push = true
	src.prs = []watchedPR{pr(5, checkFailing)}
	p.sweep(t.Context())
	if len(n.sent) != 0 {
		t.Errorf("reopening the gate announced a flip that happened while it was closed: %+v", n.sent)
	}
}

// TestPoller_SourceFailureIsSurvivable: a forge that is down, rate-limited or
// logged out must not silence the loop or lose the state it already holds.
func TestPoller_SourceFailureIsSurvivable(t *testing.T) {
	src := &fakeSource{prs: []watchedPR{pr(1, checkPending)}}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})
	p.sweep(t.Context())

	src.err = errors.New("gh: rate limited")
	src.prs = nil
	p.sweep(t.Context())
	if len(p.seen) != 1 {
		t.Errorf("a failed listing discarded the state: %+v", p.seen)
	}

	src.err = nil
	src.prs = []watchedPR{pr(1, checkPassing)}
	p.sweep(t.Context())
	if len(n.sent) != 1 {
		t.Errorf("sent %d notifications after recovering, want 1", len(n.sent))
	}
}

// TestPoller_RunStopsOnContextCancel pins the shutdown contract: cancellation and
// nothing else, with no goroutine left running.
func TestPoller_RunStopsOnContextCancel(t *testing.T) {
	src := &fakeSource{}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: false})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPoller_TwoRates is the honest cost model: the 60-second rate is spent only
// while something is being tracked, and an idle box falls back to discovery.
//
// The rate selector is `seen`, so this asserts the pairing rather than a wall clock:
// a sweep that found PRs leaves the active rate armed, and one that found none (or
// met a closed gate) leaves the discovery rate armed.
func TestPoller_TwoRates(t *testing.T) {
	src := &fakeSource{}
	g := &fakeGate{push: true}
	p := NewPRStatusPoller(src, &fakeNotifier{}, g.Open)

	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("a fresh poller arms %v, want the discovery interval %v", got, PRDiscoveryInterval)
	}

	src.prs = []watchedPR{pr(1, checkPending)}
	p.sweep(t.Context())
	if got := p.nextDelay(); got != prPollInterval {
		t.Errorf("with a PR tracked the poller arms %v, want the active interval %v", got, prPollInterval)
	}

	src.prs = nil
	p.sweep(t.Context())
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("with the PR closed the poller arms %v, want to fall back to discovery %v",
			got, PRDiscoveryInterval)
	}

	src.prs = []watchedPR{pr(1, checkPending)}
	p.sweep(t.Context())
	g.push = false
	p.sweep(t.Context())
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("with the gate closed the poller arms %v, want discovery %v",
			got, PRDiscoveryInterval)
	}

	// The active rate is faster than discovery, or the two names mean nothing.
	if prPollInterval >= PRDiscoveryInterval {
		t.Errorf("the active interval (%v) is not faster than discovery (%v)",
			prPollInterval, PRDiscoveryInterval)
	}
}

// slowSource records when each listing started and finished, and takes `delay` to
// answer. The delay IS the fixture: it models a slow forge, which is the case that
// turned the loop hot.
type slowSource struct {
	delay  time.Duration
	mu     sync.Mutex
	starts []time.Time
	ends   []time.Time
}

func (s *slowSource) Read(context.Context, bool, func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	s.mu.Lock()
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	time.Sleep(s.delay)
	s.mu.Lock()
	s.ends = append(s.ends, time.Now())
	s.mu.Unlock()
	return []ConnectionRead{authoredRead(testConn, scopePage{}, []watchedPR{pr(1, checkPending)})}
}

// TestPoller_SchedulesFromCompletion is the anti-hot-loop rule: a ticker retains one tick while a
// sweep runs, so an overlong sweep started another at once; the next sweep is timed from
// completion.
func TestPoller_SchedulesFromCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Millisecond
		src := &slowSource{delay: 2 * interval}
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{push: true}).Open)
		p.tick = interval
		p.discovery = interval

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			p.Run(ctx)
			close(done)
		}()
		// Synthetic time: long enough for three sweeps at (delay + interval) each.
		// No slack is needed and none is wanted — slack would be indistinguishable
		// from a sweep firing early.
		synctest.Sleep(3 * (src.delay + interval))
		cancel()
		<-done

		src.mu.Lock()
		starts := append([]time.Time(nil), src.starts...)
		ends := append([]time.Time(nil), src.ends...)
		src.mu.Unlock()

		if len(starts) < 2 {
			t.Fatalf("only %d sweeps ran; the fixture cannot witness the gap", len(starts))
		}
		// EXACTLY the interval. The defect being caught leaves the gap at zero.
		for i := 0; i+1 < len(starts) && i < len(ends); i++ {
			if gap := starts[i+1].Sub(ends[i]); gap != interval {
				t.Errorf("sweep %d started %v after sweep %d finished, want exactly %v: "+
					"the next sweep is being scheduled from a retained tick rather than from completion",
					i+1, gap, i, interval)
			}
		}
	})
}

// TestPoller_RunDoesNotSweepOnEntry: the first sweep only seeds, so sweeping at
// boot would spend a call per connection during startup to learn what the next
// sweep learns for free.
func TestPoller_RunDoesNotSweepOnEntry(t *testing.T) {
	src := &fakeSource{}
	g := &fakeGate{push: true}
	p := NewPRStatusPoller(src, &fakeNotifier{}, g.Open)
	// No sweep will fire inside this test, at either rate.
	p.tick = time.Hour
	p.discovery = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	cancel()
	<-done
	if g.asked != 0 || src.calls != 0 {
		t.Errorf("Run swept on entry (gate asked %d, source called %d)", g.asked, src.calls)
	}
}

// scopeRun connects id by token over wire, then sweeps twice push-only, the
// wire answering each path first names and then each path second names, and
// answers what was sent and the verdicts the poller held for the two pull
// requests the bodies carry, #4 and #5.
func scopeRun(t *testing.T, wire *pathWire, id, webBase string, first, second map[string]string) (sent []sentPush, held map[int]string) {
	t.Helper()
	h := newConnectHarness(t, wire)
	if rec := h.do(t, http.MethodPost, patPath(id), `{"token":"scope-secret"}`); rec.Code != http.StatusOK {
		t.Fatalf("Setup: POST %s = %d %s", patPath(id), rec.Code, rec.Body)
	}
	src := NewManagerPRSource(h.m, fixedOrigins(RepoOrigin{Dir: "app", WebBase: webBase, Slug: "alice/app"}))
	n := &fakeNotifier{}
	p := NewPRStatusPoller(src, n, (&fakeGate{push: true}).Open)
	maps.Copy(wire.bodies, first)
	p.sweep(t.Context())
	maps.Copy(wire.bodies, second)
	p.sweep(t.Context())
	held = map[int]string{}
	for _, tr := range p.seen {
		held[len(held)] = tr.check
	}
	if len(p.seen) != 2 {
		t.Fatalf("the poller tracked %d pull requests, want #4 and #5 (the forge saw %q)", len(p.seen), wire.requests())
	}
	return n.sent, held
}

func sortedBodies(sent []sentPush) []string {
	out := make([]string, 0, len(sent))
	for _, s := range sent {
		out = append(out, s.title+": "+s.body)
	}
	slices.Sort(out)
	return out
}

// TestProviderCheckVerdicts_MatchTheStatedScope ties the poller's documented
// scope to the library clients that decide it.
//
// The verdict is GitHub's ListMyPRs row, and on GitLab and the Gitea family a
// ReadPR of each tracked row, so every family whose CI reports a result notifies
// on both edges, which is what the settings copy promises. A family that loses
// an edge fails here, next to the sentence that has to be rewritten.
func TestProviderCheckVerdicts_MatchTheStatedScope(t *testing.T) {
	want := []string{"alice/app #4: Checks failed · PR 4", "alice/app #5: Checks passed · PR 5"}
	t.Run("GitHubNotifiesOnBothEdges", func(t *testing.T) {
		row := func(n int, state string) string {
			return fmt.Sprintf(`{"__typename":"PullRequest","number":%d,"title":"PR %d",`+
				`"url":"https://github.com/alice/app/pull/%d","state":"OPEN","isDraft":false,`+
				`"author":{"login":"alice"},"headRefName":"feat%d","baseRefName":"main",`+
				`"repository":{"nameWithOwner":"alice/app"},"commits":{"nodes":[{"commit":{"oid":"abc1234",`+
				`"statusCheckRollup":{"state":"%s","contexts":{"totalCount":1,"checkRunCountsByState":[],`+
				`"statusContextCountsByState":[{"count":1,"state":"%s"}],`+
				`"pageInfo":{"hasNextPage":false,"endCursor":"MQ"},"nodes":[]}}}}]}}`, n, n, n, n, state, state)
		}
		search := func(rows ...string) map[string]string {
			return map[string]string{"/graphql": `{"data":{"rateLimit":{"cost":1,"limit":5000,"remaining":4990,` +
				`"resetAt":"2026-01-02T00:00:00Z"},"search":{"issueCount":2,"pageInfo":{"hasNextPage":false,` +
				`"endCursor":null},"nodes":[` + strings.Join(rows, ",") + `]}}}`}
		}
		wire := &pathWire{bodies: map[string]string{"/user": `{"login":"alice"}`}}
		sent, _ := scopeRun(t, wire, "github:github.com", "https://github.com",
			search(row(4, "PENDING"), row(5, "PENDING")), search(row(4, "FAILURE"), row(5, "SUCCESS")))
		if got := sortedBodies(sent); !slices.Equal(got, want) {
			t.Errorf("GitHub notices = %q, want %q: both edges arrive on the row", got, want)
		}
	})
	t.Run("GitLabNotifiesOnBothEdges", func(t *testing.T) {
		row := func(n int) string {
			return fmt.Sprintf(`{"iid":%d,"state":"opened","title":"PR %d","author":{"username":"alice"},`+
				`"web_url":"https://gitlab.com/alice/app/-/merge_requests/%d","draft":false,`+
				`"source_branch":"feat%d","target_branch":"main","sha":"aaaaaaa%d","merge_status":"can_be_merged",`+
				`"detailed_merge_status":"mergeable","references":{"full":"alice/app!%d"}}`, n, n, n, n, n, n)
		}
		read := func(n int, pipeline string) string {
			return fmt.Sprintf(`{"data":{"project":{"fullPath":"alice/app",`+
				`"userPermissions":{"pushCode":true,"readMergeRequest":true},"mergeRequest":{"iid":"%d",`+
				`"title":"PR %d","description":"","author":{"username":"alice"},"sourceBranch":"feat%d",`+
				`"targetBranch":"main","webUrl":"https://gitlab.com/alice/app/-/merge_requests/%d",`+
				`"diffHeadSha":"aaaaaaa%d","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z",`+
				`"state":"opened","draft":false,"mergedAt":null,"mergeable":true,"detailedMergeStatus":"MERGEABLE",`+
				`"autoMergeEnabled":false,"labels":{"pageInfo":{"hasNextPage":false},"nodes":[]},`+
				`"headPipeline":{"status":"%s"}}},"queryComplexity":{"score":10,"limit":200}}}`, n, n, n, n, n, pipeline)
		}
		bodies := func(four, five string) map[string]string {
			return map[string]string{
				"/api/v4/merge_requests": "[" + row(4) + "," + row(5) + "]",
				"/api/graphql#4":         read(4, four),
				"/api/graphql#5":         read(5, five),
			}
		}
		// The PRRead document addresses one merge request by its variables, so the
		// wire answers it by the iid it names.
		wire := &pathWire{bodies: map[string]string{"/api/v4/user": `{"id":1,"username":"alice"}`}, key: func(r *http.Request) string {
			if r.URL.Path != "/api/graphql" {
				return r.URL.Path
			}
			var doc struct {
				Variables struct {
					IID string `json:"iid"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
				return r.URL.Path
			}
			return r.URL.Path + "#" + doc.Variables.IID
		}}
		sent, held := scopeRun(t, wire, "gitlab:gitlab.com", "https://gitlab.com",
			bodies("RUNNING", "PENDING"), bodies("FAILED", "SUCCESS"))
		if got := sortedBodies(sent); !slices.Equal(got, want) {
			t.Errorf("GitLab notices = %q and verdicts %v, want %q: each tracked row's read carries its head pipeline (the forge saw %q)",
				got, held, want, wire.requests())
		}
	})
	t.Run("GiteaNotifiesOnBothEdges", func(t *testing.T) {
		row := func(n int) string {
			return fmt.Sprintf(`{"id":%d,"number":%d,"state":"open","title":"PR %d","user":{"login":"alice"},`+
				`"html_url":"https://gitea.example/alice/app/pulls/%d","created_at":"2026-01-01T00:00:00Z",`+
				`"updated_at":"2026-01-02T00:00:00Z","labels":[],"pull_request":{"draft":false,"merged":false},`+
				`"repository":{"id":1,"name":"app","owner":"alice","full_name":"alice/app"}}`, n, n, n, n)
		}
		pull := func(n int) string {
			repo := `{"id":1,"name":"app","full_name":"alice/app","owner":{"login":"alice"}}`
			return fmt.Sprintf(`{"id":%d,"number":%d,"state":"open","title":"PR %d","user":{"login":"alice"},`+
				`"html_url":"https://gitea.example/alice/app/pulls/%d","created_at":"2026-01-01T00:00:00Z",`+
				`"updated_at":"2026-01-02T00:00:00Z","labels":[],"draft":false,"merged":false,"mergeable":true,`+
				`"head":{"ref":"feat%d","sha":"head%d","repo":%s},"base":{"ref":"main","repo":%s}}`, n, n, n, n, n, n, repo, repo)
		}
		status := func(n int, state string) string {
			return fmt.Sprintf(`{"sha":"head%d","state":"%s","total_count":1,`+
				`"statuses":[{"context":"ci","state":"%s","status":"%s"}]}`, n, state, state, state)
		}
		bodies := func(four, five string) map[string]string {
			return map[string]string{
				"/api/v1/repos/issues/search":                  "[" + row(4) + "," + row(5) + "]",
				"/api/v1/repos/alice/app/pulls/4":              pull(4),
				"/api/v1/repos/alice/app/pulls/5":              pull(5),
				"/api/v1/repos/alice/app/commits/head4/status": status(4, four),
				"/api/v1/repos/alice/app/commits/head5/status": status(5, five),
			}
		}
		wire := &pathWire{bodies: map[string]string{
			"/api/v1/user":         `{"id":1,"login":"alice"}`,
			"/api/v1/settings/api": `{"max_response_items":50}`,
		}}
		sent, held := scopeRun(t, wire, "gitea:gitea.example", "https://gitea.example",
			bodies("pending", "pending"), bodies("failure", "success"))
		if got := sortedBodies(sent); !slices.Equal(got, want) {
			t.Errorf("Gitea notices = %q and verdicts %v, want %q: each tracked row's read folds its head's statuses (the forge saw %q)",
				got, held, want, wire.requests())
		}
	})
}
