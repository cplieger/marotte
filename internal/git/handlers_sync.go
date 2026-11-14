package git

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

func (h *Handler) handleCommit(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		repoBody

		Message string `json:"message"`
	}
	if !decodePostBody(w, r, &body, "message required") {
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		httpreply.BadRequest(w, "message required")
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git commit", "repo", body.Repo)
	out, err := gitCmd(r.Context(), dir, "commit", "-m", body.Message)
	writeCmdResult(w, out, err)
}

// handlePush runs `git push` with git's defaults: no --force, --set-upstream or --tags. Force-push
// is not exposed (use the shell), and an unconfigured upstream fails cleanly rather than being
// guessed.
func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBodyOptional(w, r, &body) {
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git push", "repo", logsafe.Field(body.Repo))
	out, err := gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, "", "push")
	writeCmdResult(w, out, err)
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBodyOptional(w, r, &body) {
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git pull", "repo", logsafe.Field(body.Repo))
	out, err := gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, "", "pull", "--ff-only")
	writeCmdResult(w, out, err)
}

func (h *Handler) handleStash(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBodyOptional(w, r, &body) {
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git stash", "repo", body.Repo)
	out, err := gitCmd(r.Context(), dir, "stash", "push", "-m", "marotte auto-stash")
	writeCmdResult(w, out, err)
}

func (h *Handler) handleStashPop(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBodyOptional(w, r, &body) {
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git stash-pop", "repo", body.Repo)
	out, err := gitCmd(r.Context(), dir, "stash", "pop")
	writeCmdResult(w, out, err)
}
