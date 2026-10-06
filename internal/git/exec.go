// Hardened git subprocess execution and credential scrubbing, unexported because they are
// implementation detail of the git surface.

package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/systembin"
	"github.com/cplieger/runesafe/v2"
)

// errGitUnavailable is gitCmd's named refusal when the git binary is absent
// from internal/systembin's trusted directories. Distinct from a subcommand
// refusal: that one names a caller's mistake, this one names the image.
var errGitUnavailable = errors.New("git: not available")

// resolveGitBinary is the package's one resolution of the git binary and the func-var seam tests
// stage a fake git through. Required: the systembin pin makes a PATH-prepended fake unreachable, so
// tests would drive the real git against a real remote.
var resolveGitBinary = func() (string, bool) { return systembin.Resolve("git") }

// gitTimeouts holds the git subprocess timeout budgets. There is deliberately no plumbing budget: a
// local-only command is bounded by its caller's context, and every path that detaches from a
// request (statusScanBudget, pullAllBudget, the forge list cache) carries its own WithTimeout.
type gitTimeouts struct {
	// Fetch bounds network read-only operations: fetch --quiet.
	Fetch time.Duration
	// Push bounds network write operations: push, pull.
	Push time.Duration
}

// defaultTimeouts returns the production timeout policy.
func defaultTimeouts() gitTimeouts {
	return gitTimeouts{
		Fetch: 5 * time.Second,
		Push:  60 * time.Second,
	}
}

// --- Credential scrubbing ---

// urlCredPattern matches `scheme://user:pwd@host` or `scheme://token@host`
// embedded in error strings. Applies to any RFC 3986 scheme (not
// just http(s)), so ssh and git:// URLs with credential-helper
// rewrites are also scrubbed.
var urlCredPattern = regexp.MustCompile(`(://)[^/]*@`)

// urlQueryTokenPattern matches secret-bearing query parameters
// (?token=, ?access_token=, ?private_token=, ?api_key=, ?apikey=)
// that self-hosted Gitea/Forgejo and GitHub's legacy OAuth app flow
// sometimes emit. The replacement keeps the key name so debug
// context survives.
var urlQueryTokenPattern = regexp.MustCompile(`([?&](?:token|access_token|private_token|api_key|apikey)=)[^&\s]+`)

// authHeaderPattern matches Authorization: Bearer/Token/Basic
// headers echoed back in error bodies that reflect request
// headers. Case-insensitive on the header name only.
var authHeaderPattern = regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|token|basic)\s+)\S+`)

// redactCredentials is UNBOUNDED and NOT single-line, so its only callers are the three helpers
// below, and the order inside each is the substance: a transform before or after the byte-exact
// redactor can defeat it. Redaction stays at the EMIT site because `git remote get-url` output is
// also parsed (commitURLPrefix, prRemoteHost).

// maxClientOutputBytes bounds a multi-line git output block sent to a client.
// Generous: it is a transcript a human reads, and git's own failure messages
// carry the diagnosis in the last lines.
const maxClientOutputBytes = 64 * 1024

// maxRemoteURLBytes bounds a single-line value a human makes a decision from —
// the remote URL in the Sources row. Short, because anything longer is not a
// remote URL.
const maxRemoteURLBytes = 512

// clientOutputTruncated marks a client block the cap cut.
const clientOutputTruncated = "\n[output truncated]"

// logField prepares git output for a slog attribute: redact, then internal/logsafe's single-line
// preset and cap. One pass: the preset replaces unsafe runes with a space, so it cannot build a
// `://`.
func logField(s string) string {
	return logsafe.Field(redactCredentials(s))
}

// clientBlock prepares multi-line git output for a client transcript: redact, sanitize.Output,
// redact AGAIN, cap with a marker. The second pass is required because sanitize.Output DELETES
// hidden runes and can construct a match (`https:/<U+200B>/user:tok@host`). Each helper's pass
// count follows from its sanitizer; do not align them. Multi-line is preserved.
func clientBlock(s string) string {
	out := sanitize.Output(redactCredentials(s))
	out = redactCredentials(out)
	if len(out) <= maxClientOutputBytes {
		return out
	}
	return runesafe.CapBytes(out, maxClientOutputBytes) + clientOutputTruncated
}

// clientLine prepares a single-line value a human makes a decision from — a
// remote URL — for a client: redact, flatten, cap.
//
// One pass, for logField's reason: runesafe's single-line preset replaces rather
// than deletes.
func clientLine(s string) string {
	return runesafe.SanitizeSingleLineBounded(redactCredentials(s), maxRemoteURLBytes)
}

// redactCredentials strips credentials from git output, repeating until chained userinfo
// (`http://a@b@c@host`) stabilises; each match shrinks the string, so the loop is bounded. Every
// pattern matches within one LINE, so every truncation in this package must land on a line boundary
// (cappedBuffer). Called only by the three helpers above.
func redactCredentials(s string) string {
	if s == "" {
		return ""
	}
	for {
		out := urlCredPattern.ReplaceAllString(s, "${1}")
		if out == s {
			break
		}
		s = out
	}
	s = urlQueryTokenPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = authHeaderPattern.ReplaceAllString(s, "${1}[REDACTED]")
	return s
}

// --- Hardened subprocess execution ---

// allowedSubcommands gates the first non-flag argument of every gitExec call; anything else gets a
// command that fails without launching git. Declared at the exec boundary because CodeQL's
// go/command-injection cannot see handler-layer validation. The named constants are argv tokens
// built in more than one place.
const (
	subAdd      = "add"
	subCheckout = "checkout"
	subClean    = "clean"
	subFetch    = "fetch"
	subRemote   = "remote"
	subReset    = "reset"
)

var allowedSubcommands = map[string]struct{}{
	subAdd:         {},
	"branch":       {},
	subCheckout:    {},
	subClean:       {},
	"clone":        {},
	"commit":       {},
	"config":       {},
	"diff":         {},
	subFetch:       {},
	"init":         {},
	"log":          {},
	"ls-remote":    {},
	"merge":        {},
	"pull":         {},
	"push":         {},
	"rebase":       {},
	subRemote:      {},
	subReset:       {},
	"rev-list":     {},
	"rev-parse":    {},
	"show":         {},
	"show-ref":     {},
	"stash":        {},
	"status":       {},
	"submodule":    {},
	"switch":       {},
	"symbolic-ref": {},
	"tag":          {},
	"update-ref":   {},
	"worktree":     {},
}

// firstSubcommand walks args looking for the first token that doesn't
// start with '-' (i.e. the git subcommand). Returns "" if no token is
// found, which means the caller passed only flags — also rejected.
func firstSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			// Skip values for the limited set of -c/-C-style flags we
			// know take a separate argument. For our hardened-cmd usage
			// callers don't pass -c themselves, so this branch is
			// defensive.
			if a == "-c" || a == "-C" {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// gitExec builds a hardened git command: protocol.ext.allow=never and
// core.fsmonitor neutralised with -c, no terminal or askpass prompts, inherited
// GIT_CONFIG_* cleared. User and system gitconfig still load, because the forge
// credential helpers live in ~/.gitconfig. A repo's own filter drivers and hooks
// still run (status runs clean filters), so opening an untrusted repo can execute
// its code. The first non-flag arg must be in allowedSubcommands, else the command
// fails without launching git. Callers supply a context with a timeout.
func gitExec(ctx context.Context, dir string, args ...string) *exec.Cmd {
	if _, ok := allowedSubcommand(args); !ok {
		// /bin/false, no shell: an interpolatable argv would hand a taint path to the boundary this
		// allowlist closes. gitCmd reports the refusal as a real error, so this is defence in
		// depth.
		return refuseExec(ctx, dir)
	}
	// Prepend hardening -c flags. Command-line -c values take priority
	// over any gitconfig setting, so even a user gitconfig with
	// `[protocol "ext"] allow = always` cannot re-enable ext::.
	hardenedArgs := append([]string{
		"-c", "protocol.ext.allow=never",
		// core.fsmonitor names a command git runs on status and diff; cleared centrally because no
		// legitimate value exists here, so any value came from a repo's .git/config.
		"-c", "core.fsmonitor=",
		// Stops git C-quoting non-ASCII paths in the non--z output that reaches the model (commit
		// messages, PR descriptions, branch names). Quoting stays reachable (quotes, newlines,
		// controls), so no parser may drop its quote handling.
		"-c", "core.quotePath=false",
	}, args...)
	// argv[0] is an absolute systembin path, not the bare name: PATH[0] is the toolbelt engine's
	// link directory on the persistent volume, so a planted `git` would run as the server outside
	// Cedar. A miss refuses: one site's fallback voids the pin everywhere.
	gitBin, ok := resolveGitBinary()
	if !ok {
		slog.Error("git binary not found in the trusted system directories; refusing to spawn")
		return refuseExec(ctx, dir)
	}
	// The directive also suppresses the unused-directive check: golangci-lint's gosec integration
	// reports this line nondeterministically. Never start a comment line with the check's name:
	// gocritic's whyNoLint reads it as a directive.
	//nolint:gosec,nolintlint // G702: the subcommand is checked against allowedSubcommands above and argv[0] is an absolute path from a fixed system-directory set that reads no environment; every remaining argv element is a separate token to execve with no shell, and the ref/path-shaped ones are validated at the handler boundary (isValidGitRef, validateFilePath, resolveRepoDir)
	cmd := exec.CommandContext(ctx, gitBin, hardenedArgs...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_PROTOCOL_FROM_USER=0",
		// Clear runtime GIT_CONFIG_* injection, which would override the cmdline hardening; config
		// files on disk still load.
		"GIT_CONFIG_COUNT=",
		"GIT_CONFIG_PARAMETERS=",
	)
	return cmd
}

// gitCmd executes a git subprocess and returns trimmed combined output.
// allowedSubcommand reports the subcommand `args` names and whether it is
// allowlisted. Shared by gitExec (which refuses to launch) and gitCmd (which
// reports WHY), so the two can never disagree about what is permitted.
func allowedSubcommand(args []string) (string, bool) {
	sub := firstSubcommand(args)
	_, ok := allowedSubcommands[sub]
	return sub, ok
}

// refuseExec builds a command that fails without launching git (/bin/false, no shell), for
// gitExec's two refusals: a disallowed subcommand, and a git binary absent from the trusted system
// directories.
func refuseExec(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/false")
	cmd.Dir = dir
	return cmd
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	// Checked here too, because only this layer can return a message: gitExec's refusal is a silent
	// exit 1, which callers rendered as "clean:" naming nothing.
	if sub, ok := allowedSubcommand(args); !ok {
		return "", fmt.Errorf("git: subcommand not allowed: %s", sub)
	}
	// Reported here for the same reason as the subcommand refusal: gitExec's own
	// refusal is a silent exit 1, so without this a caller renders a message
	// naming no cause. No path is echoed — an operator reads the Error line
	// gitExec logs, and the response body says only that git is unavailable.
	if _, ok := resolveGitBinary(); !ok {
		return "", errGitUnavailable
	}
	out, err := gitExec(ctx, dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// splitRemote extracts the host and repository path (no surrounding slash, no ".git") from an https
// or scp-style remote, e.g. git@github.com:foo/bar.git → github.com, foo/bar. One parse for both
// halves so they describe the same remote. ok is false for an unrecognised shape or a host
// sanitizeHost rejects; repoPath may be empty when ok is true.
func splitRemote(raw string) (host, repoPath string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	h, p, scp := parseSCPStyle(raw)
	if !scp {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}
		h, p = u.Hostname(), u.Path
	}
	h = sanitizeHost(h)
	if h == "" {
		return "", "", false
	}
	return h, strings.TrimSuffix(strings.Trim(p, "/"), ".git"), true
}

// parseRemoteHost extracts the host segment from an https or scp-style
// git remote URL. Returns "" for unrecognised shapes.
//
//	https://github.com/foo/bar.git     → github.com
//	git@github.com:foo/bar.git         → github.com
//	ssh://git@gitlab.com/foo/bar.git   → gitlab.com
//
// Rejects ext:: remote-helper prefixes as a defense-in-depth measure.
func parseRemoteHost(raw string) string {
	host, _, _ := splitRemote(raw)
	return host
}

// commitURLPrefix derives the forge web location a commit hash appends to, always ending in "/"
// (GitLab's under "/-/commit/"). "" when no well-formed https location can be derived; the client
// then renders a plain hash rather than a link to the wrong page.
func commitURLPrefix(remote string) string {
	host, repoPath, ok := splitRemote(remote)
	if !ok || repoPath == "" {
		return ""
	}
	// GitLab nests repository pages under "/-/"; classified as prRefShape classifies the same
	// families, so keep the two in step.
	shape := "/commit/"
	if strings.Contains(host, "gitlab") {
		shape = "/-/commit/"
	}
	prefix := "https://" + host + "/" + repoPath + shape
	// Round-trip what goes to a browser: a host url.Parse refuses, or a path with "?" or "#", would
	// point the link elsewhere.
	u, err := url.Parse(prefix)
	if err != nil || u.Scheme != "https" || u.Host != host || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return prefix
}

// cloneDirName derives the directory `git clone <url>` creates, mirroring
// git's own guess_dir_name for the two URL shapes this surface accepts
// (https:// and scp-style git@host:path): the last path component with a
// trailing ".git" removed.
//
// Returns "" when the answer is not one ordinary directory component,
// which is the signal for the caller to let git derive the destination
// itself rather than act on a guess.
func cloneDirName(raw string) string {
	s := strings.TrimSpace(raw)
	// Neither a query nor a fragment is part of a repository path.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if _, p, ok := parseSCPStyle(s); ok {
		s = p
	} else {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Path
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	name := strings.TrimSuffix(path.Base(s), ".git")
	// A traversal component, a nested path, a flag-shaped name, or git's
	// own metadata directory is never acted on here.
	switch name {
	case "", ".", "..", ".git":
		return ""
	}
	if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "-") {
		return ""
	}
	return name
}

// sanitizeHost returns "" if host contains control characters or is empty.
func sanitizeHost(h string) string {
	for _, c := range h {
		if c < 0x20 || c == 0x7f || c == '@' || c == ':' || c == '/' {
			return ""
		}
	}
	return h
}

// parseSCPStyle recognises git's scp-like remote syntax (user@host:path) and returns (host,
// repoPath, true); false for a :// URL, a string without @, or an ext:: prefix. The result is not
// named `path`, which would shadow the package.
func parseSCPStyle(raw string) (host, repoPath string, ok bool) {
	if strings.Contains(raw, "://") {
		return "", "", false
	}
	at := strings.Index(raw, "@")
	if at <= 0 {
		return "", "", false
	}
	user := raw[:at]
	if strings.Contains(user, "::") {
		return "", "", false
	}
	rest := raw[at+1:]
	h, p, found := strings.Cut(rest, ":")
	if !found || h == "" || strings.ContainsAny(h, "/?#") {
		return "", "", false
	}
	return h, p, true
}
