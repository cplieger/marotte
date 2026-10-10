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
// answers from the credential store under dir and never refreshes a token. A
// store that cannot be opened writes one line to diag and exits 0, so git's
// later helpers and prompting still run. It returns the process exit code.
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
	if err := gitcred.New(store, quiet).Serve(ctx, fs.Arg(0), in, out, diag); err != nil {
		_, _ = fmt.Fprintf(diag, "marotte git credential helper: %s\n", oneLine(err.Error()))
		return 1
	}
	return 0
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

// reconcileHelpers registers the helper the running binary builds for every
// connection record, which heals an upgrade or a relocation, and records the
// value on each record. A record file that did not load is never used.
func (m *Manager) reconcileHelpers(ctx context.Context) {
	recs, err := m.conns.load()
	if err != nil || len(recs) == 0 {
		return
	}
	want, err := m.currentHelperValue()
	if err != nil {
		slog.Warn("forges: cannot build the git credential helper value", "error", err)
		return
	}
	moved := map[string]bool{}
	for i := range recs {
		rec := &recs[i]
		if regErr := reconcileHelper(ctx, rec.webBase(), want, m.ownedBy(rec)); regErr != nil {
			slog.Warn("forges: git credential helper not registered", "connection", rec.ID, "error", regErr)
			continue
		}
		if rec.HelperValue != want {
			moved[rec.ID] = true
		}
	}
	if len(moved) > 0 {
		m.recordHelperValue(ctx, moved, want)
	}
}

// A failure is logged; the next boot's reconcile retries it.
func (m *Manager) registerHelper(ctx context.Context, rec *connectionRecord) {
	want, err := m.currentHelperValue()
	if err == nil {
		err = reconcileHelper(ctx, rec.webBase(), want, m.ownedBy(rec))
	}
	if err != nil {
		slog.Warn("forges: git credential helper not registered", "connection", rec.ID, "error", err)
		return
	}
	if rec.HelperValue != want {
		m.recordHelperValue(ctx, map[string]bool{rec.ID: true}, want)
	}
}

func (m *Manager) unregisterHelper(ctx context.Context, rec *connectionRecord) {
	if err := reconcileHelper(ctx, rec.webBase(), "", m.ownedBy(rec)); err != nil {
		slog.Warn("forges: git credential helper not removed", "connection", rec.ID, "error", err)
	}
}

func (m *Manager) currentHelperValue() (string, error) {
	bin, err := m.executable()
	if err != nil {
		return "", err
	}
	return helperValue(bin, m.configDir)
}

// ownedBy reports a value this Marotte registered for rec: the value recorded
// on the record, or any value naming this config directory.
func (m *Manager) ownedBy(rec *connectionRecord) func(string) bool {
	return func(v string) bool {
		return (rec.HelperValue != "" && v == rec.HelperValue) || ownsHelperValue(v, m.configDir)
	}
}

func (m *Manager) recordHelperValue(ctx context.Context, ids map[string]bool, value string) {
	err := m.conns.update(ctx, func(cur []connectionRecord) []connectionRecord {
		for i := range cur {
			if ids[cur[i].ID] {
				cur[i].HelperValue = value
			}
		}
		return cur
	})
	if err != nil {
		slog.Warn("forges: registered git credential helper not recorded", "error", err)
	}
}

// Git consults helpers in order and an empty value resets the list, so a connected origin gets ""
// then want after whatever precedes it; every owned value goes with the empty value before it, and
// want empty removes the pair.
func reconcileHelper(ctx context.Context, url, want string, owned func(string) bool) error {
	key := "credential." + url + ".helper"
	have, err := gitHelperValues(ctx, key)
	if err != nil {
		return err
	}
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

func gitHelperValues(ctx context.Context, key string) ([]string, error) {
	out, err := runGitConfig(ctx, "-z", "--get-all", key)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := strings.Split(string(out), "\x00")
	return values[:len(values)-1], nil
}

func runGitConfig(ctx context.Context, args ...string) ([]byte, error) {
	git, ok := systembin.Resolve("git")
	if !ok {
		return nil, errors.New("forges: git is not installed in a system directory")
	}
	ctx, cancel := context.WithTimeout(ctx, gitConfigBudget)
	defer cancel()
	var stderr bytes.Buffer
	//nolint:gosec,nolintlint // G702: argv[0] is an absolute path from a fixed system-directory set that reads no environment and the subcommand is always config --global; the key's origin is a validated scheme and host:port (patBody.record) and every argv element reaches execve as a separate token with no shell, where git itself refuses a malformed key
	cmd := exec.CommandContext(ctx, git, append([]string{"config", "--global"}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git config %s: %w: %s", args[0], err, oneLine(strings.TrimSpace(stderr.String())))
	}
	return out, nil
}
