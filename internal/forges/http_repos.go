package forges

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// The action segment and query parameter the repository routes read.
const (
	stateClose   = "close"
	fieldHeadSHA = "head_sha"
)

// handleRepos dispatches /api/forges/{id}/repos/* paths. A repository is the
// {repo_id} segment, decoded once on the connection's family.
//
// `?refresh=1` means the reader pressed a refresh control and is asking for the
// truth, mirroring `/api/git/status-all?fetch=1`, the same distinction on the
// local-git side: a list's first page comes from the forge and replaces what is
// cached. Anything else reads through the cache.
func (h *HTTPHandler) handleRepos(w http.ResponseWriter, r *http.Request, id, rest string) {
	fc, ok := h.connectionClient(w, id)
	if !ok {
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	if rest == "" {
		h.handleRepoList(w, r, fc, force)
		return
	}
	segment, sub, _ := splitFirst(rest)
	// The library accepts only the canonical encoding, so ref.ID is the id this
	// server mints and keys every cache entry on.
	ref, err := forgeapi.DecodeRepoRef(segment, fc.family)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	if sub == "" {
		httpreply.BadRequest(w, "missing sub-resource (prs/issues/checks/releases/labels/affordances)")
		return
	}
	op, tail, _ := splitFirst(sub)
	switch op {
	case "prs":
		if tail == "" {
			h.handlePRCollection(w, r, fc, ref, force)
			return
		}
		if !strings.Contains(tail, "/") {
			handlePRDetail(w, r, fc.core, ref, tail)
			return
		}
		if numStr, op, _ := splitFirst(tail); op == "merge" {
			h.handleMerge(w, r, fc, ref, numStr)
			return
		}
		h.handlePRAction(w, r, fc, ref, tail)
	case "issues":
		h.handleIssues(w, r, fc, ref, tail)
	case "checks":
		handleChecks(w, r, fc.core, ref)
	case "releases":
		h.handleReleases(w, r, fc, ref)
	case "labels":
		handleLabels(w, r, fc.core, ref)
	case "affordances":
		h.handleAffordances(w, r, fc, ref, force)
	default:
		httpreply.NotFound(w, "unknown repo sub-resource")
	}
}

// mutated follows a successful mutation of ref: its cached lists go, and the
// inventory is asked to read the connection again. It answers the id of the
// cycle that reads the change.
func (h *HTTPHandler) mutated(fc forgeClient, ref forgeapi.RepoRef) string {
	h.manager.evictRepo(fc.id, ref.ID)
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

func (h *HTTPHandler) handleRepoList(w http.ResponseWriter, r *http.Request, fc forgeClient, force bool) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	req, err := listRequestOf(r.URL.Query())
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	page, err := h.manager.repoPage(r.Context(), fc, &req, force)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, page)
}

// createPRBody is a pull request as the client asks for one.
type createPRBody struct {
	Title        string   `json:"title"`
	Body         string   `json:"body,omitempty"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	Labels       []string `json:"labels,omitempty"`
	Draft        bool     `json:"draft,omitempty"`
}

// handlePRCollection serves the repo-level PR endpoints: list (GET) and create
// (POST), which answers the new row.
func (h *HTTPHandler) handlePRCollection(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, force bool) {
	switch r.Method {
	case http.MethodGet:
		req, err := listRequestOf(r.URL.Query())
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		page, err := h.manager.prPage(r.Context(), fc, ref, &req, force)
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		webhttp.WriteJSON(w, page)
	case http.MethodPost:
		var params createPRBody
		webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		created, err := fc.core.CreatePR(r.Context(), ref, forgeapi.NewPullRequest{
			Title: params.Title, Body: params.Body, SourceBranch: params.SourceBranch,
			TargetBranch: params.TargetBranch, Labels: params.Labels, Draft: params.Draft,
		})
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		h.mutated(fc, ref)
		webhttp.WriteJSON(w, prWire(&created))
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handlePRDetail answers one pull request with its description and its head
// commit's checks. It is read on every open rather than cached, because it is
// the read a person makes to see the current state. A non-positive number is
// the library's to refuse.
func handlePRDetail(w http.ResponseWriter, r *http.Request, core forgeapi.Core, ref forgeapi.RepoRef, numStr string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	number, err := strconv.Atoi(numStr)
	if err != nil {
		httpreply.BadRequest(w, "invalid PR number")
		return
	}
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
	webhttp.WriteJSON(w, detail)
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

// mergeBody is one merge as the client sends it.
type mergeBody struct {
	Intent       string `json:"intent"`
	Strategy     string `json:"strategy"`
	HeadSHA      string `json:"head_sha"`
	Auto         bool   `json:"auto"`
	DeleteBranch bool   `json:"delete_branch"`
}

// mergeIntents are the intents a merge body may name, in the library's spellings.
var mergeIntents = []forgeapi.MergeIntent{forgeapi.IntentDefault, forgeapi.IntentSquash, forgeapi.IntentNoSquash}

// handleMerge serves one pull request's merge: POST merges it and answers the
// outcome, GET reads its merge state back, the read that follows an accepted or
// queued merge. Neither is cached.
func (h *HTTPHandler) handleMerge(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, numStr string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
		return
	}
	number, err := strconv.Atoi(numStr)
	if err != nil {
		httpreply.BadRequest(w, "invalid PR number")
		return
	}
	pr := forgeapi.PRRef{Number: number}
	if r.Method == http.MethodGet {
		status, statusErr := fc.core.MergeStatus(r.Context(), ref, pr)
		if statusErr != nil {
			writeOpsError(w, r, statusErr)
			return
		}
		webhttp.WriteJSON(w, mergeStatusWire(&status))
		return
	}
	req, ok := mergeRequestOf(w, r, fc.family)
	if !ok {
		return
	}
	outcome, err := fc.core.MergePR(r.Context(), ref, pr, req)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	cycle := h.mutated(fc, ref)
	webhttp.WriteJSON(w, MergeResult{Outcome: mergeOutcomeWire(&outcome), CycleID: cycle})
}

// mergeRequestOf reads the merge body, answering the refusal itself and false
// when the body does not say what to merge. The head pin and the strategy's
// spelling are the library's to check.
func mergeRequestOf(w http.ResponseWriter, r *http.Request, family forgeapi.Family) (forgeapi.MergeRequest, bool) {
	var body mergeBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
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
	webhttp.WriteJSONStatus(w, status, httpreply.ErrorJSONWithCode(msg, code))
}

// handlePRAction serves the per-PR mutations, all POST-only: close and reopen
// answer the row as the forge left it, rerun answers ok.
func (h *HTTPHandler) handlePRAction(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, tail string) {
	numStr, op, _ := splitFirst(tail)
	number, err := strconv.Atoi(numStr)
	if err != nil {
		httpreply.BadRequest(w, "invalid PR number")
		return
	}
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	pr := forgeapi.PRRef{Number: number}
	var row forgeapi.PullRequest
	switch op {
	case stateClose:
		row, err = fc.core.ClosePR(r.Context(), ref, pr)
	case "reopen":
		row, err = fc.core.ReopenPR(r.Context(), ref, pr)
	case "rerun":
		h.handlePRRerun(w, r, fc, ref, pr)
		return
	default:
		httpreply.NotFound(w, "unknown PR action")
		return
	}
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	cycle := h.mutated(fc, ref)
	webhttp.WriteJSON(w, PRChanged{PR: prWire(&row), CycleID: cycle})
}

// handlePRRerun re-runs a PR's failed CI pinned to `?head_sha=`, the head the
// caller's row displayed: a re-run can trigger a deployment, so the library
// refuses one for any other commit. No pin re-runs the head as it is now. A
// re-run changes no forge object, so it evicts nothing; the checks it restarts
// are the cycle's to read.
func (h *HTTPHandler) handlePRRerun(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, pr forgeapi.PRRef) {
	if err := fc.core.RerunFailedChecks(r.Context(), ref, pr, r.URL.Query().Get(fieldHeadSHA)); err != nil {
		writeOpsError(w, r, err)
		return
	}
	if h.poller != nil {
		h.poller.refill(fc.id, ref.ID, pr.Number)
	}
	h.askCycle()
	webhttp.Ok(w)
}

func (h *HTTPHandler) handleIssues(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, tail string) {
	if tail == "" {
		h.handleIssueCollection(w, r, fc, ref)
		return
	}
	h.handleIssueAction(w, r, fc, ref, tail)
}

// createIssueBody is an issue as the client asks for one.
type createIssueBody struct {
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// handleIssueCollection serves the repo-level issue endpoints: list (GET) and
// create (POST), which answers the new issue.
func (h *HTTPHandler) handleIssueCollection(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef) {
	switch r.Method {
	case http.MethodGet:
		req, err := listRequestOf(r.URL.Query())
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		page, err := fc.core.ListIssues(r.Context(), ref, req.opts...)
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		webhttp.WriteJSON(w, issueListWire(&page))
	case http.MethodPost:
		var params createIssueBody
		webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		created, err := fc.core.CreateIssue(r.Context(), ref, forgeapi.NewIssue{
			Title: params.Title, Body: params.Body, Labels: params.Labels,
		})
		if err != nil {
			writeOpsError(w, r, err)
			return
		}
		h.mutated(fc, ref)
		webhttp.WriteJSON(w, issueWire(&created))
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleIssueAction serves the per-issue action endpoints: close, which answers
// the issue as the forge left it.
func (h *HTTPHandler) handleIssueAction(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, tail string) {
	numStr, op, _ := splitFirst(tail)
	number, err := strconv.Atoi(numStr)
	if err != nil {
		httpreply.BadRequest(w, "invalid issue number")
		return
	}
	if op != stateClose {
		httpreply.NotFound(w, "unknown issue action")
		return
	}
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	closed, err := fc.core.CloseIssue(r.Context(), ref, forgeapi.IssueRef{Number: number})
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	h.mutated(fc, ref)
	webhttp.WriteJSON(w, issueWire(&closed))
}

// handleChecks answers the folded CI verdict of `?ref=`, a branch or a SHA. The
// library refuses an empty or malformed ref as ref_invalid before any request.
func handleChecks(w http.ResponseWriter, r *http.Request, core forgeapi.Checks, ref forgeapi.RepoRef) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	checks, err := core.CommitStatus(r.Context(), ref, r.URL.Query().Get("ref"))
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, commitChecksWire(&checks))
}

// handleReleases serves the release list (GET) and the cut (POST), which
// answers the new release. Releases is an optional role, so a client without it
// answers ErrNotSupported.
func (h *HTTPHandler) handleReleases(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
		return
	}
	rel, ok := fc.core.(forgeapi.Releases)
	if !ok {
		writeOpsError(w, r, ErrNotSupported)
		return
	}
	if r.Method == http.MethodPost {
		h.createRelease(w, r, fc, rel, ref)
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
	webhttp.WriteJSON(w, releaseListWire(&page))
}

// createReleaseBody is a release as the client asks for one.
type createReleaseBody struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name,omitempty"`
	Body       string `json:"body,omitempty"`
	Target     string `json:"target,omitempty"` // commit SHA or branch
	Draft      bool   `json:"draft,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

func (h *HTTPHandler) createRelease(w http.ResponseWriter, r *http.Request, fc forgeClient, rel forgeapi.Releases, ref forgeapi.RepoRef) {
	var params createReleaseBody
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
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
	h.mutated(fc, ref)
	webhttp.WriteJSON(w, releaseWire(&created))
}

// handleAffordances answers what the repository allows. A mutation of the
// repository evicts the cached answer with its lists.
func (h *HTTPHandler) handleAffordances(w http.ResponseWriter, r *http.Request, fc forgeClient, ref forgeapi.RepoRef, force bool) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	aff, err := h.manager.repoAffordances(r.Context(), fc, ref, force)
	if err != nil {
		writeOpsError(w, r, err)
		return
	}
	webhttp.WriteJSON(w, aff)
}

// handleLabels serves the label list. Labels is an optional role, so a client
// without it answers ErrNotSupported.
func handleLabels(w http.ResponseWriter, r *http.Request, core forgeapi.Core, ref forgeapi.RepoRef) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	lab, ok := core.(forgeapi.Labels)
	if !ok {
		writeOpsError(w, r, ErrNotSupported)
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
	webhttp.WriteJSON(w, labelListWire(&page))
}
