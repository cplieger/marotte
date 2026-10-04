package forges

// Tests for the repository routes: the repo_id segment, the list routes and
// the pull-request detail over the client.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// pagedCore answers each list call with the page its cursor names and records
// what each call asked for. The embedded Core is nil, so any other method panics.
type pagedCore struct {
	forgeapi.Core
	prPages    map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]
	repoPages  map[forgeapi.Cursor]forgeapi.Page[forgeapi.Repository]
	issuePages map[forgeapi.Cursor]forgeapi.Page[forgeapi.Issue]
	readErr    error
	checksErr  error
	affErr     error
	read       forgeapi.PullRequest
	checks     forgeapi.CommitChecks
	aff        forgeapi.RepoAffordances
	asked      []forgeapi.RepoRef
	cursors    []forgeapi.Cursor
	states     []forgeapi.ListState
	refs       []string
	prReads    []forgeapi.PRRef
	mu         sync.Mutex
}

func (c *pagedCore) record(repo forgeapi.RepoRef, opts []forgeapi.ListOption) (forgeapi.ListSettings, error) {
	set, err := forgeapi.ResolveList(opts...)
	if err != nil {
		return set, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, repo)
	c.cursors = append(c.cursors, set.After)
	c.states = append(c.states, set.State)
	return set, nil
}

func (c *pagedCore) ListPRs(_ context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	set, err := c.record(repo, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return c.prPages[set.After], nil
}

func (c *pagedCore) ListRepos(_ context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	set, err := c.record(forgeapi.RepoRef{}, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	return c.repoPages[set.After], nil
}

func (c *pagedCore) ListIssues(_ context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	set, err := c.record(repo, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	return c.issuePages[set.After], nil
}

func (c *pagedCore) CommitStatus(_ context.Context, repo forgeapi.RepoRef, ref string) (forgeapi.CommitChecks, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, repo)
	c.refs = append(c.refs, ref)
	return c.checks, c.checksErr
}

func (c *pagedCore) ReadPR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, repo)
	c.prReads = append(c.prReads, pr)
	return c.read, c.readErr
}

func (c *pagedCore) RepoAffordances(_ context.Context, repo forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, repo)
	return c.aff, c.affErr
}

// answerAffordances changes what RepoAffordances answers from the next read on.
func (c *pagedCore) answerAffordances(aff forgeapi.RepoAffordances, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.aff, c.affErr = aff, err
}

// rolesCore is a pagedCore that also serves the two roles a family may lack.
type rolesCore struct {
	*pagedCore
	releasePages map[forgeapi.Cursor]forgeapi.Page[forgeapi.Release]
	labelPages   map[forgeapi.Cursor]forgeapi.Page[forgeapi.Label]
}

func (c rolesCore) ListReleases(_ context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Release], error) {
	set, err := c.record(repo, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	return c.releasePages[set.After], nil
}

func (rolesCore) CreateRelease(context.Context, forgeapi.RepoRef, forgeapi.NewRelease) (forgeapi.Release, error) {
	return forgeapi.Release{}, nil
}

func (c rolesCore) ListLabels(_ context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Label], error) {
	set, err := c.record(repo, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	return c.labelPages[set.After], nil
}

func (c *pagedCore) calls() (asked []forgeapi.RepoRef, cursors []forgeapi.Cursor, states []forgeapi.ListState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.asked), slices.Clone(c.cursors), slices.Clone(c.states)
}

func (c *pagedCore) commitRefs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.refs)
}

func (c *pagedCore) reads() []forgeapi.PRRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.prReads)
}

// onePRPage is a first page holding pull request n of o/r.
func onePRPage(n int) map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest] {
	return map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {Items: []forgeapi.PullRequest{{
		Ref:   forgeapi.PRRef{Number: n},
		Repo:  forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"},
		State: forgeapi.PRStateOpen, Title: "row",
	}}}}
}

// repoRoutes serves the forge routes over m.
func repoRoutes(m *Manager) *http.ServeMux {
	mux := http.NewServeMux()
	NewHTTPHandler(m, nil).RegisterRoutes(mux)
	return mux
}

func getRoute(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

// githubRepoPath is a repository sub-resource route on the GitHub row.
func githubRepoPath(repoID, tail string) string {
	return "/api/forges/github%3Agithub.com/repos/" + repoID + "/" + tail
}

// prNumbers is the numbers of the rows a PR list answer carries.
func prNumbers(t *testing.T, rec *httptest.ResponseRecorder) []int {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("PR list = %d %s, want 200", rec.Code, rec.Body)
	}
	var body struct{ PRs []PR }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("PR list body %q: %v", rec.Body, err)
	}
	out := make([]int, 0, len(body.PRs))
	for i := range body.PRs {
		out = append(out, body.PRs[i].Number)
	}
	return out
}

func TestRepoRoute_DecodesRepoID(t *testing.T) {
	core := &pagedCore{prPages: onePRPage(4)}
	mux := repoRoutes(recordManager(t, core))

	rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs?state=open"))
	if got := prNumbers(t, rec); !slices.Equal(got, []int{4}) {
		t.Errorf("GET prs rows = %v, want [4]", got)
	}
	asked, _, _ := core.calls()
	want := []forgeapi.RepoRef{{ID: "v1.6f2f72", Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"}}
	if !reflect.DeepEqual(asked, want) {
		t.Errorf("the library was asked for %+v, want %+v", asked, want)
	}
	if row := decodeBody(t, rec)["prs"].([]any)[0].(map[string]any); row["repo_id"] != "v1.6f2f72" || row["repo"] != "o/r" {
		t.Errorf("the row carries repo_id %v and repo %v, want v1.6f2f72 and o/r", row["repo_id"], row["repo"])
	}
}

func TestRepoRoute_BadRepoIDIs400(t *testing.T) {
	core := &pagedCore{prPages: onePRPage(1)}
	mux := repoRoutes(recordManager(t, core))

	segments := map[string]string{
		"no version prefix":          "o",
		"not hex":                    "v1.zz",
		"a separator github lacks":   "v1.6f2f722f78",
		"another version prefix":     "v2.6f2f72",
		"an empty selector":          "v1.",
		"a dot-dot segment":          "v1.2e2e2f72",
		"owner and name, not an id":  "o/r",
		"percent-encoded owner/name": "o%2Fr",
		"a trailing non-hex byte":    "v1.6f2f72x",
	}
	for name, seg := range segments {
		for _, tail := range []string{"prs", "issues"} {
			rec := getRoute(t, mux, githubRepoPath(seg, tail))
			if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeRepoRefInvalid {
				t.Errorf("%s: GET %s = %d %s, want 400 %s", name, githubRepoPath(seg, tail), rec.Code, rec.Body, forgeapi.CodeRepoRefInvalid)
			}
		}
	}
	if asked, _, _ := core.calls(); len(asked) != 0 {
		t.Errorf("the library was asked %+v for a refused id, want nothing", asked)
	}
}

func TestRepoRoute_NestedGitLabProjectIsReachable(t *testing.T) {
	core := &pagedCore{prPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {}}}
	rec := connectionRecord{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com", WebBaseURL: "https://gitlab.com"}
	mux := repoRoutes(recordManagerFor(t, &rec, core))
	const nested = "v1.666f7267656170692d6c6976652f6e65737465642f73616e64626f78"

	for _, tail := range []string{"prs?state=open", "issues?state=open"} {
		if got := getRoute(t, mux, "/api/forges/gitlab%3Agitlab.com/repos/"+nested+"/"+tail); got.Code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200", tail, got.Code, got.Body)
		}
	}
	asked, _, _ := core.calls()
	if len(asked) != 2 {
		t.Fatalf("the library was asked %+v, want the list and the issue read", asked)
	}
	for _, ref := range asked {
		if ref.Family != forgeapi.FamilyGitLab || ref.Selector != "forgeapi-live/nested/sandbox" || ref.ID != nested {
			t.Errorf("the library was asked for %+v, want the nested GitLab project under its canonical id", ref)
		}
	}
}

// One repository has one identifier: an id that is not the canonical encoding
// of its selector is refused before the library or the cache (ADR-0100).
func TestRepoRoute_NonCanonicalIDIsRefused(t *testing.T) {
	core := &pagedCore{prPages: onePRPage(4)}
	mux := repoRoutes(recordManager(t, core))

	if rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs")); rec.Code != http.StatusOK {
		t.Fatalf("Setup: GET the canonical id = %d %s", rec.Code, rec.Body)
	}
	for name, seg := range map[string]string{"upper-case hex": "v1.6F2F72", "a mixed-case selector": "v1.4f2f52"} {
		rec := getRoute(t, mux, githubRepoPath(seg, "prs"))
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeRepoRefInvalid {
			t.Errorf("%s: GET = %d %s, want 400 %s", name, rec.Code, rec.Body, forgeapi.CodeRepoRefInvalid)
		}
	}
	if asked, _, _ := core.calls(); len(asked) != 1 {
		t.Errorf("the library was asked %d times for one repository, want 1 (%+v)", len(asked), asked)
	}
}

func TestListEnvelope_CarriesNextPartialSuccessor(t *testing.T) {
	core := &pagedCore{
		prPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{
			"": {
				Items:     onePRPage(1)[""].Items,
				Next:      "c2",
				Partial:   &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 1},
				Successor: &forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/renamed", DisplayPath: "o/renamed"},
			},
			"c2": {},
		},
		repoPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Repository]{
			"": {
				Items:   []forgeapi.Repository{{Ref: forgeapi.RepoRef{Selector: "o/r", DisplayPath: "o/r"}}},
				Next:    "r2",
				Partial: &forgeapi.Partial{Reason: forgeapi.PartialBudget, Fetched: 1},
			},
		},
	}
	mux := repoRoutes(recordManager(t, core))

	prs := decodeBody(t, getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs")))
	wantPartial := map[string]any{"reason": "pagination_cap", "fetched": float64(1), "omitted_at_least": float64(1)}
	wantSuccessor := map[string]any{"repo_id": "v1.6f2f72656e616d6564", "display_path": "o/renamed"}
	if prs["next"] != "c2" || !reflect.DeepEqual(prs["partial"], wantPartial) || !reflect.DeepEqual(prs["successor"], wantSuccessor) {
		t.Errorf("PR list envelope = %v, want next c2, partial %v and successor %v", prs, wantPartial, wantSuccessor)
	}

	last := decodeBody(t, getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs?after=c2")))
	for _, key := range []string{"next", "partial", "successor"} {
		if _, ok := last[key]; ok {
			t.Errorf("a complete page carries %q: %v", key, last)
		}
	}
	if rows, ok := last["prs"].([]any); !ok || len(rows) != 0 {
		t.Errorf("an empty page encodes prs = %v, want []", last["prs"])
	}

	repos := decodeBody(t, getRoute(t, mux, "/api/forges/github%3Agithub.com/repos"))
	wantRepoPartial := map[string]any{"reason": "budget", "fetched": float64(1), "omitted_at_least": float64(0)}
	if repos["next"] != "r2" || !reflect.DeepEqual(repos["partial"], wantRepoPartial) {
		t.Errorf("repository list envelope = %v, want next r2 and partial %v", repos, wantRepoPartial)
	}
	if _, ok := repos["successor"]; ok {
		t.Errorf("the repository list carries a successor: %v", repos)
	}
}

func TestListRoute_StateMapsAndAnUnknownStateIs400(t *testing.T) {
	cases := map[string]forgeapi.ListState{
		"":       forgeapi.ListStateOpen,
		"open":   forgeapi.ListStateOpen,
		"closed": forgeapi.ListStateClosed,
		"merged": forgeapi.ListStateMerged,
		"all":    forgeapi.ListStateAll,
	}
	for query, want := range cases {
		t.Run("state="+query, func(t *testing.T) {
			core := &pagedCore{prPages: onePRPage(1)}
			mux := repoRoutes(recordManager(t, core))
			if rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs?state="+query)); rec.Code != http.StatusOK {
				t.Fatalf("GET = %d %s, want 200", rec.Code, rec.Body)
			}
			if _, _, states := core.calls(); !slices.Equal(states, []forgeapi.ListState{want}) {
				t.Errorf("the library listed states %v, want [%v]", states, want)
			}
		})
	}

	t.Run("an unknown state", func(t *testing.T) {
		core := &pagedCore{prPages: onePRPage(1)}
		mux := repoRoutes(recordManager(t, core))
		rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs?state=draft"))
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeListStateInvalid {
			t.Errorf("GET ?state=draft = %d %s, want 400 %s", rec.Code, rec.Body, forgeapi.CodeListStateInvalid)
		}
		if asked, _, _ := core.calls(); len(asked) != 0 {
			t.Errorf("the library was asked %+v, want nothing", asked)
		}
	})

	t.Run("a state on the repository list", func(t *testing.T) {
		wire := aliceWire()
		h := newConnectHarness(t, wire)
		if rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`); rec.Code != http.StatusOK {
			t.Fatalf("Setup: connect = %d %s", rec.Code, rec.Body)
		}
		before := len(wire.requests())
		rec := h.do(t, http.MethodGet, "/api/forges/github%3Agithub.com/repos?state=open", "")
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeListStateInvalid {
			t.Errorf("GET repos?state=open = %d %s, want 400 %s", rec.Code, rec.Body, forgeapi.CodeListStateInvalid)
		}
		if after := len(wire.requests()); after != before {
			t.Errorf("the forge saw %d requests for a refused list, want none", after-before)
		}
	})
}

// patGitHub is the forge routes over a real GitHub client connected through
// the PAT route, answered by wire.
func patGitHub(t *testing.T, wire *userWire) *connectHarness {
	t.Helper()
	h := newConnectHarness(t, wire)
	if rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`); rec.Code != http.StatusOK {
		t.Fatalf("Setup: connect = %d %s", rec.Code, rec.Body)
	}
	return h
}

func TestIssueList_StateMapsAndAnUnknownStateIs400(t *testing.T) {
	cases := map[string]forgeapi.ListState{
		"":       forgeapi.ListStateOpen,
		"open":   forgeapi.ListStateOpen,
		"closed": forgeapi.ListStateClosed,
		"all":    forgeapi.ListStateAll,
	}
	for query, want := range cases {
		t.Run("state="+query, func(t *testing.T) {
			core := &pagedCore{}
			mux := repoRoutes(recordManager(t, core))
			if rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "issues?state="+query)); rec.Code != http.StatusOK {
				t.Fatalf("GET issues?state=%s = %d %s, want 200", query, rec.Code, rec.Body)
			}
			if _, _, states := core.calls(); !slices.Equal(states, []forgeapi.ListState{want}) {
				t.Errorf("GET issues?state=%s: the library listed states %v, want [%v]", query, states, want)
			}
		})
	}

	t.Run("an unknown state", func(t *testing.T) {
		core := &pagedCore{}
		mux := repoRoutes(recordManager(t, core))
		rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "issues?state=draft"))
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeListStateInvalid {
			t.Errorf("GET issues?state=draft = %d %s, want 400 %s", rec.Code, rec.Body, forgeapi.CodeListStateInvalid)
		}
		if asked, _, _ := core.calls(); len(asked) != 0 {
			t.Errorf("GET issues?state=draft: the library was asked %+v, want nothing", asked)
		}
	})

	t.Run("merged, which no issue can be", func(t *testing.T) {
		wire := aliceWire()
		h := patGitHub(t, wire)
		before := len(wire.requests())
		rec := h.do(t, http.MethodGet, githubRepoPath("v1.6f2f72", "issues?state=merged"), "")
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeListStateInvalid {
			t.Errorf("GET issues?state=merged = %d %s, want 400 %s", rec.Code, rec.Body, forgeapi.CodeListStateInvalid)
		}
		if after := len(wire.requests()); after != before {
			t.Errorf("GET issues?state=merged: the forge saw %d requests, want none", after-before)
		}
	})
}

func TestReadRoutes_AStateOnAStatelessListIsTheLibrarysRefusal(t *testing.T) {
	wire := aliceWire()
	h := patGitHub(t, wire)
	before := len(wire.requests())
	for _, tail := range []string{"releases?state=open", "labels?state=open"} {
		rec := h.do(t, http.MethodGet, githubRepoPath("v1.6f2f72", tail), "")
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeListStateInvalid {
			t.Errorf("GET %s = %d %s, want 400 %s", tail, rec.Code, rec.Body, forgeapi.CodeListStateInvalid)
		}
	}
	if after := len(wire.requests()); after != before {
		t.Errorf("the forge saw %d requests for refused lists, want none", after-before)
	}
}

func TestReleaseAndLabelRoutes_AbsentRoleIs501(t *testing.T) {
	t.Run("a client with neither role", func(t *testing.T) {
		core := &pagedCore{}
		mux := repoRoutes(recordManager(t, core))
		for _, tail := range []string{"releases", "labels"} {
			rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", tail))
			if rec.Code != http.StatusNotImplemented || decodeBody(t, rec)["code"] != "not_supported" {
				t.Errorf("GET %s = %d %s, want 501 not_supported", tail, rec.Code, rec.Body)
			}
		}
		if asked, _, _ := core.calls(); len(asked) != 0 {
			t.Errorf("the library was asked %+v, want nothing", asked)
		}
	})

	t.Run("a client serving both", func(t *testing.T) {
		core := rolesCore{
			pagedCore: &pagedCore{},
			releasePages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Release]{
				"": {Items: []forgeapi.Release{{TagName: "v1.0.0", Name: "One"}}},
			},
			labelPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Label]{
				"": {Items: []forgeapi.Label{{Name: "bug", Color: "d73a4a"}}},
			},
		}
		mux := repoRoutes(recordManager(t, core))
		want := map[string]map[string]any{
			"releases": {"tag_name": "v1.0.0", "name": "One"},
			"labels":   {"name": "bug", "color": "d73a4a"},
		}
		for tail, row := range want {
			rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", tail))
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s = %d %s, want 200", tail, rec.Code, rec.Body)
				continue
			}
			if rows, _ := decodeBody(t, rec)[tail].([]any); len(rows) != 1 || !reflect.DeepEqual(rows[0], row) {
				t.Errorf("GET %s rows = %v, want [%v]", tail, rows, row)
			}
		}
	})
}

func TestReadRoutes_CarryNextPartialSuccessor(t *testing.T) {
	partial := &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 1}
	successor := &forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/renamed", DisplayPath: "o/renamed"}
	core := rolesCore{
		pagedCore: &pagedCore{
			issuePages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Issue]{
				"":   {Items: []forgeapi.Issue{{Ref: forgeapi.IssueRef{Number: 16}}}, Next: "i2", Partial: partial, Successor: successor},
				"i2": {},
			},
			checks: forgeapi.CommitChecks{State: forgeapi.CheckPending, Partial: partial, Successor: successor},
		},
		releasePages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Release]{
			"":   {Items: []forgeapi.Release{{TagName: "v1"}}, Next: "r2", Partial: partial, Successor: successor},
			"r2": {},
		},
		labelPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Label]{
			"":   {Items: []forgeapi.Label{{Name: "bug"}}, Next: "l2", Partial: partial, Successor: successor},
			"l2": {},
		},
	}
	mux := repoRoutes(recordManager(t, core))
	wantPartial := map[string]any{"reason": "pagination_cap", "fetched": float64(1), "omitted_at_least": float64(1)}
	wantSuccessor := map[string]any{"repo_id": "v1.6f2f72656e616d6564", "display_path": "o/renamed"}

	lists := []struct{ list, next string }{{"issues", "i2"}, {"releases", "r2"}, {"labels", "l2"}}
	for _, l := range lists {
		first := decodeBody(t, getRoute(t, mux, githubRepoPath("v1.6f2f72", l.list)))
		if first["next"] != l.next || !reflect.DeepEqual(first["partial"], wantPartial) || !reflect.DeepEqual(first["successor"], wantSuccessor) {
			t.Errorf("GET %s envelope = %v, want next %s, partial %v and successor %v", l.list, first, l.next, wantPartial, wantSuccessor)
		}
		if rows, ok := first[l.list].([]any); !ok || len(rows) != 1 {
			t.Errorf("GET %s rows = %v, want one row", l.list, first[l.list])
		}
		last := decodeBody(t, getRoute(t, mux, githubRepoPath("v1.6f2f72", l.list+"?after="+l.next)))
		for _, key := range []string{"next", "partial", "successor"} {
			if _, ok := last[key]; ok {
				t.Errorf("GET %s?after=%s: a complete page carries %q: %v", l.list, l.next, key, last)
			}
		}
		if rows, ok := last[l.list].([]any); !ok || len(rows) != 0 {
			t.Errorf("GET %s?after=%s encodes %s = %v, want []", l.list, l.next, l.list, last[l.list])
		}
	}
	_, cursors, _ := core.calls()
	if want := []forgeapi.Cursor{"", "i2", "", "r2", "", "l2"}; !slices.Equal(cursors, want) {
		t.Errorf("the library was asked for cursors %q, want %q", cursors, want)
	}

	checks := decodeBody(t, getRoute(t, mux, githubRepoPath("v1.6f2f72", "checks?ref=main")))
	if !reflect.DeepEqual(checks["partial"], wantPartial) || !reflect.DeepEqual(checks["successor"], wantSuccessor) || checks["state"] != "pending" {
		t.Errorf("GET checks = %v, want state pending, partial %v and successor %v", checks, wantPartial, wantSuccessor)
	}
}

func TestChecksRoute_RefReachesTheLibraryAndAnEmptyRefIs400(t *testing.T) {
	core := &pagedCore{}
	mux := repoRoutes(recordManager(t, core))
	if rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "checks?ref=feat/x")); rec.Code != http.StatusOK {
		t.Fatalf("GET checks?ref=feat/x = %d %s, want 200", rec.Code, rec.Body)
	}
	asked, _, _ := core.calls()
	wantRepo := []forgeapi.RepoRef{{ID: "v1.6f2f72", Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"}}
	if refs := core.commitRefs(); !slices.Equal(refs, []string{"feat/x"}) || !reflect.DeepEqual(asked, wantRepo) {
		t.Errorf("the library was asked for refs %q in %+v, want [feat/x] in %+v", refs, asked, wantRepo)
	}

	wire := aliceWire()
	h := patGitHub(t, wire)
	before := len(wire.requests())
	rec := h.do(t, http.MethodGet, githubRepoPath("v1.6f2f72", "checks"), "")
	if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeRefInvalid {
		t.Errorf("GET checks with no ref = %d %s, want 400 %s", rec.Code, rec.Body, forgeapi.CodeRefInvalid)
	}
	if after := len(wire.requests()); after != before {
		t.Errorf("GET checks with no ref: the forge saw %d requests, want none", after-before)
	}
}

const seedHead = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

// seedPR is pull request #15 of o/r as ReadPR answers it, at head.
func seedPR(head string) forgeapi.PullRequest {
	return forgeapi.PullRequest{
		Ref:   forgeapi.PRRef{Number: 15},
		Repo:  forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"},
		Title: "Seed", Body: "The description.", HeadSHA: head, State: forgeapi.PRStateOpen,
		Action: forgeapi.ActionState{Checks: forgeapi.CheckFailing},
	}
}

// detailPR is the pr object of a detail answer.
func detailPR(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	pr, ok := body["pr"].(map[string]any)
	if !ok {
		t.Fatalf("detail body %v carries no pr object", body)
	}
	return pr
}

func TestPRDetailRoute_CarriesBodyAndChecks(t *testing.T) {
	core := &pagedCore{
		read: seedPR(seedHead),
		checks: forgeapi.CommitChecks{
			State: forgeapi.CheckFailing, Failing: 1, Total: 1,
			Contexts: []forgeapi.CheckContext{{Name: "ci/build", TargetURL: "https://ci.example/1", State: forgeapi.CheckFailing}},
		},
	}
	mux := repoRoutes(recordManager(t, core))

	rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs/15"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET prs/15 = %d %s, want 200", rec.Code, rec.Body)
	}
	body := decodeBody(t, rec)
	pr := detailPR(t, body)
	if pr["number"] != float64(15) || pr["body"] != "The description." || pr["head_sha"] != seedHead || pr["repo_id"] != "v1.6f2f72" {
		t.Errorf("GET prs/15 pr = %v, want #15 with its body, its head and repo_id v1.6f2f72", pr)
	}
	wantChecks := map[string]any{
		"state":          "failing",
		"checks":         []any{map[string]any{"name": "ci/build", "state": "failing", "url": "https://ci.example/1"}},
		"checks_passing": float64(0), "checks_failing": float64(1), "checks_pending": float64(0),
		"checks_neutral": float64(0), "checks_unknown": float64(0), "checks_total": float64(1),
	}
	if !reflect.DeepEqual(body["checks"], wantChecks) {
		t.Errorf("GET prs/15 checks = %v, want %v", body["checks"], wantChecks)
	}

	repo := forgeapi.RepoRef{ID: "v1.6f2f72", Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"}
	asked, _, _ := core.calls()
	if reads := core.reads(); !slices.Equal(reads, []forgeapi.PRRef{{Number: 15}}) || !reflect.DeepEqual(asked, []forgeapi.RepoRef{repo, repo}) {
		t.Errorf("the library read %v in %+v, want #15 and then its checks, both in %+v", reads, asked, repo)
	}
	if refs := core.commitRefs(); !slices.Equal(refs, []string{seedHead}) {
		t.Errorf("the checks were read for %q, want the pull request's head [%s]", refs, seedHead)
	}
}

func TestPRDetailRoute_NoHeadAnswersThePROnly(t *testing.T) {
	core := &pagedCore{read: seedPR("")}
	mux := repoRoutes(recordManager(t, core))

	rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs/15"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET prs/15 = %d %s, want 200", rec.Code, rec.Body)
	}
	body := decodeBody(t, rec)
	if checks, ok := body["checks"]; ok {
		t.Errorf("a row with no head carries checks %v, want none", checks)
	}
	if pr := detailPR(t, body); pr["number"] != float64(15) || pr["head_sha"] != nil {
		t.Errorf("GET prs/15 pr = %v, want #15 with no head_sha", pr)
	}
	if refs := core.commitRefs(); len(refs) != 0 {
		t.Errorf("the checks were read for %q on a row with no head, want no read", refs)
	}
}

func TestPRDetailRoute_IsNotCached(t *testing.T) {
	core := &pagedCore{read: seedPR(seedHead)}
	mux := repoRoutes(recordManager(t, core))

	for range 2 {
		if rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs/15")); rec.Code != http.StatusOK {
			t.Fatalf("GET prs/15 = %d %s, want 200", rec.Code, rec.Body)
		}
	}
	if reads, refs := core.reads(), core.commitRefs(); len(reads) != 2 || len(refs) != 2 {
		t.Errorf("two opens read the pull request %d and its checks %d times, want 2 and 2", len(reads), len(refs))
	}
}

func TestPRDetailRoute_BadNumberIs400(t *testing.T) {
	t.Run("not an integer", func(t *testing.T) {
		core := &pagedCore{read: seedPR(seedHead)}
		mux := repoRoutes(recordManager(t, core))
		for _, seg := range []string{"seven", "1e3", "15x", "99999999999999999999"} {
			if rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs/"+seg)); rec.Code != http.StatusBadRequest {
				t.Errorf("GET prs/%s = %d %s, want 400", seg, rec.Code, rec.Body)
			}
		}
		if reads := core.reads(); len(reads) != 0 {
			t.Errorf("the library read %v for a malformed number, want nothing", reads)
		}
	})

	t.Run("not positive, which the library refuses", func(t *testing.T) {
		wire := aliceWire()
		h := patGitHub(t, wire)
		before := len(wire.requests())
		for _, seg := range []string{"0", "-3"} {
			rec := h.do(t, http.MethodGet, githubRepoPath("v1.6f2f72", "prs/"+seg), "")
			if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeRepoRefInvalid {
				t.Errorf("GET prs/%s = %d %s, want 400 %s", seg, rec.Code, rec.Body, forgeapi.CodeRepoRefInvalid)
			}
		}
		if after := len(wire.requests()); after != before {
			t.Errorf("the forge saw %d requests for a non-positive number, want none", after-before)
		}
	})
}

func TestPRDetailRoute_OnlyGETReads(t *testing.T) {
	core := &pagedCore{read: seedPR(seedHead)}
	mux := repoRoutes(recordManager(t, core))
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, githubRepoPath("v1.6f2f72", "prs/15"), nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s prs/15 = %d Allow %q, want 405 Allow GET", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if reads := core.reads(); len(reads) != 0 {
		t.Errorf("the library read %v for a non-GET, want nothing", reads)
	}
}

func TestPRDetailRoute_ErrorsThroughTheEnvelope(t *testing.T) {
	t.Run("the read fails", func(t *testing.T) {
		core := &pagedCore{readErr: &forgeapi.Error{
			Op: "ReadPR", Code: forgeapi.CodePRNotFound, Kind: forgeapi.KindNotFound, Message: "no pull request 15",
		}}
		mux := repoRoutes(recordManager(t, core))
		rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs/15"))
		body := decodeBody(t, rec)
		if rec.Code != http.StatusNotFound || body["code"] != forgeapi.CodePRNotFound || body["kind"] != "not_found" {
			t.Errorf("GET prs/15 = %d %s, want 404 %s kind not_found", rec.Code, rec.Body, forgeapi.CodePRNotFound)
		}
		if refs := core.commitRefs(); len(refs) != 0 {
			t.Errorf("the checks were read for %q after the read failed, want no read", refs)
		}
	})

	t.Run("the checks read fails", func(t *testing.T) {
		core := &pagedCore{read: seedPR(seedHead), checksErr: &forgeapi.Error{
			Op: "CommitStatus", Kind: forgeapi.KindRateLimited, RetryAfter: 30 * time.Second, Message: "rate limited",
		}}
		mux := repoRoutes(recordManager(t, core))
		rec := getRoute(t, mux, githubRepoPath("v1.6f2f72", "prs/15"))
		body := decodeBody(t, rec)
		if rec.Code != http.StatusTooManyRequests || body["kind"] != "rate_limited" || body["retry_after_s"] != float64(30) {
			t.Errorf("GET prs/15 = %d %s, want 429 rate_limited with retry_after_s 30", rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Retry-After"); got != "30" {
			t.Errorf("Retry-After = %q, want 30", got)
		}
		if pr, ok := body["pr"]; ok {
			t.Errorf("a failed checks read still answered the row %v, want the envelope alone", pr)
		}
	})
}
