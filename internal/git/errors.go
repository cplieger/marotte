package git

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// errorKind is a machine-readable discriminator for git handler errors.
// Clients switch on the "error" field; the "detail" field carries variable
// context (e.g. branch name).
type errorKind string

// kindNoStaged and the following constants define the ErrorKind values for git handler errors.
const (
	kindNoStaged         errorKind = "no_staged_changes"
	kindNoChanges        errorKind = "no_changes"
	kindGenerationFailed errorKind = "generation_failed"
	kindShowFailed       errorKind = "show_failed"
	// kindNotInRepo means no discovered repository owns the path, so it has no committed revision:
	// not a failure, and a client showing an all-add diff is correct. Kept apart from
	// kindShowFailed so a real git failure does not render as a new file.
	kindNotInRepo errorKind = "not_in_repo"
)

func writeGitError(w http.ResponseWriter, kind errorKind, detail string) {
	resp := httpreply.ErrorJSON(string(kind))
	if detail != "" {
		resp["detail"] = detail
	}
	webhttp.WriteJSON(w, resp)
}

// --- git show error classification ---

// errPathNotInRef indicates the requested path does not exist at the
// given ref (new file, deleted file, or invalid object name). Callers
// should surface this as empty content rather than a hard error.
var errPathNotInRef = errors.New("path not found at ref")

// errUnsafeRepoPath means a destructive repo operation was REFUSED because the name does not
// resolve to a directory inside the workspace (a symlinked or non-directory component, or an
// escape). Distinct from a disk failure: a refusal is the operator's to fix.
var errUnsafeRepoPath = errors.New("repo path is not safe to unlink")

// errBlobTooLarge reports a blob over the show cap.
var errBlobTooLarge = errors.New("blob over the show cap")

// gitShowCmd runs `git show <ref>:<path>` and returns the blob's exact bytes, reading at most
// maxBytes+1 of stdout so an oversized blob costs no more than the cap; over it the command is
// cancelled and errBlobTooLarge returned. stderr is returned apart for the log. Exit 128 on a
// validated ref is errPathNotInRef, except when the directory is not a git repo at all.
func gitShowCmd(ctx context.Context, dir, ref, path string, maxBytes int64) (blob []byte, gitStderr string, err error) {
	if _, ok := resolveGitBinary(); !ok {
		return nil, "", errGitUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// --no-textconv pins the raw-blob default (git 2.55.0; TestTextconv_FixtureIsArmed), so this
	// call never runs a repo-supplied diff.<driver>.textconv. Per call site: plumbing subcommands
	// reject the flag.
	cmd := gitExec(ctx, dir, "show", "--no-textconv", ref+":"+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if startErr := cmd.Start(); startErr != nil {
		return nil, "", startErr
	}
	blob, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if int64(len(blob)) > maxBytes {
		cancel()
		_ = cmd.Wait()
		return nil, "", errBlobTooLarge
	}
	waitErr := cmd.Wait()
	errText := strings.TrimSpace(stderr.String())
	if waitErr == nil {
		return blob, errText, readErr
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok && exitErr.ExitCode() == 128 {
		// "not a git repository" is a repo-level failure, not a
		// path-not-found. Let it fall through as a generic error.
		if strings.Contains(errText, "not a git repository") {
			return nil, errText, waitErr
		}
		return nil, "", errPathNotInRef
	}
	return nil, errText, waitErr
}
