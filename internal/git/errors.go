package git

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

const (
	jsonKeyOutput = httpreply.JSONKeyOutput
	// jsonKeyRepos is the envelope key of every per-repository array this package
	// serves: /api/git/repos, /api/git/status-all and /api/git/pull-all.
	jsonKeyRepos   = "repos"
	refHEAD        = "HEAD"
	remoteOrigin   = "origin"
	msgNotAGitRepo = "not a git repo"
)

// --- git error taxonomy ---

// ErrorKind is a machine-readable discriminator for git handler errors.
// Clients switch on the "error" field; the "detail" field carries variable
// context (e.g. branch name).
type ErrorKind string

// KindNoStaged and the following constants define the ErrorKind values for git handler errors.
const (
	KindNoStaged         ErrorKind = "no_staged_changes"
	KindNoChanges        ErrorKind = "no_changes"
	KindGenerationFailed ErrorKind = "generation_failed"
	KindShowFailed       ErrorKind = "show_failed"
	// KindNotInRepo means no discovered repository owns the path, so it has no committed revision:
	// not a failure, and a client showing an all-add diff is correct. Kept apart from
	// KindShowFailed so a real git failure does not render as a new file.
	KindNotInRepo ErrorKind = "not_in_repo"
)

// writeGitError writes a structured error response with a stable
// machine-readable kind and an optional human-readable detail field.
func writeGitError(w http.ResponseWriter, kind ErrorKind, detail string) {
	resp := httpreply.ErrorJSON(string(kind))
	if detail != "" {
		resp["detail"] = detail
	}
	webhttp.WriteJSON(w, resp)
}

// --- git show error classification ---

// ErrPathNotInRef indicates the requested path does not exist at the
// given ref (new file, deleted file, or invalid object name). Callers
// should surface this as empty content rather than a hard error.
var ErrPathNotInRef = errors.New("path not found at ref")

// ErrUnsafeRepoPath means a destructive repo operation was REFUSED because the name does not
// resolve to a directory inside the workspace (a symlinked or non-directory component, or an
// escape). Distinct from a disk failure: a refusal is the operator's to fix.
var ErrUnsafeRepoPath = errors.New("repo path is not safe to unlink")

// gitShowCmd runs `git show <ref>:<path>` and classifies the error: exit 128 on a validated ref is
// ErrPathNotInRef, except when the directory is not a git repo at all.
func gitShowCmd(ctx context.Context, dir, ref, path string) (string, error) {
	// --no-textconv pins the raw-blob default (git 2.55.0; TestTextconv_FixtureIsArmed), so this
	// call never runs a repo-supplied diff.<driver>.textconv. Per call site: plumbing subcommands
	// reject the flag.
	out, err := gitCmd(ctx, dir, "show", "--no-textconv", ref+":"+path)
	if err == nil {
		return out, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 128 {
		// "not a git repository" is a repo-level failure, not a
		// path-not-found. Let it fall through as a generic error.
		if strings.Contains(out, "not a git repository") {
			return out, err
		}
		return "", ErrPathNotInRef
	}
	return out, err
}
