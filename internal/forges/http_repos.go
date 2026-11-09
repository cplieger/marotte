package forges

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
)

// The action segment and query parameter the repository routes read.
const (
	stateClose   = "close"
	fieldHeadSHA = "head_sha"
)

// forceOf reads `?refresh=1`: the reader asking for the truth, as `/api/git/status-all?fetch=1` is on the
// local side.
func forceOf(r *http.Request) bool { return r.URL.Query().Get("refresh") == "1" }

// mutated follows a successful mutation of ref: its cached lists go, and the
// inventory is asked to read the connection again. It answers the id of the
// cycle that reads the change.
func (h *HTTPHandler) mutated(rh *readHandle, ref forgeapi.RepoRef) string {
	h.manager.evictRepo(rh.id, ref.ID)
	return h.askCycle()
}

// askCycle asks for a cycle that begins after any in flight, which may have
// read the repository before the change, and answers its id. A handler no
// poller was wired to has no inventory and answers "0".
func (h *HTTPHandler) askCycle() string {
	if h.poller == nil {
		return "0"
	}
	return strconv.FormatUint(h.poller.cycles.ask(), 10)
}

// routeListStates are the filters `?state=` names, in the library's spellings.
var routeListStates = []forgeapi.ListState{
	forgeapi.ListStateOpen, forgeapi.ListStateClosed, forgeapi.ListStateMerged, forgeapi.ListStateAll,
}

// listRequestOf reads a list route's `?state=` and `?after=`. An absent state is
// the library's default, open; any other text is the unknown member, which the
// library refuses as list_state_invalid.
func listRequestOf(q url.Values) (listRequest, error) {
	var opts []forgeapi.ListOption
	if raw := q.Get("state"); raw != "" {
		state := forgeapi.ListStateUnknown
		for _, s := range routeListStates {
			if s.String() == raw {
				state = s
				break
			}
		}
		opts = append(opts, forgeapi.WithState(state))
	}
	if after := q.Get("after"); after != "" {
		opts = append(opts, forgeapi.WithAfter(forgeapi.Cursor(after)))
	}
	return resolveList(opts...)
}

func (h *HTTPHandler) handleRepoList(w http.ResponseWriter, r *http.Request, id string) {
	rh, ok := h.handle(w, id)
	if !ok {
		return
	}
	req, err := listRequestOf(r.URL.Query())
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	page, err := h.manager.repoPage(r.Context(), &rh, &req, forceOf(r))
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, page)
}

// PRCreate is POST .../prs' body.
type PRCreate struct {
	Title        string   `json:"title"`
	Body         string   `json:"body,omitempty"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	Labels       []string `json:"labels,omitempty"`
	Draft        bool     `json:"draft,omitempty"`
}

// handlePRCollection serves the repo-level PR endpoints: list (GET) and create
// (POST), which answers the new row.
func (h *HTTPHandler) handlePRCollection(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		req, err := listRequestOf(r.URL.Query())
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		page, err := h.manager.prPage(r.Context(), rh, ref, &req, forceOf(r))
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		httpserver.WriteJSON(w, page)
	case http.MethodPost:
		var params PRCreate
		httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		created, err := rh.core.CreatePR(r.Context(), ref, forgeapi.NewPullRequest{
			Title: params.Title, Body: params.Body, SourceBranch: params.SourceBranch,
			TargetBranch: params.TargetBranch, Labels: params.Labels, Draft: params.Draft,
		})
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		h.mutated(rh, ref)
		httpserver.WriteJSON(w, prWire(&created))
	}
}

// It is read on every open rather than cached, because it is the read a person makes to see the
// current state. A non-positive number is the library's to refuse.
func handlePRDetail(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef, number int) {
	core := rh.core
	pr, err := core.ReadPR(r.Context(), ref, forgeapi.PRRef{Number: number})
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	detail := PRDetail{PR: prWire(&pr)}
	if pr.HeadSHA != "" {
		checks, statusErr := core.CommitStatus(r.Context(), ref, pr.HeadSHA)
		if statusErr != nil {
			writeOpsError(w, r, statusErr)
			return
		}
		folded := commitChecksWire(&checks)
		detail.Checks = &folded
	}
	httpserver.WriteJSON(w, detail)
}

// Marotte's own refusals on the merge route, in the forge error envelope.
const (
	// codeMergeInvalid answers a body that is not JSON of the merge's shape, or
	// whose intent is not one of the library's spellings.
	codeMergeInvalid = "merge_invalid"
	// codeStrategyRequired answers a merge naming no strategy on a family whose
	// default intent is a merge commit.
	codeStrategyRequired = "strategy_required"
)

// PRMerge is POST .../prs/{number}/merge's body.
type PRMerge struct {
	Intent       string `json:"intent"`
	Strategy     string `json:"strategy,omitempty"`
	HeadSHA      string `json:"head_sha"`
	Auto         bool   `json:"auto,omitempty"`
	DeleteBranch bool   `json:"delete_branch,omitempty"`
}

// mergeIntents are the intents a merge body may name, in the library's spellings.
var mergeIntents = []forgeapi.MergeIntent{forgeapi.IntentDefault, forgeapi.IntentSquash, forgeapi.IntentNoSquash}

// handleMerge serves one pull request's merge: POST merges it and answers the
// outcome, GET (and its HEAD) reads its merge state back, the read that follows
// an accepted or queued merge. Neither is cached.
func (h *HTTPHandler) handleMerge(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef, number int) {
	pr := forgeapi.PRRef{Number: number}
	if r.Method != http.MethodPost {
		status, statusErr := rh.core.MergeStatus(r.Context(), ref, pr)
		if statusErr != nil {
			writeOpsError(w, r, statusErr)
			return
		}
		httpserver.WriteJSON(w, mergeStatusWire(&status))
		return
	}
	req, ok := mergeRequestOf(w, r, rh.family)
	if !ok {
		return
	}
	outcome, err := rh.core.MergePR(r.Context(), ref, pr, req)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	cycle := h.mutated(rh, ref)
	httpserver.WriteJSON(w, MergeResult{Outcome: mergeOutcomeWire(&outcome), CycleID: cycle})
}

// mergeRequestOf reads the merge body, answering the refusal itself and false
// when the body does not say what to merge. The head pin and the strategy's
// spelling are the library's to check.
func mergeRequestOf(w http.ResponseWriter, r *http.Request, family forgeapi.Family) (forgeapi.MergeRequest, bool) {
	var body PRMerge
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeMergeRefusal(w, http.StatusBadRequest, codeMergeInvalid,
			"the body must be {intent, strategy?, head_sha, auto?, delete_branch?}")
		return forgeapi.MergeRequest{}, false
	}
	i := slices.IndexFunc(mergeIntents, func(in forgeapi.MergeIntent) bool { return in.String() == body.Intent })
	if i < 0 {
		writeMergeRefusal(w, http.StatusBadRequest, codeMergeInvalid, "intent must be default, squash or no_squash")
		return forgeapi.MergeRequest{}, false
	}
	// The default intent is a merge commit on GitHub and the Gitea family, so a
	// merge there always names its strategy rather than inheriting one.
	if family != forgeapi.FamilyGitLab && body.Strategy == "" {
		writeMergeRefusal(w, http.StatusBadRequest, codeStrategyRequired,
			"a merge on this forge must name its strategy (squash, merge or rebase)")
		return forgeapi.MergeRequest{}, false
	}
	return forgeapi.MergeRequest{
		Intent: mergeIntents[i], Strategy: body.Strategy, HeadSHA: body.HeadSHA,
		DeleteBranch: body.DeleteBranch, AutoMerge: body.Auto,
	}, true
}

func writeMergeRefusal(w http.ResponseWriter, status int, code, msg string) {
	httpserver.WriteJSONStatus(w, status, httpreply.ErrorJSONWithCode(msg, code))
}

// prAction is the per-PR mutation op, close or reopen, answering the row as the forge left it.
func (h *HTTPHandler) prAction(op string) func(http.ResponseWriter, *http.Request, *readHandle, forgeapi.RepoRef, int) {
	return func(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef, number int) {
		pr := forgeapi.PRRef{Number: number}
		var row forgeapi.PullRequest
		var err error
		if op == stateClose {
			row, err = rh.core.ClosePR(r.Context(), ref, pr)
		} else {
			row, err = rh.core.ReopenPR(r.Context(), ref, pr)
		}
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		cycle := h.mutated(rh, ref)
		httpserver.WriteJSON(w, PRChanged{PR: prWire(&row), CycleID: cycle})
	}
}

// handlePRRerun re-runs a PR's failed CI pinned to `?head_sha=`, the head the
// caller's row displayed: a re-run can trigger a deployment, so the library
// refuses one for any other commit. No pin re-runs the head as it is now. A
// re-run changes no forge object, so it evicts nothing; the checks it restarts
// are the cycle's to read.
func (h *HTTPHandler) handlePRRerun(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef, number int) {
	pr := forgeapi.PRRef{Number: number}
	if err := rh.core.RerunFailedChecks(r.Context(), ref, pr, r.URL.Query().Get(fieldHeadSHA)); err != nil {
		writeOpsError(w, r, err)
		return
	}
	if h.poller != nil {
		h.poller.refill(rh.id, ref.ID, pr.Number)
	}
	h.askCycle()
	httpreply.OK(w)
}

// IssueCreate is POST .../issues' body.
type IssueCreate struct {
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// handleIssueCollection serves the repo-level issue endpoints: list (GET) and
// create (POST), which answers the new issue.
func (h *HTTPHandler) handleIssueCollection(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		req, err := listRequestOf(r.URL.Query())
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		page, err := rh.core.ListIssues(r.Context(), ref, req.opts...)
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		httpserver.WriteJSON(w, issueListWire(&page))
	case http.MethodPost:
		var params IssueCreate
		httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		created, err := rh.core.CreateIssue(r.Context(), ref, forgeapi.NewIssue{
			Title: params.Title, Body: params.Body, Labels: params.Labels,
		})
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		h.mutated(rh, ref)
		httpserver.WriteJSON(w, issueWire(&created))
	}
}

// handleIssueClose closes one issue, answering it as the forge left it.
func (h *HTTPHandler) handleIssueClose(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef, number int) {
	closed, err := rh.core.CloseIssue(r.Context(), ref, forgeapi.IssueRef{Number: number})
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	h.mutated(rh, ref)
	httpserver.WriteJSON(w, issueWire(&closed))
}

// handleChecks answers the folded CI verdict of `?ref=`, a branch or a SHA. The
// library refuses an empty or malformed ref as ref_invalid before any request.
func handleChecks(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
	checks, err := rh.core.CommitStatus(r.Context(), ref, r.URL.Query().Get("ref"))
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, commitChecksWire(&checks))
}

// Releases is an optional role, so a client without it answers errNotSupported.
func (h *HTTPHandler) handleReleases(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
	rel, ok := rh.core.(forgeapi.Releases)
	if !ok {
		writeOpsError(w, r, errNotSupported)
		return
	}
	if r.Method == http.MethodPost {
		h.createRelease(w, r, rh, rel, ref)
		return
	}
	req, err := listRequestOf(r.URL.Query())
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	page, err := rel.ListReleases(r.Context(), ref, req.opts...)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, releaseListWire(&page))
}

// ReleaseCreate is POST .../releases' body.
type ReleaseCreate struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name,omitempty"`
	Body       string `json:"body,omitempty"`
	Target     string `json:"target,omitempty"` // commit SHA or branch
	Draft      bool   `json:"draft,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

func (h *HTTPHandler) createRelease(w http.ResponseWriter, r *http.Request, rh *readHandle, rel forgeapi.Releases, ref forgeapi.RepoRef) {
	var params ReleaseCreate
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	created, err := rel.CreateRelease(r.Context(), ref, forgeapi.NewRelease{
		TagName: params.TagName, Name: params.Name, Body: params.Body, Target: params.Target,
		Draft: params.Draft, Prerelease: params.Prerelease,
	})
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	h.mutated(rh, ref)
	httpserver.WriteJSON(w, releaseWire(&created))
}

// A mutation of the repository evicts the cached answer with its lists.
func (h *HTTPHandler) handleAffordances(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
	aff, err := h.manager.repoAffordances(r.Context(), rh, ref, forceOf(r))
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, aff)
}

// Labels is an optional role, so a client without it answers errNotSupported.
func handleLabels(w http.ResponseWriter, r *http.Request, rh *readHandle, ref forgeapi.RepoRef) {
	lab, ok := rh.core.(forgeapi.Labels)
	if !ok {
		writeOpsError(w, r, errNotSupported)
		return
	}
	req, err := listRequestOf(r.URL.Query())
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	page, err := lab.ListLabels(r.Context(), ref, req.opts...)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, labelListWire(&page))
}
