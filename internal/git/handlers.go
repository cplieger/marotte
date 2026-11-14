package git

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/route"
	"golang.org/x/sync/singleflight"
)

// Option configures a Handler at construction time.
type Option func(*Handler)

// Handler implements git HTTP endpoints (non-AI operations).
type Handler struct {
	fetchFlight singleflight.Group
	repoFlight  singleflight.Group
	pullFlight  singleflight.Group
	// lifetime is the status scans' own, ended only by Shutdown; scanMu orders each scan's
	// scans.Go before Shutdown's cancel, so none is added once Wait may have begun.
	lifetime    context.Context
	end         context.CancelFunc
	statusCache statusCache
	workDir     string
	scans       sync.WaitGroup
	timeouts    gitTimeouts
	// showMax caps the blob /api/git/show serves.
	showMax int64
	scanMu  sync.Mutex
}

// defaultShowMax is the show cap when no WithShowMax is given; the composition root passes
// the viewer's own cap so the two cannot drift.
const defaultShowMax = 2 << 20

// NewHandler returns a Handler scoped to workDir. Shutdown ends the status scans it runs behind
// its answers.
func NewHandler(workDir string, opts ...Option) *Handler {
	lifetime, end := context.WithCancel(context.Background())
	h := &Handler{workDir: workDir, timeouts: defaultTimeouts(), showMax: defaultShowMax, lifetime: lifetime, end: end}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Shutdown cancels the status scans in flight, refuses any later one, and returns once those in
// flight have, or with ctx's error when ctx ends first. It may be called more than once.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.scanMu.Lock()
	h.end()
	h.scanMu.Unlock()
	done := make(chan struct{})
	go func() {
		h.scans.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("git: status scans still running: %w", ctx.Err())
	}
}

// WithShowMax caps the blob /api/git/show serves at n bytes; a larger revision answers 413.
// Non-positive keeps the default.
func WithShowMax(n int64) Option {
	return func(h *Handler) {
		if n > 0 {
			h.showMax = n
		}
	}
}

// routeMux is what a route registration needs: the server's registrar, or a test's mux. An alias, so
// RegisterRoutes has the server's routeHandler signature.
type routeMux = interface{ Bind(route.Binding) }

// RegisterRoutes binds every /api/git/* route.
func (h *Handler) RegisterRoutes(m routeMux) {
	get := func(p string, f http.HandlerFunc, mt route.MethodTypes) { m.Bind(route.Methods(p, f, mt)) }
	repo := reflect.TypeFor[RepoQuery]()
	get("/api/git/repos", h.handleRepos, route.Typed[struct{}, RepoNames](http.MethodGet))
	get("/api/git/status", h.handleStatus, route.Typed[struct{}, Status](http.MethodGet).WithQuery(reflect.TypeFor[StatusQuery]()))
	get("/api/git/status-all", h.handleStatusAll, route.Typed[struct{}, StatusAll](http.MethodGet).WithQuery(reflect.TypeFor[StatusAllQuery]()))
	get("/api/git/show", h.handleShow, route.Typed[struct{}, ShowResponse](http.MethodGet).WithQuery(reflect.TypeFor[ShowQuery]()))
	get("/api/git/log", h.handleLog, route.Typed[struct{}, Log](http.MethodGet).WithQuery(repo))
	get("/api/git/branch-commits", h.handleBranchCommits,
		route.Typed[struct{}, BranchCommits](http.MethodGet).WithQuery(reflect.TypeFor[BranchCommitsQuery]()))
	get("/api/git/branches", h.handleBranches, route.Typed[struct{}, Branches](http.MethodGet).WithQuery(repo))
	post := func(p string, f http.HandlerFunc, mt route.MethodTypes) { m.Bind(route.Methods(p, f, mt)) }
	post("/api/git/stage", h.handleStage, route.Typed[FilesRequest, ChangeResult](http.MethodPost))
	post("/api/git/unstage", h.handleUnstage, route.Typed[FilesRequest, ChangeResult](http.MethodPost))
	post("/api/git/discard", h.handleDiscard, route.Typed[FilesRequest, ChangeResult](http.MethodPost))
	post("/api/git/commit", h.handleCommit, route.Typed[CommitRequest, CommandOutput](http.MethodPost))
	post("/api/git/push", h.handlePush, route.Typed[RepoRequest, CommandOutput](http.MethodPost))
	post("/api/git/pull", h.handlePull, route.Typed[RepoRequest, CommandOutput](http.MethodPost))
	post("/api/git/pull-all", h.handlePullAll, route.Typed[struct{}, PullAll](http.MethodPost))
	// An NDJSON stream of progress lines, then the outcome line.
	m.Bind(route.Binding{
		Pattern: "/api/git/clone", Handler: http.HandlerFunc(h.handleClone), Kind: route.KindRaw,
		Methods: []route.MethodTypes{{Method: http.MethodPost, Request: reflect.TypeFor[CloneRequest]()}},
	})
	post("/api/git/checkout", h.handleCheckout, route.Typed[CheckoutRequest, CommandOutput](http.MethodPost))
	post("/api/git/stash", h.handleStash, route.Typed[RepoRequest, CommandOutput](http.MethodPost))
	post("/api/git/stash-pop", h.handleStashPop, route.Typed[RepoRequest, CommandOutput](http.MethodPost))
	post("/api/git/remove", h.handleRemove, route.Typed[RepoRequest, ChangeResult](http.MethodPost))
	post("/api/git/reclone", h.handleReclone, route.Typed[RepoRequest, CommandOutput](http.MethodPost))
	post("/api/git/pr-fetch", h.handlePRFetch, route.Typed[PRFetchRequest, CommandOutput](http.MethodPost))
}

// RepoQuery is the query of the reads that address one repo; empty is the workspace root.
type RepoQuery struct {
	Repo string `json:"repo,omitempty"`
}

// StatusQuery is GET /api/git/status' query; any Quick skips the fetch.
type StatusQuery struct {
	Repo  string `json:"repo,omitempty"`
	Quick string `json:"quick,omitempty"`
}

// StatusAllQuery is GET /api/git/status-all's query: Fetch "1" fetches first, Paths scopes the scan.
type StatusAllQuery struct {
	Fetch string `json:"fetch,omitempty"`
	Paths string `json:"paths,omitempty"`
}

// ShowQuery is GET /api/git/show's query.
type ShowQuery struct {
	Repo string `json:"repo,omitempty"`
	Path string `json:"path"`
	Ref  string `json:"ref,omitempty"`
}

// BranchCommitsQuery is GET /api/git/branch-commits' query.
type BranchCommitsQuery struct {
	Repo string `json:"repo,omitempty"`
	Base string `json:"base"`
	Head string `json:"head,omitempty"`
}

// RepoNames is GET /api/git/repos' body.
type RepoNames struct {
	Repos []string `json:"repos"`
}

// Log is GET /api/git/log's body.
type Log struct {
	Remote          string   `json:"remote"`
	CommitURLPrefix string   `json:"commit_url_prefix"`
	Entries         []string `json:"entries"`
	Behind          int      `json:"behind"`
}

// BranchCommits is GET /api/git/branch-commits' body.
type BranchCommits struct {
	Subjects []string `json:"subjects"`
}

// Branch is one row of GET /api/git/branches.
type Branch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// Branches is GET /api/git/branches' body.
type Branches struct {
	Current  string   `json:"current"`
	Branches []Branch `json:"branches"`
}

// PullAll is POST /api/git/pull-all's body.
type PullAll struct {
	Repos []PullResult `json:"repos"`
}

// FilesRequest is a stage, unstage or discard body.
type FilesRequest struct {
	RepoRequest

	Files []string `json:"files"`
}

// CommitRequest is POST /api/git/commit's body.
type CommitRequest struct {
	RepoRequest

	Message string `json:"message"`
}

// CheckoutRequest is POST /api/git/checkout's body.
type CheckoutRequest struct {
	RepoRequest

	Branch string `json:"branch"`
	Create bool   `json:"create"`
}

// PRFetchRequest is POST /api/git/pr-fetch's body.
type PRFetchRequest struct {
	Repo   string `json:"repo"`
	Head   string `json:"head"`
	Number int    `json:"number"`
}

// CloneRequest is POST /api/git/clone's body.
type CloneRequest struct {
	URL string `json:"url"`
}

// Empty, "." and rejected inputs fall back to workDir. HasDotDot judges the name as written, before
// any join.
func resolveRepoDir(workDir, repo string) string {
	if repo == "" || repo == "." || pathinside.HasDotDot(repo) || filepath.IsAbs(repo) {
		return workDir
	}
	return filepath.Join(workDir, filepath.Clean(repo))
}

// LEXICAL-ONLY on purpose: a symlinked repo addressed by its link name is a feature here, which an
// os.Root would refuse.
func (h *Handler) repoDir(repo string) string {
	return resolveRepoDir(h.workDir, repo)
}

func repoFromQuery(r *http.Request) string { return r.URL.Query().Get("repo") }

// RepoRequest names the repo a write addresses; empty is the workspace root.
type RepoRequest struct {
	Repo string `json:"repo"`
}

// Status is GET /api/git/status' body.
type Status struct {
	Branch   string       `json:"branch"`
	Remote   string       `json:"remote"`
	Files    []StatusFile `json:"files"`
	Ahead    int          `json:"ahead"`
	Behind   int          `json:"behind"`
	Stashes  int          `json:"stashes"`
	IsRepo   bool         `json:"is_repo"`
	HasDirty bool         `json:"has_dirty"`
}

// handlePRFetch fetches a pull request's head ref into a local branch named
// "pr-<number>", or body.Head when set. prRefShape owns which ref shape the
// remote uses.
func (h *Handler) handlePRFetch(w http.ResponseWriter, r *http.Request) {
	var body PRFetchRequest
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	if body.Number <= 0 || body.Number > 10_000_000 {
		slog.Warn("git pr-fetch: invalid PR number rejected", "repo", body.Repo, "number", body.Number)
		httpreply.BadRequest(w, "invalid PR number")
		return
	}
	// body.Head becomes a local branch name, so it needs handleCheckout's ref
	// validation against flag smuggling and ref injection.
	if body.Head != "" && !isValidGitRef(body.Head) {
		slog.Warn("git pr-fetch: invalid head rejected", "repo", body.Repo, "number", body.Number, "head", body.Head)
		httpreply.BadRequest(w, "invalid head name")
		return
	}
	dir := h.repoDir(body.Repo)
	remote, err := gitCmd(r.Context(), dir, subRemote, "get-url", remoteOrigin)
	if err != nil {
		slog.Warn("git pr-fetch: origin lookup failed", "repo", body.Repo, "pr", body.Number, "error", err)
		writeCmdResult(w, remote, err)
		return
	}
	refShape := prRefShape(remote)
	local := "pr-" + strconv.Itoa(body.Number)
	if body.Head != "" {
		local = body.Head
	}
	args := []string{subFetch, remoteOrigin, fmt.Sprintf(refShape, body.Number) + ":" + local}
	slog.Info("git pr-fetch", "repo", body.Repo, "number", body.Number, "local", local)
	out, err := gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, remote, args...)
	writeCmdResult(w, out, err)
}

// prRefShape picks the ref-spec template for the origin URL's host: GitLab uses
// "refs/merge-requests/<n>/head", the GitHub and Gitea family "refs/pull/<n>/head",
// which is also the default for an unknown or unparseable remote. Matching the host
// segment only keeps a GitHub-hosted repo whose path contains "gitlab" on the
// GitHub shape.
func prRefShape(remote string) string {
	host := parseRemoteHost(remote)
	if strings.Contains(host, "gitlab") {
		return "refs/merge-requests/%d/head"
	}
	return "refs/pull/%d/head"
}
