package forges

// Tests for the merge route and the merge-status read: the merge body reaching the
// library, the family rules the route adds before it, and the outcome and status
// answers. The library's own refusals are driven through a real family client.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
)

const mergeHead = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

// mergeCore records every merge and status read and answers what it holds. The
// embedded Core is nil, so any other method panics.
type mergeCore struct {
	forgeapi.Core
	err       error
	status    forgeapi.MergeStatus
	outcome   forgeapi.MergeOutcome
	requests  []forgeapi.MergeRequest
	repos     []forgeapi.RepoRef
	prs       []forgeapi.PRRef
	statusPRs []forgeapi.PRRef
	mu        sync.Mutex
}

func (c *mergeCore) MergePR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	c.repos = append(c.repos, repo)
	c.prs = append(c.prs, pr)
	return c.outcome, c.err
}

func (c *mergeCore) MergeStatus(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.MergeStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repos = append(c.repos, repo)
	c.statusPRs = append(c.statusPRs, pr)
	return c.status, c.err
}

func (c *mergeCore) merges() []forgeapi.MergeRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
}

type mergeRow struct {
	rec    connectionRecord
	repoID string
	family forgeapi.Family
	sel    string
}

func githubMergeRow() mergeRow {
	return mergeRow{rec: githubRecord(), repoID: "v1.6f2f72", family: forgeapi.FamilyGitHub, sel: "o/r"}
}

func gitlabMergeRow() mergeRow {
	return mergeRow{
		rec:    connectionRecord{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com", WebBaseURL: "https://gitlab.com"},
		repoID: "v1.666f7267656170692d6c6976652f6e65737465642f73616e64626f78",
		family: forgeapi.FamilyGitLab, sel: "forgeapi-live/nested/sandbox",
	}
}

func giteaMergeRow() mergeRow {
	return mergeRow{rec: localGiteaRecord(), repoID: "v1.6f2f72", family: forgeapi.FamilyGitea, sel: "o/r"}
}

func codebergMergeRow() mergeRow {
	return mergeRow{
		rec:    connectionRecord{ID: "codeberg:codeberg.org", Kind: KindCodeberg, Host: "codeberg.org", WebBaseURL: "https://codeberg.org"},
		repoID: "v1.6f2f72", family: forgeapi.FamilyGitea, sel: "o/r",
	}
}

func (row mergeRow) mergePath(n string) string {
	return "/api/forges/" + url.PathEscape(row.rec.ID) + "/repos/" + row.repoID + "/prs/" + n + "/merge"
}

// Each manager registers its helper in a git config of its own, so the package's shared one does
// not grow with every case.
func mergeMux(t *testing.T, row mergeRow, core forgeapi.Core) *http.ServeMux {
	t.Helper()
	isolateGit(t)
	rec := row.rec
	return repoRoutes(recordManagerFor(t, &rec, core))
}

func sendRoute(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body)))
	return rec
}

func TestMergeRoute_BodyCarriesIntentStrategyAndPin(t *testing.T) {
	cases := []struct {
		row  mergeRow
		want forgeapi.MergeRequest
		name string
		body string
	}{
		{
			name: "GitHub squash names the family spelling",
			row:  githubMergeRow(),
			body: `{"intent":"default","strategy":"squash","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{Intent: forgeapi.IntentDefault, Strategy: "squash", HeadSHA: mergeHead},
		},
		{
			name: "GitHub rebase",
			row:  githubMergeRow(),
			body: `{"intent":"default","strategy":"rebase","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{Strategy: "rebase", HeadSHA: mergeHead},
		},
		{
			name: "GitHub carries an intent beside its strategy",
			row:  githubMergeRow(),
			body: `{"intent":"no_squash","strategy":"merge","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, Strategy: "merge", HeadSHA: mergeHead},
		},
		{
			name: "Gitea rebase and the branch deletion",
			row:  giteaMergeRow(),
			body: `{"intent":"default","strategy":"rebase","head_sha":"` + mergeHead + `","delete_branch":true}`,
			want: forgeapi.MergeRequest{Strategy: "rebase", HeadSHA: mergeHead, DeleteBranch: true},
		},
		{
			name: "Codeberg squash",
			row:  codebergMergeRow(),
			body: `{"intent":"default","strategy":"squash","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{Strategy: "squash", HeadSHA: mergeHead},
		},
		{
			name: "GitLab squash is the intent",
			row:  gitlabMergeRow(),
			body: `{"intent":"squash","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{Intent: forgeapi.IntentSquash, HeadSHA: mergeHead},
		},
		{
			name: "GitLab no_squash",
			row:  gitlabMergeRow(),
			body: `{"intent":"no_squash","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: mergeHead},
		},
		{
			name: "GitLab default",
			row:  gitlabMergeRow(),
			body: `{"intent":"default","head_sha":"` + mergeHead + `"}`,
			want: forgeapi.MergeRequest{HeadSHA: mergeHead},
		},
		{
			name: "GitLab arms its auto-merge",
			row:  gitlabMergeRow(),
			body: `{"intent":"squash","head_sha":"` + mergeHead + `","auto":true}`,
			want: forgeapi.MergeRequest{Intent: forgeapi.IntentSquash, HeadSHA: mergeHead, AutoMerge: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := &mergeCore{outcome: forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeMerged}}
			mux := mergeMux(t, tc.row, core)

			rec := sendRoute(t, mux, http.MethodPost, tc.row.mergePath("15"), tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST merge %s = %d %s, want 200", tc.body, rec.Code, rec.Body)
			}
			if got := core.merges(); !reflect.DeepEqual(got, []forgeapi.MergeRequest{tc.want}) {
				t.Errorf("POST merge %s reached the library as %+v, want [%+v]", tc.body, got, tc.want)
			}
			wantRepo := forgeapi.RepoRef{ID: tc.row.repoID, Family: tc.row.family, Selector: tc.row.sel, DisplayPath: tc.row.sel}
			if !reflect.DeepEqual(core.repos, []forgeapi.RepoRef{wantRepo}) || !slices.Equal(core.prs, []forgeapi.PRRef{{Number: 15}}) {
				t.Errorf("the library merged %+v in %+v, want #15 in %+v", core.prs, core.repos, wantRepo)
			}
		})
	}
}

func TestMergeRoute_EmptyHeadIsMissingSHA400(t *testing.T) {
	for name, body := range map[string]string{
		"empty":  `{"intent":"default","strategy":"squash","head_sha":""}`,
		"absent": `{"intent":"default","strategy":"squash"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wire := aliceWire()
			h := patGitHub(t, wire)
			before := len(wire.requests())

			rec := h.do(t, http.MethodPost, githubMergeRow().mergePath("15"), body)
			if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != forgeapi.CodeMissingSHA {
				t.Errorf("POST merge %s = %d %s, want 400 %s", body, rec.Code, rec.Body, forgeapi.CodeMissingSHA)
			}
			if after := len(wire.requests()); after != before {
				t.Errorf("the forge saw %d requests for an unpinned merge, want none", after-before)
			}
		})
	}
}

func TestMergeRoute_AnswersTheOutcome(t *testing.T) {
	cases := []struct {
		want    map[string]any
		name    string
		outcome forgeapi.MergeOutcome
	}{
		{
			name: "merged",
			outcome: forgeapi.MergeOutcome{
				State: forgeapi.MergeOutcomeMerged, QueueState: forgeapi.QueueNone, QueuePosition: forgeapi.QueuePositionUnknown,
			},
			want: map[string]any{"state": "merged", "queue_state": "none", "queue_position": float64(-1)},
		},
		{
			name: "accepted carries its code",
			outcome: forgeapi.MergeOutcome{
				State: forgeapi.MergeOutcomeAccepted, Code: forgeapi.CodeAlreadyEnqueued,
				QueueState: forgeapi.QueueUnknown, QueuePosition: forgeapi.QueuePositionUnknown,
			},
			want: map[string]any{
				"state": "accepted", "code": forgeapi.CodeAlreadyEnqueued, "queue_state": "unknown", "queue_position": float64(-1),
			},
		},
		{
			name: "enqueued carries its place",
			outcome: forgeapi.MergeOutcome{
				State: forgeapi.MergeOutcomeEnqueued, QueueState: forgeapi.QueueQueued, QueuePosition: 3,
			},
			want: map[string]any{"state": "enqueued", "queue_state": "queued", "queue_position": float64(3)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := &mergeCore{outcome: tc.outcome}
			mux := mergeMux(t, githubMergeRow(), core)

			rec := sendRoute(t, mux, http.MethodPost, githubMergeRow().mergePath("15"),
				`{"intent":"default","strategy":"squash","head_sha":"`+mergeHead+`"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST merge = %d %s, want 200", rec.Code, rec.Body)
			}
			body := decodeBody(t, rec)
			if !reflect.DeepEqual(body["outcome"], tc.want) {
				t.Errorf("POST merge answered %v, want outcome %v", body, tc.want)
			}
		})
	}

	t.Run("a refusal is the error envelope", func(t *testing.T) {
		core := &mergeCore{err: &forgeapi.Error{
			Op: "MergePR", Code: forgeapi.CodeNotMergeable, Kind: forgeapi.KindNotMergeable, Message: "checks are failing",
		}}
		mux := mergeMux(t, githubMergeRow(), core)

		rec := sendRoute(t, mux, http.MethodPost, githubMergeRow().mergePath("15"),
			`{"intent":"default","strategy":"squash","head_sha":"`+mergeHead+`"}`)
		body := decodeBody(t, rec)
		if rec.Code != http.StatusConflict || body["code"] != forgeapi.CodeNotMergeable || body["kind"] != "not_mergeable" {
			t.Errorf("POST merge = %d %s, want 409 %s kind not_mergeable", rec.Code, rec.Body, forgeapi.CodeNotMergeable)
		}
		if _, ok := body["outcome"]; ok {
			t.Errorf("a refused merge answered an outcome %v, want the envelope alone", body["outcome"])
		}
	})
}

func TestMergeRoute_GitLabStrategyIsRefused400(t *testing.T) {
	wire := &userWire{status: http.StatusOK, body: `{"id":1,"username":"alice"}`}
	h := newConnectHarness(t, wire)
	connectGitLabByPAT(t, h)
	before := len(wire.requests())

	rec := h.do(t, http.MethodPost, gitlabMergeRow().mergePath("11"),
		`{"intent":"default","strategy":"rebase","head_sha":"`+mergeHead+`"}`)
	if body := decodeBody(t, rec); rec.Code != http.StatusBadRequest || body["code"] != forgeapi.CodeStrategyNotAllowed {
		t.Errorf("POST merge with a strategy on GitLab = %d %s, want 400 %s", rec.Code, rec.Body, forgeapi.CodeStrategyNotAllowed)
	}
	if after := len(wire.requests()); after != before {
		t.Errorf("the forge saw %d requests for a refused strategy, want none", after-before)
	}
}

func TestMergeRoute_GitHubAndGiteaWithoutAStrategyAre400(t *testing.T) {
	body := `{"intent":"squash","head_sha":"` + mergeHead + `"}`
	for name, row := range map[string]mergeRow{
		"GitHub": githubMergeRow(), "Gitea": giteaMergeRow(), "Codeberg": codebergMergeRow(),
	} {
		t.Run(name, func(t *testing.T) {
			core := &mergeCore{outcome: forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeMerged}}
			mux := mergeMux(t, row, core)

			rec := sendRoute(t, mux, http.MethodPost, row.mergePath("15"), body)
			if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != "strategy_required" {
				t.Errorf("POST merge %s on %s = %d %s, want 400 strategy_required", body, name, rec.Code, rec.Body)
			}
			if got := core.merges(); len(got) != 0 {
				t.Errorf("the library was asked to merge %+v with no strategy named, want nothing", got)
			}
		})
	}

	t.Run("GitLab takes the intent alone", func(t *testing.T) {
		core := &mergeCore{outcome: forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeMerged}}
		mux := mergeMux(t, gitlabMergeRow(), core)
		if rec := sendRoute(t, mux, http.MethodPost, gitlabMergeRow().mergePath("11"), body); rec.Code != http.StatusOK {
			t.Errorf("POST merge %s on GitLab = %d %s, want 200", body, rec.Code, rec.Body)
		}
	})
}

// The library arms each forge's own auto-merge on every family (ADR-0103), so a
// merge asked to wait reaches it with AutoMerge set on each of them.
func TestMergeRoute_AutoReachesTheLibraryOnEveryFamily(t *testing.T) {
	for name, row := range map[string]mergeRow{
		"GitHub": githubMergeRow(), "Gitea": giteaMergeRow(), "Codeberg": codebergMergeRow(), "GitLab": gitlabMergeRow(),
	} {
		t.Run(name, func(t *testing.T) {
			core := &mergeCore{outcome: forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeEnqueued}}
			mux := mergeMux(t, row, core)

			rec := sendRoute(t, mux, http.MethodPost, row.mergePath("15"),
				`{"intent":"default","strategy":"squash","head_sha":"`+mergeHead+`","auto":true}`)
			if rec.Code != http.StatusOK {
				t.Errorf("POST merge with auto on %s = %d %s, want 200", name, rec.Code, rec.Body)
			}
			if got := core.merges(); len(got) != 1 || !got[0].AutoMerge {
				t.Errorf("the library was asked to merge %+v, want one request with AutoMerge set", got)
			}
		})
	}
}

func TestMergeRoute_MalformedBodyIs400(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":        `intent=squash`,
		"no intent":       `{"strategy":"squash","head_sha":"` + mergeHead + `"}`,
		"empty intent":    `{"intent":"","strategy":"squash","head_sha":"` + mergeHead + `"}`,
		"unknown intent":  `{"intent":"rebase","head_sha":"` + mergeHead + `"}`,
		"auto not a bool": `{"intent":"default","strategy":"squash","head_sha":"` + mergeHead + `","auto":"1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			core := &mergeCore{outcome: forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeMerged}}
			mux := mergeMux(t, githubMergeRow(), core)

			rec := sendRoute(t, mux, http.MethodPost, githubMergeRow().mergePath("15"), body)
			if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != "merge_invalid" {
				t.Errorf("POST merge %s = %d %s, want 400 merge_invalid", body, rec.Code, rec.Body)
			}
			if got := core.merges(); len(got) != 0 {
				t.Errorf("the library was asked to merge %+v on a malformed body, want nothing", got)
			}
		})
	}

	t.Run("not a number", func(t *testing.T) {
		core := &mergeCore{}
		mux := mergeMux(t, githubMergeRow(), core)
		rec := sendRoute(t, mux, http.MethodPost, githubMergeRow().mergePath("seven"),
			`{"intent":"default","strategy":"squash","head_sha":"`+mergeHead+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST prs/seven/merge = %d %s, want 400", rec.Code, rec.Body)
		}
		if got := core.merges(); len(got) != 0 {
			t.Errorf("the library was asked to merge %+v for a malformed number, want nothing", got)
		}
	})
}

func TestMergeStatusRoute_AnswersMergedAndQueue(t *testing.T) {
	moved := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/r2", DisplayPath: "o/r2"}
	cases := []struct {
		want   map[string]any
		name   string
		status forgeapi.MergeStatus
	}{
		{
			name:   "merged, with its page",
			status: forgeapi.MergeStatus{Merged: forgeapi.SupportYes, Queue: forgeapi.QueueNone, WebURL: "https://github.com/o/r/pull/15"},
			want:   map[string]any{"merged": "yes", "queue_state": "none", "web_url": "https://github.com/o/r/pull/15"},
		},
		{
			name:   "still queued, the repository moved",
			status: forgeapi.MergeStatus{Merged: forgeapi.SupportNo, Queue: forgeapi.QueueQueued, Successor: &moved},
			want: map[string]any{
				"merged": "no", "queue_state": "queued",
				"successor": map[string]any{"repo_id": "v1.6f2f7232", "display_path": "o/r2"},
			},
		},
		{
			name:   "unread",
			status: forgeapi.MergeStatus{},
			want:   map[string]any{"merged": "unknown", "queue_state": "unknown"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := &mergeCore{status: tc.status}
			mux := mergeMux(t, githubMergeRow(), core)

			rec := getRoute(t, mux, githubMergeRow().mergePath("15"))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET merge = %d %s, want 200", rec.Code, rec.Body)
			}
			if body := decodeBody(t, rec); !reflect.DeepEqual(body, tc.want) {
				t.Errorf("GET merge answered %v, want %v", body, tc.want)
			}
			if !slices.Equal(core.statusPRs, []forgeapi.PRRef{{Number: 15}}) || len(core.merges()) != 0 {
				t.Errorf("GET merge read the status of %+v and merged %+v, want #15 read and nothing merged",
					core.statusPRs, core.merges())
			}
		})
	}

	t.Run("a failed read is the error envelope", func(t *testing.T) {
		core := &mergeCore{err: &forgeapi.Error{
			Op: "MergeStatus", Code: forgeapi.CodePRNotFound, Kind: forgeapi.KindNotFound, Message: "no pull request 15",
		}}
		mux := mergeMux(t, githubMergeRow(), core)
		rec := getRoute(t, mux, githubMergeRow().mergePath("15"))
		if body := decodeBody(t, rec); rec.Code != http.StatusNotFound || body["code"] != forgeapi.CodePRNotFound {
			t.Errorf("GET merge = %d %s, want 404 %s", rec.Code, rec.Body, forgeapi.CodePRNotFound)
		}
	})
}

func TestMergeRoute_OtherMethodsAre405(t *testing.T) {
	core := &mergeCore{}
	mux := mergeMux(t, githubMergeRow(), core)
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := sendRoute(t, mux, method, githubMergeRow().mergePath("15"), "")
		allow := rec.Header().Get("Allow")
		if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodPost) {
			t.Errorf("%s merge = %d Allow %q, want 405 allowing GET and POST", method, rec.Code, allow)
		}
	}
	if len(core.merges()) != 0 || len(core.statusPRs) != 0 {
		t.Errorf("another method merged %+v or read %+v, want nothing", core.merges(), core.statusPRs)
	}
}
