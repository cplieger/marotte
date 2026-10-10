package git

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
	"github.com/cplieger/webhttp/v3"
)

// utilityPrompter is the AI text generation this handler needs: one round trip taking a prompt and
// an effort level and returning text.
type utilityPrompter interface {
	UtilityPrompt(ctx context.Context, prompt string, effort marotte.EffortLevel) (string, error)
}

// AIHandler registers the AI-backed git endpoints (commit-message, pr-description), apart from
// Handler because they need an AI bridge and little git.
type AIHandler struct {
	prompter utilityPrompter
	workDir  string
}

// NewAIHandler returns an AIHandler. The prompter must be non-nil.
func NewAIHandler(workDir string, prompter utilityPrompter) *AIHandler {
	return &AIHandler{
		prompter: prompter,
		workDir:  workDir,
	}
}

// RegisterRoutes registers the AI-backed git endpoints.
func (a *AIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/git/commit-message", a.handleCommitMessage)
	mux.HandleFunc("/api/git/pr-description", a.handlePRDescription)
	mux.HandleFunc("/api/git/branch-name", a.handleBranchName)
}

// repoDir resolves a client-supplied repo name against workDir (same
// logic as Handler.repoDir).
func (a *AIHandler) repoDir(repo string) string {
	return resolveRepoDir(a.workDir, repo)
}

func getRecentCommits(ctx context.Context, dir string, n int) string {
	out, err := gitCmd(ctx, dir, "log", "--oneline", "--no-merges",
		"-n"+strconv.Itoa(n))
	if err != nil || strings.TrimSpace(out) == "" {
		return "No commit history available"
	}
	return strings.TrimSpace(out)
}

const diffTruncatedSuffix = "\n\n[Diff truncated due to size]"

func truncateDiff(diff string, maxBytes int) string {
	if len(diff) > maxBytes {
		return diff[:maxBytes] + diffTruncatedSuffix
	}
	return diff
}

func (a *AIHandler) handleCommitMessage(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	dir := a.repoDir(body.Repo)

	// --no-textconv even with --stat: the flag stops the textconv program from running at all.
	diff, err := gitCmd(r.Context(), dir, "diff", "--no-textconv", "--cached", "--stat")
	if err != nil || strings.TrimSpace(diff) == "" {
		writeGitError(w, kindNoStaged, "")
		return
	}

	// Full diff, capped at 8KB.
	fullDiff, dErr := gitCmd(r.Context(), dir, "diff", "--no-textconv", "--cached")
	if dErr != nil {
		fullDiff = diff
	}
	fullDiff = truncateDiff(fullDiff, 8*1024)

	commitHistory := getRecentCommits(r.Context(), dir, 10)

	prompt := buildCommitPrompt(commitHistory, fullDiff)

	result, err := a.prompter.UtilityPrompt(r.Context(), prompt, marotte.EffortMedium)
	if err != nil {
		slog.Error("commit message generation failed", "error", err)
		writeGitError(w, kindGenerationFailed, err.Error())
		return
	}

	msg := extractCommitMessage(result)
	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: msg})
}

// defaultPRBase is the assumed base branch when a PR-description
// request doesn't supply one.
const defaultPRBase = "main"

func (a *AIHandler) handlePRDescription(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	dir := a.repoDir(body.Repo)

	// Base branch, default main.
	base := defaultPRBase
	if body.Branch != "" {
		if !isValidGitRef(body.Branch) {
			slog.Warn("git pr-description: invalid branch rejected",
				"repo", body.Repo, "branch", body.Branch)
			httpreply.BadRequest(w, "invalid branch name")
			return
		}
		base = body.Branch
	}

	// Diff between current branch and base.
	diff, err := gitCmd(r.Context(), dir, "diff", "--no-textconv", base+"...HEAD")
	if err != nil || strings.TrimSpace(diff) == "" {
		// Fall back to origin/main if local main doesn't exist.
		diff, err = gitCmd(r.Context(), dir, "diff", "--no-textconv", "origin/"+base+"...HEAD")
		if err != nil || strings.TrimSpace(diff) == "" {
			writeGitError(w, kindNoChanges, "against "+base)
			return
		}
	}

	// Cap at 12KB for PR descriptions (larger than commit messages).
	diff = truncateDiff(diff, 12*1024)

	log, err := gitCmd(r.Context(), dir, "log", "--oneline", base+"..HEAD")
	if err != nil || strings.TrimSpace(log) == "" {
		if fallbackLog, fallbackErr := gitCmd(r.Context(), dir, "log", "--oneline", "origin/"+base+"..HEAD"); fallbackErr == nil {
			log = fallbackLog
		}
	}

	prompt := buildPRPrompt(log, diff)

	result, err := a.prompter.UtilityPrompt(r.Context(), prompt, marotte.EffortMedium)
	if err != nil {
		slog.Error("PR description generation failed", "error", err)
		writeGitError(w, kindGenerationFailed, err.Error())
		return
	}

	result = strings.TrimSpace(result)
	result = modeltext.StripCodeFence(result)
	result = strings.TrimSpace(result)

	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: result})
}

// handleBranchName suggests a branch name for the repo's work in progress: from uncommitted changes
// when any exist, else the most recent commits; existing branch names feed the prompt for style and
// collisions.
func (a *AIHandler) handleBranchName(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	dir := a.repoDir(body.Repo)

	workContext := uncommittedContext(r.Context(), dir)
	if workContext == "" {
		// Nothing uncommitted: fall back to recent commits.
		commits := getRecentCommits(r.Context(), dir, 5)
		if commits == "No commit history available" {
			writeGitError(w, kindNoChanges, "nothing to name a branch after")
			return
		}
		workContext = "Recent commits:\n" + commits
	}

	branches, err := gitCmd(r.Context(), dir, "branch", "--list", "--format=%(refname:short)")
	if err != nil {
		branches = ""
	}
	prompt := buildBranchPrompt(strings.TrimSpace(branches), workContext)

	result, err := a.prompter.UtilityPrompt(r.Context(), prompt, marotte.EffortLow)
	if err != nil {
		slog.Error("branch name generation failed", "error", err)
		writeGitError(w, kindGenerationFailed, err.Error())
		return
	}
	name := sanitizeBranchName(result)
	if name == "" {
		writeGitError(w, kindGenerationFailed, "model returned no usable name")
		return
	}
	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: name})
}

// Empty when the tree is clean.
func uncommittedContext(ctx context.Context, dir string) string {
	status, err := gitCmd(ctx, dir, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) == "" {
		return ""
	}
	diff, dErr := gitCmd(ctx, dir, "diff", "--no-textconv", "HEAD")
	if dErr != nil {
		diff = ""
	}
	diff = truncateDiff(diff, 6*1024)
	return "Changed files:\n" + strings.TrimSpace(status) + "\n\nDiff:\n" + diff
}
