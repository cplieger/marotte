package forges

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/trigger"
)

// feedWire answers a GitHub instance by request path and records each request's path and query.
type feedWire struct {
	bodies map[string]string
	seen   []string
	mu     sync.Mutex
}

func (w *feedWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.seen = append(w.seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	body, ok := w.bodies[r.URL.Path]
	w.mu.Unlock()
	status := http.StatusOK
	if !ok {
		status, body = http.StatusNotFound, `{"message":"Not Found"}`
	}
	return &http.Response{
		StatusCode: status, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (w *feedWire) requests(prefix string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, s := range w.seen {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

const feedIssuesBody = `[` +
	`{"number":12,"title":"t","state":"open","user":{"login":"alice","type":"User"},` +
	`"labels":[{"name":"report:fast","color":"ff0000"}],"html_url":"https://github.com/cplieger/animap/issues/12",` +
	`"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-02T10:00:00Z"},` +
	`{"number":13,"title":"p","state":"open","user":{"login":"bob","type":"User"},"labels":[],` +
	`"pull_request":{"url":"https://api.github.com/repos/cplieger/animap/pulls/13"},` +
	`"html_url":"https://github.com/cplieger/animap/pull/13",` +
	`"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-02T10:00:00Z"}]`

const feedPRsBody = `{"data":{"rateLimit":{"cost":1,"limit":5000,"remaining":4990,"resetAt":"2026-11-06T00:00:00Z"},` +
	`"repository":{"nameWithOwner":"cplieger/animap","viewerPermission":"WRITE","pullRequests":{"totalCount":1,` +
	`"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{"__typename":"PullRequest","number":31,"title":"x",` +
	`"url":"https://github.com/cplieger/animap/pull/31","state":"OPEN","isDraft":false,"author":{"login":"alice"},` +
	`"headRefName":"feat","baseRefName":"main","repository":{"nameWithOwner":"cplieger/animap"},` +
	`"labels":{"totalCount":1,"nodes":[{"name":"report:fast","color":"ff0000","description":""}]},` +
	`"commits":{"nodes":[{"commit":{"oid":"abc1234","statusCheckRollup":null}}]}}]}}}}`

const feedRunsBody = `{"total_count":1,"workflow_runs":[{"id":41414016210,"name":"Weekly","head_branch":"main",` +
	`"head_sha":"acb5820ced9479c074f688cc328bf03f341a511d","path":".github/workflows/weekly.yaml",` +
	`"run_number":562,"event":"schedule","status":"completed","conclusion":"success",` +
	`"html_url":"https://github.com/cplieger/animap/actions/runs/41414016210",` +
	`"created_at":"2026-11-05T09:00:00Z","updated_at":"2026-11-05T09:30:00Z"}]}`

func TestFeedSourceReadsIssuesPRsRuns(t *testing.T) {
	wire := &feedWire{bodies: map[string]string{
		"/user":                               `{"login":"alice"}`,
		"/user/repos":                         `[]`,
		"/repos/cplieger/animap/issues":       feedIssuesBody,
		"/graphql":                            feedPRsBody,
		"/repos/cplieger/animap/actions/runs": feedRunsBody,
	}}
	h := newConnectHarness(t, wire)
	id := h.signedIn(t, KindGitHub, "", "feed-secret", "")
	src := NewFeedSource(h.m)
	feed := trigger.Feed{Connection: id, Selector: "cplieger/animap"}

	t.Run("issues", func(t *testing.T) {
		page, err := src.OpenIssues(t.Context(), feed, "")
		if err != nil {
			t.Fatalf("OpenIssues = %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Ref.Number != 12 || page.Items[0].Author != "alice" ||
			len(page.Items[0].Labels) != 1 || page.Items[0].Labels[0].Name != "report:fast" {
			t.Errorf("issues = %+v, want #12 by alice labelled report:fast, the pull request row dropped", page.Items)
		}
		reqs := wire.requests("GET /repos/cplieger/animap/issues")
		if len(reqs) != 1 || !strings.Contains(reqs[0], "state=open") {
			t.Errorf("issue requests = %q, want one asking state=open", reqs)
		}
	})
	t.Run("pull requests", func(t *testing.T) {
		page, err := src.OpenPRs(t.Context(), feed, "")
		if err != nil {
			t.Fatalf("OpenPRs = %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Ref.Number != 31 || page.Items[0].TargetBranch != "main" ||
			len(page.Items[0].Labels) != 1 || page.Items[0].Labels[0].Name != "report:fast" {
			t.Errorf("pull requests = %+v, want #31 into main labelled report:fast", page.Items)
		}
	})
	t.Run("runs", func(t *testing.T) {
		since := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
		page, err := src.Runs(t.Context(), feed, since, "")
		if err != nil {
			t.Fatalf("Runs = %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("runs = %+v, want one", page.Items)
		}
		run := page.Items[0]
		if run.Ref.ID != 41414016210 || run.WorkflowPath != ".github/workflows/weekly.yaml" || run.Branch != "main" ||
			run.State != forgeapi.CheckPassing || run.Trigger != forgeapi.TriggerSchedule {
			t.Errorf("run = %+v, want 41414016210 of weekly.yaml on main, passing, by schedule", run)
		}
		reqs := wire.requests("GET /repos/cplieger/animap/actions/runs")
		if len(reqs) != 1 {
			t.Fatalf("run requests = %q, want one", reqs)
		}
		q, err := url.ParseQuery(reqs[0][strings.IndexByte(reqs[0], '?')+1:])
		if err != nil {
			t.Fatal(err)
		}
		bound, err := time.Parse(time.RFC3339, strings.TrimPrefix(q.Get("created"), ">="))
		if err != nil || !bound.Equal(since) {
			t.Errorf("run request created=%q, want >= %s", q.Get("created"), since)
		}
	})
	t.Run("a signed-out connection sends nothing", func(t *testing.T) {
		before := len(wire.requests(""))
		gone := trigger.Feed{Connection: MakeID(KindGitHub, "ghe.example"), Selector: "cplieger/animap"}
		if _, err := src.OpenIssues(t.Context(), gone, ""); !errors.Is(err, errNotLoggedIn) {
			t.Errorf("OpenIssues on an unknown connection = %v, want errNotLoggedIn", err)
		}
		if got := wire.requests(""); len(got) != before {
			t.Errorf("the unknown connection sent %q", got[before:])
		}
	})
}

type probeKey struct{}

// TestAttemptObserverSeesFeedReads pins that a connection signed in after ObserveAttempts reports each attempt of a
// feed read under its id, with the caller's context values, which is how the trigger budget tells its reads apart.
func TestAttemptObserverSeesFeedReads(t *testing.T) {
	wire := &feedWire{bodies: map[string]string{
		"/user":                         `{"login":"alice"}`,
		"/user/repos":                   `[]`,
		"/repos/cplieger/animap/issues": feedIssuesBody,
	}}
	h := newConnectHarness(t, wire)
	var mu sync.Mutex
	var seen []string
	h.m.ObserveAttempts(func(conn string) func(http.RoundTripper) http.RoundTripper {
		return func(next http.RoundTripper) http.RoundTripper {
			return roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if v, _ := r.Context().Value(probeKey{}).(string); v != "" {
					mu.Lock()
					seen = append(seen, conn+" "+v+" "+r.URL.Path)
					mu.Unlock()
				}
				return next.RoundTrip(r)
			})
		}
	})
	id := h.signedIn(t, KindGitHub, "", "feed-secret", "")
	ctx := context.WithValue(t.Context(), probeKey{}, "trigger")
	if _, err := NewFeedSource(h.m).OpenIssues(ctx, trigger.Feed{Connection: id, Selector: "cplieger/animap"}, ""); err != nil {
		t.Fatalf("OpenIssues = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := id + " trigger /repos/cplieger/animap/issues"
	if len(seen) != 1 || seen[0] != want {
		t.Errorf("observed %q, want [%q]", seen, want)
	}
}
