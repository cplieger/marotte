package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/workspace"
	"github.com/cplieger/webhttp/v3"
)

func (h *Handler) handleClone(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !decodePostBody(w, r, &body, "url required") {
		return
	}
	if body.URL == "" {
		httpreply.BadRequest(w, "url required")
		return
	}
	if !isAllowedRemoteScheme(body.URL) {
		slog.Warn("git clone: invalid scheme rejected", "url", logField(body.URL))
		httpreply.BadRequest(w, "only https:// and git@ URLs allowed")
		return
	}
	// `--` keeps the URL a positional argument against git argument-injection CVEs
	// (CVE-2017-1000117, CVE-2018-11235 and variants), on top of the scheme prefix.
	slog.Info("git clone", "url", logField(body.URL))
	// From here the response is NDJSON progress lines while git transfers, then one final
	// {output}/{error} line, so the client measures liveness rather than holding a wall-clock
	// timeout.
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w)
	lastSent := time.Time{}
	progress := func(line string) {
		if time.Since(lastSent) < progressInterval {
			return
		}
		lastSent = time.Now()
		writeCloneStreamLine(w, rc, map[string]string{"progress": clientBlock(line)})
	}
	cloneCtx, cancel := context.WithTimeoutCause(r.Context(), cloneCeiling, errCloneCeiling)
	defer cancel()
	out, err := h.clone(cloneCtx, body.URL, progress)
	if err != nil {
		slog.Error("git clone: failed", "url", logField(body.URL), "error", err, "out", logField(out))
		writeCloneStreamLine(w, rc, map[string]string{"error": clientBlock(cmdFailure(out, err))})
		return
	}
	writeCloneStreamLine(w, rc, map[string]string{jsonKeyOutput: clientBlock(out)})
}

// A var so the handler test does not have to pace a fake git in real time.
var progressInterval = 500 * time.Millisecond

// writeCloneStreamLine writes one NDJSON line of the clone response and
// flushes it, so the client sees each line as it happens rather than one
// buffered body at the end — the flush IS the liveness signal.
func writeCloneStreamLine(w http.ResponseWriter, rc *http.ResponseController, v map[string]string) {
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return
	}
	// A ResponseWriter with no Flusher underneath buffers until the handler
	// returns, which degrades to a single body; nothing to
	// repair from here.
	_ = rc.Flush()
}

// clone runs the clone for handleClone, choosing a plain `git clone` or adoptDestination by what
// sits at the destination, and on failure restores the destination to its pre-clone state, so a
// killed clone leaves no half-written directory behind.
func (h *Handler) clone(ctx context.Context, remote string, onProgress func(string)) (string, error) {
	name := cloneDirName(remote)
	if name == "" {
		// The destination is not predictable from this URL, so let git
		// derive it and report whatever it finds. No cleanup either: the
		// destination is unknown here for the same reason.
		return runTransfer(ctx, h.workDir, onProgress, "clone", "--progress", "--", remote)
	}
	dir := filepath.Join(h.workDir, name)
	if dir == h.workDir {
		// Unreachable while cloneDirName refuses "." and ".."; the workspace
		// root must never become an adoption target.
		return runTransfer(ctx, h.workDir, onProgress, "clone", "--progress", "--", remote)
	}
	switch inspectCloneDest(ctx, dir) {
	case destRepo:
		return "", fmt.Errorf("%s already exists and is a git repository. Delete it or use re-clone to replace it", name)
	case destOccupied:
		slog.Info("git clone: adopting an existing directory", "dir", name)
		out, err := adoptDestination(ctx, dir, remote, onProgress)
		if err != nil {
			// The directory held the user's content before adoption; only the
			// .git that `git init` created is ours to take back.
			h.discardCloneDebris(dir, name, false)
		}
		return out, err
	case destEmpty:
		out, err := runTransfer(ctx, h.workDir, onProgress, "clone", "--progress", "--", remote)
		if err != nil {
			// The user created this directory, so it stays; everything git
			// put inside a previously-empty one is debris.
			h.discardCloneDebris(dir, name, true)
		}
		return out, err
	}
	// destAbsent: git created the directory, so a failure removes it whole; also the safe default
	// for a destState added later.
	out, err := runTransfer(ctx, h.workDir, onProgress, "clone", "--progress", "--", remote)
	if err != nil {
		if rmErr := h.removeRepoDir(dir); rmErr != nil {
			slog.Warn("git clone: failed to remove the partial destination", "dir", name, "error", rmErr)
		}
	}
	return out, err
}

// discardCloneDebris removes what a failed clone or adoption left inside a destination that existed
// before: sweepAll removes every entry (it was empty, so all is git's), otherwise only `git init`'s
// .git (the user's content predates it).
func (h *Handler) discardCloneDebris(dir, name string, sweepAll bool) {
	root, err := os.OpenRoot(h.workDir)
	if err != nil {
		slog.Warn("git clone: cleanup could not open the workspace root", "dir", name, "error", err)
		return
	}
	defer func() { _ = root.Close() }()
	rel, err := workspace.RelPath(h.workDir, dir)
	if err != nil {
		slog.Warn("git clone: cleanup refused the destination path", "dir", name, "error", err)
		return
	}
	// Opening the destination through the root refuses a symlink component,
	// so the removals below cannot be redirected outside the workspace.
	sub, err := root.OpenRoot(rel)
	if err != nil {
		// Gone already (git cleaned up after itself) is the goal state.
		return
	}
	defer func() { _ = sub.Close() }()
	if !sweepAll {
		if rmErr := sub.RemoveAll(".git"); rmErr != nil {
			slog.Warn("git clone: failed to remove the adopted .git", "dir", name, "error", rmErr)
		}
		return
	}
	f, err := sub.Open(".")
	if err != nil {
		slog.Warn("git clone: cleanup could not list the destination", "dir", name, "error", err)
		return
	}
	entries, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		slog.Warn("git clone: cleanup could not list the destination", "dir", name, "error", err)
		return
	}
	for _, entry := range entries {
		if err := sub.RemoveAll(entry); err != nil {
			slog.Warn("git clone: failed to remove clone debris", "dir", name, "entry", entry, "error", err)
		}
	}
}

type destState int

const (
	// destAbsent is nothing at the name, or something that is not a
	// directory. Both are git's to report.
	destAbsent destState = iota
	destEmpty
	destRepo
	destOccupied
)

// A symlink reads as destAbsent deliberately: adopting one would write through it to a target the
// caller never named, so it stays git's error to report.
func inspectCloneDest(ctx context.Context, dir string) destState {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return destAbsent
	}
	if IsRepo(ctx, dir) {
		return destRepo
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		// An unreadable directory falls through to plain clone, whose
		// failure names the real reason.
		return destEmpty
	}
	return destOccupied
}

// adoptDestination clones remote into an existing non-empty directory, which plain `git clone`
// refuses: `git init`, add origin, fetch, then check out origin/HEAD. Needed because code
// intelligence writes <workspace>/.kiro/settings/lsp.json into a directory before its repo is
// cloned.
func adoptDestination(ctx context.Context, dir, remote string, onProgress func(string)) (string, error) {
	var combined strings.Builder
	run := func(args ...string) (string, error) {
		out, err := gitCmd(ctx, dir, args...)
		if out != "" {
			combined.WriteString(out)
			combined.WriteString("\n")
		}
		return out, err
	}
	report := func() string { return strings.TrimSpace(combined.String()) }

	for _, args := range [][]string{
		{"init", "--quiet"},
		// `--` barrier for the same reason handleClone passes one.
		{subRemote, "add", remoteOrigin, "--", remote},
	} {
		if _, err := run(args...); err != nil {
			return report(), err
		}
	}
	// The fetch is the adoption's transfer leg — the only step that moves
	// data over the network — so it runs under the same progress-driven
	// liveness the plain clone gets (--progress instead of --quiet).
	if out, err := runTransfer(ctx, dir, onProgress, subFetch, "--progress", "--no-tags", remoteOrigin); err != nil {
		if out != "" {
			combined.WriteString(out)
			combined.WriteString("\n")
		}
		return report(), err
	}
	// A remote with no commits has no origin/HEAD; the repository is initialised and tracked with
	// nothing to check out.
	if _, err := run(subRemote, "set-head", remoteOrigin, "--auto"); err != nil {
		return report(), nil
	}
	head, err := run("symbolic-ref", "--short", "refs/remotes/"+remoteOrigin+"/HEAD")
	if err != nil {
		return report(), nil
	}
	// --short answers "origin/<branch>"; the local branch is what remains.
	branch := strings.TrimPrefix(strings.TrimSpace(head), remoteOrigin+"/")
	if !isValidGitRef(branch) {
		return report(), fmt.Errorf("remote HEAD is not a usable branch name: %q", branch)
	}
	// A tracking branch by DWIM, exactly what clone would have left behind.
	if _, err := run(subCheckout, branch); err != nil {
		return report(), err
	}
	return report(), nil
}

// handleReclone deletes the local copy of `repo` and re-clones it from its configured origin,
// one-click recovery from local-only mess. Rejects an empty or "." repo: the workspace root is not
// necessarily a repo.
func (h *Handler) handleReclone(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "repo required") {
		return
	}
	if body.Repo == "" || body.Repo == "." {
		httpreply.BadRequest(w, "re-clone requires a named repo (cannot target workspace root)")
		return
	}
	dir := h.repoDir(body.Repo)
	if dir == h.workDir {
		httpreply.BadRequest(w, "cannot re-clone workspace root")
		return
	}
	if !IsRepo(r.Context(), dir) {
		httpreply.BadRequest(w, msgNotAGitRepo)
		return
	}
	remote, err := gitCmd(r.Context(), dir, subRemote, "get-url", remoteOrigin)
	if err != nil || remote == "" {
		slog.Warn("git reclone: origin lookup failed", "repo", body.Repo, "error", err)
		webhttp.WriteJSON(w, httpreply.ErrorJSON("no origin remote"))
		return
	}
	// The origin URL came from git config and may name another transport, so handleClone's scheme
	// allowlist is re-applied BEFORE the delete.
	if !isAllowedRemoteScheme(remote) {
		webhttp.WriteJSON(w, httpreply.ErrorJSON("origin has unsupported scheme for re-clone"))
		return
	}
	slog.Info("git reclone starting", "repo", body.Repo)
	// Delete after resolving the URL, through the pinned parent (see removeRepoDir).
	if rmErr := h.removeRepoDir(dir); rmErr != nil {
		if errors.Is(rmErr, errUnsafeRepoPath) {
			slog.Warn("git reclone: refused", "repo", body.Repo, "error", rmErr)
			httpreply.BadRequest(w, "that repo path is not inside the workspace")
			return
		}
		slog.Error("git reclone: remove failed", "repo", body.Repo, "error", rmErr)
		webhttp.WriteJSON(w, httpreply.ErrorJSON("remove failed"))
		return
	}
	// `--`: a prior malicious clone could have stored a `-flag` origin.
	cmd := gitExec(r.Context(), h.workDir, "clone", "--", remote, filepath.Base(dir))
	out, cErr := cmd.CombinedOutput()
	if cErr != nil {
		slog.Error("git reclone: clone failed", "repo", body.Repo, "error", cErr, "out", logField(strings.TrimSpace(string(out))))
	} else {
		slog.Info("git reclone completed", "repo", body.Repo)
	}
	writeCmdResult(w, strings.TrimSpace(string(out)), cErr)
}

// isAllowedRemoteScheme reports whether url uses a transport scheme
// permitted for clone and re-clone operations: https:// or scp-style
// (git@). Restricted to those two to prevent the UI from accidentally
// driving insecure transports (http://) or remote helpers (ext::).
func isAllowedRemoteScheme(url string) bool {
	return strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "git@")
}
