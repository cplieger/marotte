package forges

// Tests for the per-PR, issue and release mutations: each reaches the
// connection's forgeapi client with the decoded repository, answers the row the
// library answered, answers a library failure through the error envelope, and on
// success evicts its repository's lists and asks for an inventory cycle.

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
)

type mutationCall struct {
	newPR      forgeapi.NewPullRequest
	newIssue   forgeapi.NewIssue
	newRelease forgeapi.NewRelease
	op         string
	head       string
	repo       forgeapi.RepoRef
	number     int
}

// mutationCore records every mutation and answers what it holds, and counts the
// pull-request lists and the affordance reads it answers, empty. The embedded
// Core is nil, so any other method panics.
type mutationCore struct {
	forgeapi.Core
	err      error
	pr       forgeapi.PullRequest
	issue    forgeapi.Issue
	calls    []mutationCall
	afforded []string
	lists    int
	mu       sync.Mutex
}

func (c *mutationCore) record(call mutationCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *mutationCore) recorded() []mutationCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func (c *mutationCore) listed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lists
}

func (c *mutationCore) ListPRs(context.Context, forgeapi.RepoRef, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	return forgeapi.Page[forgeapi.PullRequest]{}, nil
}

func (c *mutationCore) RepoAffordances(_ context.Context, repo forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.afforded = append(c.afforded, repo.ID)
	return forgeapi.RepoAffordances{}, nil
}

func (c *mutationCore) affordanceReads(repoID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, id := range c.afforded {
		if id == repoID {
			n++
		}
	}
	return n
}

func (c *mutationCore) MergePR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, _ forgeapi.MergeRequest) (forgeapi.MergeOutcome, error) {
	c.record(mutationCall{op: "MergePR", repo: repo, number: pr.Number})
	return forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeMerged}, c.err
}

func (c *mutationCore) CreatePR(_ context.Context, repo forgeapi.RepoRef, req forgeapi.NewPullRequest) (forgeapi.PullRequest, error) {
	c.record(mutationCall{op: "CreatePR", repo: repo, newPR: req})
	return c.pr, c.err
}

func (c *mutationCore) ClosePR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	c.record(mutationCall{op: "ClosePR", repo: repo, number: pr.Number})
	return c.pr, c.err
}

func (c *mutationCore) ReopenPR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	c.record(mutationCall{op: "ReopenPR", repo: repo, number: pr.Number})
	return c.pr, c.err
}

func (c *mutationCore) RerunFailedChecks(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, headSHA string) error {
	c.record(mutationCall{op: "RerunFailedChecks", repo: repo, number: pr.Number, head: headSHA})
	return c.err
}

func (c *mutationCore) CreateIssue(_ context.Context, repo forgeapi.RepoRef, req forgeapi.NewIssue) (forgeapi.Issue, error) {
	c.record(mutationCall{op: "CreateIssue", repo: repo, newIssue: req})
	return c.issue, c.err
}

func (c *mutationCore) CloseIssue(_ context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error) {
	c.record(mutationCall{op: "CloseIssue", repo: repo, number: issue.Number})
	return c.issue, c.err
}

type releasingMutationCore struct {
	*mutationCore
	release forgeapi.Release
}

func (releasingMutationCore) ListReleases(context.Context, forgeapi.RepoRef, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Release], error) {
	return forgeapi.Page[forgeapi.Release]{}, nil
}

func (c releasingMutationCore) CreateRelease(_ context.Context, repo forgeapi.RepoRef, req forgeapi.NewRelease) (forgeapi.Release, error) {
	c.record(mutationCall{op: "CreateRelease", repo: repo, newRelease: req})
	return c.release, c.err
}

func (row mergeRow) repoPath(tail string) string {
	return "/api/forges/" + url.PathEscape(row.rec.ID) + "/repos/" + row.repoID + "/" + tail
}

func (row mergeRow) decoded() forgeapi.RepoRef {
	return forgeapi.RepoRef{ID: row.repoID, Family: row.family, Selector: row.sel, DisplayPath: row.sel}
}

func answeredPR(row mergeRow, n int, state forgeapi.PRState) forgeapi.PullRequest {
	return forgeapi.PullRequest{
		Ref:   forgeapi.PRRef{Number: n},
		Repo:  forgeapi.RepoRef{Family: row.family, Selector: row.sel, DisplayPath: row.sel},
		Title: "Seed", SourceBranch: "feat/x", TargetBranch: "main", HeadSHA: mergeHead, State: state,
	}
}

func TestCloseReopenRoutes_OnTheClient(t *testing.T) {
	ops := []struct {
		op, verb string
		state    forgeapi.PRState
		want     string
	}{
		{op: "ClosePR", verb: "close", state: forgeapi.PRStateClosed, want: "closed"},
		{op: "ReopenPR", verb: "reopen", state: forgeapi.PRStateOpen, want: "open"},
	}
	for _, row := range []mergeRow{githubMergeRow(), gitlabMergeRow()} {
		for _, o := range ops {
			t.Run(row.rec.ID+"_"+o.verb, func(t *testing.T) {
				core := &mutationCore{pr: answeredPR(row, 3, o.state)}
				mux := mergeMux(t, row, core)

				rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs/3/"+o.verb), "")
				if rec.Code != http.StatusOK {
					t.Fatalf("POST prs/3/%s = %d %s, want 200", o.verb, rec.Code, rec.Body)
				}
				body, _ := decodeBody(t, rec)["pr"].(map[string]any)
				if body["number"] != float64(3) || body["state"] != o.want || body["repo_id"] != row.repoID || body["head_sha"] != mergeHead {
					t.Errorf("POST prs/3/%s answered %v, want the library's row: #3 %s in %s at %s",
						o.verb, body, o.want, row.repoID, mergeHead)
				}
				if action, ok := body["action"].(map[string]any); !ok || action["mergeable"] != "unknown" {
					t.Errorf("POST prs/3/%s action = %v, want the row's action state with mergeable unknown", o.verb, body["action"])
				}
				want := []mutationCall{{op: o.op, repo: row.decoded(), number: 3}}
				if got := core.recorded(); !reflect.DeepEqual(got, want) {
					t.Errorf("the library received %+v, want %+v", got, want)
				}
			})
		}
	}

	t.Run("refusals reach nothing", func(t *testing.T) {
		row := githubMergeRow()
		core := &mutationCore{}
		mux := mergeMux(t, row, core)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rec := sendRoute(t, mux, method, row.repoPath("prs/3/close"), "")
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
				t.Errorf("%s prs/3/close = %d Allow %q, want 405 Allow POST", method, rec.Code, rec.Header().Get("Allow"))
			}
		}
		if rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs/3/detonate"), ""); rec.Code != http.StatusNotFound {
			t.Errorf("POST prs/3/detonate = %d %s, want 404", rec.Code, rec.Body)
		}
		if rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs/three/close"), ""); rec.Code != http.StatusBadRequest {
			t.Errorf("POST prs/three/close = %d %s, want 400", rec.Code, rec.Body)
		}
		if got := core.recorded(); len(got) != 0 {
			t.Errorf("the library received %+v for refused requests, want nothing", got)
		}
	})
}

func TestRerunRoute_PinTravels(t *testing.T) {
	row := githubMergeRow()
	cases := []struct {
		name, query, head string
	}{
		{name: "the head the row displayed", query: "?head_sha=" + mergeHead, head: mergeHead},
		{name: "no pin re-runs the live head", query: "", head: ""},
		{name: "a pin is the library's to check", query: "?head_sha=not-a-sha", head: "not-a-sha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := &mutationCore{}
			mux := mergeMux(t, row, core)
			rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs/5/rerun"+tc.query), "")
			if rec.Code != http.StatusOK || decodeBody(t, rec)["ok"] != true {
				t.Fatalf("POST prs/5/rerun%s = %d %s, want 200 {ok: true}", tc.query, rec.Code, rec.Body)
			}
			want := []mutationCall{{op: "RerunFailedChecks", repo: row.decoded(), number: 5, head: tc.head}}
			if got := core.recorded(); !reflect.DeepEqual(got, want) {
				t.Errorf("the library received %+v, want %+v", got, want)
			}
		})
	}

	t.Run("a malformed pin is the library's refusal", func(t *testing.T) {
		wire := aliceWire()
		h := patGitHub(t, wire)
		before := len(wire.requests())
		rec := h.do(t, http.MethodPost, githubRepoPath("v1.6f2f72", "prs/5/rerun?head_sha=abc1234%3Brm"), "")
		body := decodeBody(t, rec)
		if rec.Code != http.StatusBadRequest || body["code"] != forgeapi.CodeRefInvalid || body["kind"] != "unknown" {
			t.Errorf("POST prs/5/rerun?head_sha=abc1234;rm = %d %s, want 400 %s kind unknown", rec.Code, rec.Body, forgeapi.CodeRefInvalid)
		}
		if after := len(wire.requests()); after != before {
			t.Errorf("the forge saw %d requests for a refused pin, want none", after-before)
		}
	})
}

func TestCreatePRRoute_AnswersTheNewRow(t *testing.T) {
	const create = `{"title":"Add x","body":"Why.","source_branch":"feat/x","target_branch":"main","labels":["bug"],"draft":true}`
	for _, row := range []mergeRow{githubMergeRow(), gitlabMergeRow()} {
		t.Run(row.rec.ID, func(t *testing.T) {
			created := answeredPR(row, 15, forgeapi.PRStateOpen)
			created.Title, created.Body, created.Draft = "Add x", "Why.", true
			created.Partial = &forgeapi.Partial{Reason: forgeapi.PartialLabelsNotApplied}
			core := &mutationCore{pr: created}
			mux := mergeMux(t, row, core)

			rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs"), create)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST prs = %d %s, want 200", rec.Code, rec.Body)
			}
			body := decodeBody(t, rec)
			if body["number"] != float64(15) || body["repo_id"] != row.repoID || body["title"] != "Add x" ||
				body["source_branch"] != "feat/x" || body["draft"] != true || body["state"] != "open" {
				t.Errorf("POST prs answered %v, want the new row #15 in %s", body, row.repoID)
			}
			if partial, ok := body["partial"].(map[string]any); !ok || partial["reason"] != "labels_not_applied" {
				t.Errorf("POST prs partial = %v, want reason labels_not_applied", body["partial"])
			}
			if action, ok := body["action"].(map[string]any); !ok || action["checks"] != "unknown" {
				t.Errorf("POST prs action = %v, want the row's action state", body["action"])
			}
			want := []mutationCall{{op: "CreatePR", repo: row.decoded(), newPR: forgeapi.NewPullRequest{
				Title: "Add x", Body: "Why.", SourceBranch: "feat/x", TargetBranch: "main", Labels: []string{"bug"}, Draft: true,
			}}}
			if got := core.recorded(); !reflect.DeepEqual(got, want) {
				t.Errorf("the library received %+v, want %+v", got, want)
			}
		})
	}

	t.Run("a body that is not JSON creates nothing", func(t *testing.T) {
		row := githubMergeRow()
		core := &mutationCore{}
		mux := mergeMux(t, row, core)
		if rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs"), `{"title":`); rec.Code != http.StatusBadRequest {
			t.Errorf("POST prs with a truncated body = %d %s, want 400", rec.Code, rec.Body)
		}
		if got := core.recorded(); len(got) != 0 {
			t.Errorf("the library received %+v, want nothing", got)
		}
	})
}

func TestIssueMutations_OnTheClient(t *testing.T) {
	row := gitlabMergeRow()
	issue := forgeapi.Issue{
		Ref: forgeapi.IssueRef{Number: 16}, Title: "Broken", State: forgeapi.IssueStateOpen,
		Labels: []forgeapi.Label{{Name: "bug"}},
	}

	t.Run("create answers the new issue", func(t *testing.T) {
		core := &mutationCore{issue: issue}
		mux := mergeMux(t, row, core)
		rec := sendRoute(t, mux, http.MethodPost, row.repoPath("issues"), `{"title":"Broken","body":"It is.","labels":["bug"]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST issues = %d %s, want 200", rec.Code, rec.Body)
		}
		body := decodeBody(t, rec)
		if body["number"] != float64(16) || body["title"] != "Broken" || body["state"] != "open" ||
			!reflect.DeepEqual(body["labels"], []any{"bug"}) {
			t.Errorf("POST issues answered %v, want #16 open labelled bug", body)
		}
		want := []mutationCall{{op: "CreateIssue", repo: row.decoded(), newIssue: forgeapi.NewIssue{
			Title: "Broken", Body: "It is.", Labels: []string{"bug"},
		}}}
		if got := core.recorded(); !reflect.DeepEqual(got, want) {
			t.Errorf("the library received %+v, want %+v", got, want)
		}
	})

	t.Run("close answers the closed issue", func(t *testing.T) {
		closed := issue
		closed.State = forgeapi.IssueStateClosed
		core := &mutationCore{issue: closed}
		mux := mergeMux(t, row, core)
		rec := sendRoute(t, mux, http.MethodPost, row.repoPath("issues/16/close"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("POST issues/16/close = %d %s, want 200", rec.Code, rec.Body)
		}
		if body := decodeBody(t, rec); body["number"] != float64(16) || body["state"] != "closed" {
			t.Errorf("POST issues/16/close answered %v, want #16 closed", body)
		}
		want := []mutationCall{{op: "CloseIssue", repo: row.decoded(), number: 16}}
		if got := core.recorded(); !reflect.DeepEqual(got, want) {
			t.Errorf("the library received %+v, want %+v", got, want)
		}
	})

	t.Run("refusals reach nothing", func(t *testing.T) {
		core := &mutationCore{}
		mux := mergeMux(t, row, core)
		rec := sendRoute(t, mux, http.MethodGet, row.repoPath("issues/16/close"), "")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("GET issues/16/close = %d Allow %q, want 405 Allow POST", rec.Code, rec.Header().Get("Allow"))
		}
		for path, want := range map[string]int{
			"issues/16/reopen": http.StatusNotFound,
			"issues/x/close":   http.StatusBadRequest,
		} {
			if rec := sendRoute(t, mux, http.MethodPost, row.repoPath(path), ""); rec.Code != want {
				t.Errorf("POST %s = %d %s, want %d", path, rec.Code, rec.Body, want)
			}
		}
		if rec := sendRoute(t, mux, http.MethodPost, row.repoPath("issues"), `["title"]`); rec.Code != http.StatusBadRequest {
			t.Errorf("POST issues with a list body = %d %s, want 400", rec.Code, rec.Body)
		}
		if got := core.recorded(); len(got) != 0 {
			t.Errorf("the library received %+v for refused requests, want nothing", got)
		}
	})
}

func TestCreateReleaseRoute_AbsentRoleIs501(t *testing.T) {
	row := giteaMergeRow()
	const cut = `{"tag_name":"v1.0.0","name":"One","body":"Notes.","target":"main","draft":true,"prerelease":true}`

	t.Run("a client without the role", func(t *testing.T) {
		core := &mutationCore{}
		mux := mergeMux(t, row, core)
		rec := sendRoute(t, mux, http.MethodPost, row.repoPath("releases"), cut)
		if rec.Code != http.StatusNotImplemented || decodeBody(t, rec)["code"] != "not_supported" {
			t.Errorf("POST releases = %d %s, want 501 not_supported", rec.Code, rec.Body)
		}
		if got := core.recorded(); len(got) != 0 {
			t.Errorf("the library received %+v, want nothing", got)
		}
	})

	t.Run("a client serving it answers the cut release", func(t *testing.T) {
		core := releasingMutationCore{
			mutationCore: &mutationCore{},
			release:      forgeapi.Release{TagName: "v1.0.0", Name: "One", WebURL: "https://gitea.example/o/r/releases/v1.0.0", Draft: true},
		}
		mux := mergeMux(t, row, core)
		rec := sendRoute(t, mux, http.MethodPost, row.repoPath("releases"), cut)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST releases = %d %s, want 200", rec.Code, rec.Body)
		}
		if body := decodeBody(t, rec); body["tag_name"] != "v1.0.0" || body["url"] != "https://gitea.example/o/r/releases/v1.0.0" || body["draft"] != true {
			t.Errorf("POST releases answered %v, want the cut release v1.0.0", body)
		}
		want := []mutationCall{{op: "CreateRelease", repo: row.decoded(), newRelease: forgeapi.NewRelease{
			TagName: "v1.0.0", Name: "One", Body: "Notes.", Target: "main", Draft: true, Prerelease: true,
		}}}
		if got := core.recorded(); !reflect.DeepEqual(got, want) {
			t.Errorf("the library received %+v, want %+v", got, want)
		}
		if rec := sendRoute(t, mux, http.MethodPost, row.repoPath("releases"), `{"tag_name":`); rec.Code != http.StatusBadRequest {
			t.Errorf("POST releases with a truncated body = %d %s, want 400", rec.Code, rec.Body)
		}
		if got := core.recorded(); len(got) != 1 {
			t.Errorf("the library received %d cuts after a truncated body, want the one before it", len(got))
		}
	})
}

var everyMutation = []struct{ op, tail, body string }{
	{op: "ClosePR", tail: "prs/3/close"},
	{op: "ReopenPR", tail: "prs/3/reopen"},
	{op: "RerunFailedChecks", tail: "prs/3/rerun?head_sha=" + mergeHead},
	{op: "CreatePR", tail: "prs", body: `{"title":"t","source_branch":"feat/x","target_branch":"main"}`},
	{op: "CreateIssue", tail: "issues", body: `{"title":"t"}`},
	{op: "CloseIssue", tail: "issues/16/close"},
	{op: "CreateRelease", tail: "releases", body: `{"tag_name":"v1"}`},
}

func failEveryMutation(t *testing.T, err error) map[string]map[string]any {
	t.Helper()
	row := githubMergeRow()
	core := releasingMutationCore{mutationCore: &mutationCore{err: err}}
	mux := mergeMux(t, row, core)
	bodies := make(map[string]map[string]any, len(everyMutation))
	for _, m := range everyMutation {
		rec := sendRoute(t, mux, http.MethodPost, row.repoPath(m.tail), m.body)
		body := decodeBody(t, rec)
		body["status"] = float64(rec.Code)
		body["retry_after_header"] = rec.Header().Get("Retry-After")
		bodies[m.op] = body
		for _, key := range []string{"number", "tag_name", "ok"} {
			if v, ok := body[key]; ok {
				t.Errorf("%s failed and still answered %s %v, want the envelope alone", m.op, key, v)
			}
		}
	}
	ops := make([]string, 0, len(everyMutation))
	for _, c := range core.recorded() {
		ops = append(ops, c.op)
	}
	if want := []string{"ClosePR", "ReopenPR", "RerunFailedChecks", "CreatePR", "CreateIssue", "CloseIssue", "CreateRelease"}; !slices.Equal(ops, want) {
		t.Fatalf("the library received %v, want every mutation once: %v", ops, want)
	}
	return bodies
}

func TestErrorRoute_RateLimitedSetsRetryAfterAndBody(t *testing.T) {
	bodies := failEveryMutation(t, &forgeapi.Error{
		Op: "mutation", Kind: forgeapi.KindRateLimited, Retryable: true, RetryAfter: 90 * time.Second,
		DiagID: "d1e2f3a4b5c6d", Message: "secondary rate limit",
	})
	for op, body := range bodies {
		if body["status"] != float64(http.StatusTooManyRequests) || body["kind"] != "rate_limited" ||
			body["retry_after_s"] != float64(90) || body["diag_id"] != "d1e2f3a4b5c6d" {
			t.Errorf("%s answered %v, want 429 rate_limited retry_after_s 90 with its diag_id", op, body)
		}
		if body["retry_after_header"] != "90" {
			t.Errorf("%s Retry-After = %q, want 90", op, body["retry_after_header"])
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, "secondary rate limit") {
			t.Errorf("%s error = %q, want the library's message", op, msg)
		}
	}
}

func TestErrorRoute_ReconnectRequiredIs401(t *testing.T) {
	bodies := failEveryMutation(t, &forgeapi.Error{
		Op: "Token", Code: forgeapi.CodeReconnectRequired, Kind: forgeapi.KindUnauthorized,
		Message: "the credential needs a new sign-in",
	})
	for op, body := range bodies {
		if body["status"] != float64(http.StatusUnauthorized) || body["code"] != forgeapi.CodeReconnectRequired || body["kind"] != "unauthorized" {
			t.Errorf("%s answered %v, want 401 %s kind unauthorized", op, body, forgeapi.CodeReconnectRequired)
		}
	}
}

func TestErrorRoute_RepoRefStaleCarriesTheSuccessor(t *testing.T) {
	bodies := failEveryMutation(t, &forgeapi.Error{
		Op: "mutation", Code: forgeapi.CodeRepoRefStale, Kind: forgeapi.KindNotFound, Message: "moved",
		Successor: &forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/renamed", DisplayPath: "o/renamed"},
	})
	want := map[string]any{"repo_id": "v1.6f2f72656e616d6564", "display_path": "o/renamed"}
	for op, body := range bodies {
		if body["status"] != float64(http.StatusNotFound) || body["code"] != forgeapi.CodeRepoRefStale || !reflect.DeepEqual(body["successor"], want) {
			t.Errorf("%s answered %v, want 404 %s with successor %v", op, body, forgeapi.CodeRepoRefStale, want)
		}
	}
}

func TestErrorRoute_CapabilityUnsupportedIs501WithItsEvidence(t *testing.T) {
	bodies := failEveryMutation(t, &forgeapi.Error{
		Op: "RerunFailedChecks", Code: forgeapi.CodeCapabilityUnsupported, Kind: forgeapi.KindForbidden,
		Message: "this instance cannot re-run checks", Capability: forgeapi.CapRerunChecks,
		Evidence: forgeapi.Evidence{Source: forgeapi.EvidenceSwagger, Detail: "the swagger document names no re-run route"},
	})
	want := map[string]any{
		"name": "rerun_checks", "support": "no", "source": "swagger", "detail": "the swagger document names no re-run route",
	}
	for op, body := range bodies {
		if body["status"] != float64(http.StatusNotImplemented) || body["code"] != forgeapi.CodeCapabilityUnsupported ||
			body["kind"] != "forbidden" || !reflect.DeepEqual(body["capability"], want) {
			t.Errorf("%s answered %v, want 501 %s kind forbidden with capability %v", op, body, forgeapi.CodeCapabilityUnsupported, want)
		}
	}
}

// movingMutations is every mutation that changes a forge object, on the GitHub
// row: everyMutation but the re-run, and the merge.
var movingMutations = []struct{ op, tail, body string }{
	{op: "ClosePR", tail: "prs/3/close"},
	{op: "ReopenPR", tail: "prs/3/reopen"},
	{op: "MergePR", tail: "prs/3/merge", body: `{"intent":"default","strategy":"squash","head_sha":"` + mergeHead + `"}`},
	{op: "CreatePR", tail: "prs", body: `{"title":"t","source_branch":"feat/x","target_branch":"main"}`},
	{op: "CreateIssue", tail: "issues", body: `{"title":"t"}`},
	{op: "CloseIssue", tail: "issues/16/close"},
	{op: "CreateRelease", tail: "releases", body: `{"tag_name":"v1"}`},
}

var rerunMutation = struct{ op, tail, body string }{op: "RerunFailedChecks", tail: "prs/3/rerun?head_sha=" + mergeHead}

// cycleMux serves row's routes over core with a poller whose gate is closed, so
// a sweep reads the forge only when something asked for a cycle.
func cycleMux(t *testing.T, row mergeRow, core forgeapi.Core) (*http.ServeMux, *PRStatusPoller, *heldSource) {
	t.Helper()
	isolateGit(t)
	rec := row.rec
	src := newHeldSource(false)
	p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{}).Open)
	return inventoryRoutes(recordManagerFor(t, &rec, core), p), p, src
}

func TestMutation_EvictsItsRepositorysLists(t *testing.T) {
	row := githubMergeRow()
	prs := row.repoPath("prs")
	for _, m := range movingMutations {
		t.Run(m.op, func(t *testing.T) {
			core := &mutationCore{}
			mux := mergeMux(t, row, releasingMutationCore{mutationCore: core})
			if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK {
				t.Fatalf("Setup: GET %s = %d %s", prs, rec.Code, rec.Body)
			}
			if rec := sendRoute(t, mux, http.MethodPost, row.repoPath(m.tail), m.body); rec.Code != http.StatusOK {
				t.Fatalf("POST %s = %d %s, want 200", m.tail, rec.Code, rec.Body)
			}
			if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK || core.listed() != 2 {
				t.Errorf("GET %s after %s = %d after %d lists, want 200 after 2: the mutation evicts its repository's list",
					prs, m.op, rec.Code, core.listed())
			}
		})
	}
}

func TestMutation_AsksForAnInventoryCycle(t *testing.T) {
	row := githubMergeRow()
	for _, m := range append(slices.Clone(movingMutations), rerunMutation) {
		t.Run(m.op, func(t *testing.T) {
			mux, p, src := cycleMux(t, row, releasingMutationCore{mutationCore: &mutationCore{}})
			p.sweep(t.Context())
			if got := src.reads(); len(got) != 0 {
				t.Fatalf("Setup: a closed-gate sweep with nothing asked read %v, want nothing", got)
			}
			if rec := sendRoute(t, mux, http.MethodPost, row.repoPath(m.tail), m.body); rec.Code != http.StatusOK {
				t.Fatalf("POST %s = %d %s, want 200", m.tail, rec.Code, rec.Body)
			}
			p.sweep(t.Context())
			if got := src.reads(); !slices.Equal(got, []bool{true}) {
				t.Errorf("the closed-gate sweep after %s read %v, want one present read: the mutation asks for a cycle", m.op, got)
			}
		})
	}
}

// The client keeps a closed or merged row hidden until an entry from the cycle
// a mutation names, so the answer must name the cycle that begins after it.
func TestMutation_AnswersTheCycleThatReadsIt(t *testing.T) {
	row := githubMergeRow()
	for _, m := range movingMutations[:3] {
		t.Run(m.op, func(t *testing.T) {
			mux, p, _ := cycleMux(t, row, releasingMutationCore{mutationCore: &mutationCore{pr: answeredPR(row, 3, forgeapi.PRStateClosed)}})
			rec := sendRoute(t, mux, http.MethodPost, row.repoPath(m.tail), m.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST %s = %d %s, want 200", m.tail, rec.Code, rec.Body)
			}
			p.sweep(t.Context())
			if got := decodeBody(t, rec)["cycle_id"]; got != "1" || cycleOf(p, testConn.ID) != "1" {
				t.Errorf("POST %s answered cycle_id %v and the next cycle was %q, want both 1", m.tail, got, cycleOf(p, testConn.ID))
			}
		})
	}
}

func TestMutation_DuringACycleAsksForTheNext(t *testing.T) {
	isolateGit(t)
	row := githubMergeRow()
	rec := row.rec
	m := recordManagerFor(t, &rec, &mutationCore{pr: answeredPR(row, 3, forgeapi.PRStateClosed)})
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(true)
		p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{}).Open)
		mux := inventoryRoutes(m, p)
		stop := runLoop(t, p)
		defer stop()
		if id := pressRefresh(t, mux); id != "1" {
			t.Fatalf("Setup: the press answered cycle %q, want 1", id)
		}
		synctest.Wait()

		rec := sendRoute(t, mux, http.MethodPost, row.repoPath("prs/3/close"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("POST prs/3/close = %d %s, want 200", rec.Code, rec.Body)
		}
		if got := decodeBody(t, rec)["cycle_id"]; got != "2" {
			t.Errorf("the close during cycle 1 answered cycle_id %v, want 2: cycle 1 may have read the row before it", got)
		}
		close(src.release)
		synctest.Wait()

		if got := src.reads(); !slices.Equal(got, []bool{true, true}) || cycleOf(p, testConn.ID) != "2" {
			t.Errorf("reads %v, entry from cycle %q; want two present reads and the entry from cycle 2: "+
				"cycle 1 may have read the repository before the close", got, cycleOf(p, testConn.ID))
		}
	})
}

func TestMutation_AFailedMutationEvictsAndAsksNothing(t *testing.T) {
	row := githubMergeRow()
	prs := row.repoPath("prs")
	for _, m := range append(slices.Clone(movingMutations), rerunMutation) {
		t.Run(m.op, func(t *testing.T) {
			core := &mutationCore{err: &forgeapi.Error{Op: m.op, Kind: forgeapi.KindConflict, Message: "refused"}}
			mux, p, src := cycleMux(t, row, releasingMutationCore{mutationCore: core})
			if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK {
				t.Fatalf("Setup: GET %s = %d %s", prs, rec.Code, rec.Body)
			}
			if rec := sendRoute(t, mux, http.MethodPost, row.repoPath(m.tail), m.body); rec.Code != http.StatusConflict {
				t.Fatalf("POST %s = %d %s, want the library's refusal as 409", m.tail, rec.Code, rec.Body)
			}
			if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK || core.listed() != 1 {
				t.Errorf("GET %s after a refused %s = %d after %d lists, want 200 after 1", prs, m.op, rec.Code, core.listed())
			}
			p.sweep(t.Context())
			if got := src.reads(); len(got) != 0 {
				t.Errorf("the closed-gate sweep after a refused %s read %v, want nothing", m.op, got)
			}
		})
	}
}
