package forges

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/forgeapi/gitcred"
	"github.com/cplieger/marotte/internal/systembin"
)

// HelperCommand is the first argument that runs the binary as git's
// credential helper instead of the server.
const HelperCommand = "git-credential"

const (
	helperServeBudget = 10 * time.Second
	gitConfigBudget   = 10 * time.Second
)

// RunCredentialHelper answers one git credential-helper invocation, args being
// everything after [HelperCommand]: --config-dir <dir> and git's action. It
// answers from the credential store under dir for the active slot of every
// connection the records file holds usable, and never refreshes a token. A
// store or records file that cannot be read writes one line to diag and exits
// 0, so git's later helpers and prompting still run. It returns the process
// exit code.
func RunCredentialHelper(ctx context.Context, args []string, in io.Reader, out, diag io.Writer) int {
	fs := flag.NewFlagSet(HelperCommand, flag.ContinueOnError)
	fs.SetOutput(diag)
	configDir := fs.String("config-dir", "", "the Marotte config directory holding the forge credential store")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configDir == "" || fs.NArg() != 1 {
		_, _ = fmt.Fprintf(diag, "usage: marotte %s --config-dir <dir> <get|store|erase>\n", HelperCommand)
		return 2
	}
	dir := filepath.Join(*configDir, credentialStoreDir)
	// No store directory means no connection owns any origin; opening it would
	// create the directory from a read.
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return 0
	}
	keys, err := activeKeys(*configDir)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "marotte git credential helper: the forge connection records %s cannot be read (%s), so no credential was handed to git\n",
			filepath.Join(*configDir, connectionsFileName), oneLine(err.Error()))
		return 0
	}
	// The decline line already reaches diag; a logger on stderr would print it twice.
	quiet := forgeapi.WithLogger(slog.New(slog.DiscardHandler))
	store, err := creds.OpenFileStore(dir, quiet)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "marotte git credential helper: the forge credential store %s cannot be used (%s), so no credential was handed to git\n",
			dir, oneLine(err.Error()))
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, helperServeBudget)
	defer cancel()
	listed := func() ([]string, error) { return keys, nil }
	if err := gitcred.NewFor(store, listed, quiet).Serve(ctx, fs.Arg(0), in, out, diag); err != nil {
		_, _ = fmt.Fprintf(diag, "marotte git credential helper: %s\n", oneLine(err.Error()))
		return 1
	}
	return 0
}

// activeKeys is the active slot key of every connection the records file holds
// with no refusal, read without the server's mode verdict: the helper process
// only reads, and git keeps working through a mode problem the server reports.
// A missing file answers none.
func activeKeys(configDir string) ([]string, error) {
	recs, err := newRecordsFile(configDir).read()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(recs))
	for i := range recs {
		if recs[i].Standing == standingUsable {
			keys = append(keys, slotKey(recs[i].ID, recs[i].Slot))
		}
	}
	return keys, nil
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// resolvedExecutable is the running binary's path with symlinks resolved, the
// path git is told to run.
func resolvedExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// helperValue is the credential.<url>.helper value that runs bin as the helper for configDir. Git
// runs a value through the shell only when it passes is_absolute_path, so the leading slash stays
// outside the quotes: a value starting with a quote is taken as a helper name and run as git
// credential-'<path>', which never reaches bin.
func helperValue(bin, configDir string) (string, error) {
	if !filepath.IsAbs(bin) {
		return "", fmt.Errorf("forges: helper binary path %q is not absolute", bin)
	}
	dir, err := filepath.Abs(configDir)
	if err != nil {
		return "", err
	}
	return "/" + shellQuote(bin[1:]) + helperArgs(dir), nil
}

func helperArgs(absConfigDir string) string {
	return " " + HelperCommand + " --config-dir " + shellQuote(absConfigDir)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ownsHelperValue reports whether value is a helper this config directory's
// Marotte registered, from any binary location.
func ownsHelperValue(value, configDir string) bool {
	dir, err := filepath.Abs(configDir)
	if err != nil {
		return false
	}
	return strings.HasPrefix(value, "/") && strings.HasSuffix(value, helperArgs(dir))
}

// reconcileGit makes git's credential helper registrations follow the usable
// connections: for each one's origin "" then this binary's helper after any
// foreign entries, and for every other origin no value this config directory
// owns. It does nothing while persistence is unusable, when no row's usability
// is known and a reconcile would strip every helper. A git config failure is
// logged and retried by the next keeper pass, and is silent once ctx has
// ended, when the caller left rather than git failing.
func (m *Manager) reconcileGit(ctx context.Context) {
	// The origins are read under gitMu: a reconcile that waited holding an
	// older read would undo the reconcile of the change that superseded it.
	m.gitMu.Lock()
	defer m.gitMu.Unlock()
	wanted, healthy := m.helperOrigins()
	if !healthy {
		return
	}
	have, err := registeredHelpers(ctx)
	if err != nil {
		warnGit(ctx, "forges: git credential helpers not read", "error", err)
		return
	}
	want, err := m.currentHelperValue()
	if err != nil {
		slog.Warn("forges: cannot build the git credential helper value", "error", err)
		return
	}
	owned := func(v string) bool { return ownsHelperValue(v, m.configDir) }
	for url := range wanted {
		if err := reconcileHelper(ctx, url, have[url], want, owned); err != nil {
			warnGit(ctx, "forges: git credential helper not registered", "origin", url, "error", err)
		}
	}
	for url, values := range have {
		if wanted[url] || !slices.ContainsFunc(values, owned) {
			continue
		}
		if err := reconcileHelper(ctx, url, values, "", owned); err != nil {
			warnGit(ctx, "forges: git credential helper not removed", "origin", url, "error", err)
		}
	}
}

// warnGit logs a git config failure unless ctx has ended: exec refuses or
// kills a command whose ctx ended, so that failure names no fault in git.
func warnGit(ctx context.Context, msg string, args ...any) {
	if ctx.Err() == nil {
		slog.Warn(msg, args...)
	}
}

// helperOrigins is the web origin of every usable connection, and whether
// persistence is usable, read under one hold of mu.
func (m *Manager) helperOrigins() (wanted map[string]bool, healthy bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	wanted = map[string]bool{}
	for _, c := range m.conns {
		if m.usable(c) {
			wanted[c.spec.webBase()] = true
		}
	}
	return wanted, m.health == ""
}

func (m *Manager) currentHelperValue() (string, error) {
	bin, err := m.executable()
	if err != nil {
		return "", err
	}
	return helperValue(bin, m.configDir)
}

// Git consults helpers in order and an empty value resets the list, so a connected origin gets ""
// then want after whatever precedes it; every owned value goes with the empty value before it, and
// want empty removes the pair.
func reconcileHelper(ctx context.Context, url string, have []string, want string, owned func(string) bool) error {
	key := "credential." + url + ".helper"
	next := withoutOwned(have, owned)
	if want != "" {
		next = append(next, "", want)
	}
	if slices.Equal(have, next) {
		return nil
	}
	return writeHelperValues(ctx, key, len(have) > 0, next)
}

// withoutOwned drops every owned value and the empty value directly before it.
func withoutOwned(have []string, owned func(string) bool) []string {
	next := make([]string, 0, len(have)+2)
	for _, v := range have {
		if !owned(v) {
			next = append(next, v)
			continue
		}
		if n := len(next); n > 0 && next[n-1] == "" {
			next = next[:n-1]
		}
	}
	return next
}

func writeHelperValues(ctx context.Context, key string, exists bool, values []string) error {
	if exists {
		if _, err := runGitConfig(ctx, "--unset-all", key); err != nil {
			return err
		}
	}
	for _, v := range values {
		if _, err := runGitConfig(ctx, "--add", key, v); err != nil {
			return err
		}
	}
	return nil
}

// registeredHelpers is every credential.<url>.helper value in git's global
// config, by url, in git's order, from one read.
func registeredHelpers(ctx context.Context) (map[string][]string, error) {
	out, err := runGitConfig(ctx, "-z", "--get-regexp", `^credential\..*\.helper$`)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	have := map[string][]string{}
	for entry := range strings.SplitSeq(string(out), "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		rest, prefixed := strings.CutPrefix(key, "credential.")
		url, suffixed := strings.CutSuffix(rest, ".helper")
		if prefixed && suffixed && url != "" {
			have[url] = append(have[url], value)
		}
	}
	return have, nil
}

func runGitConfig(ctx context.Context, args ...string) ([]byte, error) {
	git, ok := systembin.Resolve("git")
	if !ok {
		return nil, errors.New("forges: git is not installed in a system directory")
	}
	ctx, cancel := context.WithTimeout(ctx, gitConfigBudget)
	defer cancel()
	var stderr bytes.Buffer
	//nolint:gosec,nolintlint // G702: argv[0] is an absolute path from a fixed system-directory set that reads no environment and the subcommand is always config --global; the key's origin is a validated scheme and host:port (connectionFields.record, validSpec) and every argv element reaches execve as a separate token with no shell, where git itself refuses a malformed key
	cmd := exec.CommandContext(ctx, git, append([]string{"config", "--global"}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git config %s: %w: %s", args[0], err, oneLine(strings.TrimSpace(stderr.String())))
	}
	return out, nil
}
