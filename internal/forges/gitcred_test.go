package forges

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/marotte/internal/systembin"
)

// TestMain runs the test binary as the credential helper when git re-executes
// it, and otherwise points git's global config away from this machine's for the
// whole package: a manager's boot Refresh registers the helper for every record
// it holds.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperCommand {
		os.Exit(RunCredentialHelper(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(runIsolatedFromGitConfig(m))
}

func runIsolatedFromGitConfig(m *testing.M) int {
	home, err := os.MkdirTemp("", "forges-gitconfig-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Setup: temp git home:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(home) }()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "Setup:", k, err)
			return 1
		}
	}
	return m.Run()
}

// isolateGit gives the test its own HOME and global git config, hides the
// system one, and returns the global config's path.
func isolateGit(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, ".gitconfig")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	return cfg
}

func writeGitConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

func helperValuesFor(t *testing.T, url string) []string {
	t.Helper()
	got, err := gitHelperValues(t.Context(), "credential."+url+".helper")
	if err != nil {
		t.Fatalf("read credential.%s.helper: %v", url, err)
	}
	return got
}

func mustHelperValue(t *testing.T, bin, configDir string) string {
	t.Helper()
	v, err := helperValue(bin, configDir)
	if err != nil {
		t.Fatalf("helperValue(%q, %q): %v", bin, configDir, err)
	}
	return v
}

func ownedBy(configDir string) func(string) bool {
	return func(v string) bool { return ownsHelperValue(v, configDir) }
}

// seedCredential saves a static token for one connection in configDir's store.
func seedCredential(t *testing.T, configDir, key string, rec creds.Record) {
	t.Helper()
	store, reason := openCredentialStore(configDir)
	if store == nil {
		t.Fatalf("Setup: open store: %s", reason)
	}
	rec.Kind = forgeapi.CredKindStaticPAT
	rec.Issued = time.Now()
	if err := store.Save(key, rec); err != nil {
		t.Fatalf("Setup: save %s: %v", key, err)
	}
}

func runHelper(t *testing.T, configDir, action, input string) (code int, out, diag string) {
	t.Helper()
	var o, d bytes.Buffer
	code = RunCredentialHelper(t.Context(), []string{"--config-dir", configDir, action}, strings.NewReader(input), &o, &d)
	return code, o.String(), d.String()
}

func TestHelperValue_LeadingSlashOutsideTheQuotes(t *testing.T) {
	bin := "/tmp/it's a dir/marotte"
	cfgDir := "/srv/my config"
	v := mustHelperValue(t, bin, cfgDir)

	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "'") || strings.HasPrefix(v, "!") {
		t.Fatalf("helperValue(%q, %q) = %q, want a value starting with / and never with a quote or !", bin, cfgDir, v)
	}
	sh, ok := systembin.Resolve("sh")
	if !ok {
		t.Skip("no system sh to split the value with")
	}
	out, err := exec.CommandContext(t.Context(), sh, "-c", `set -- `+v+` get; printf '%s\0' "$@"`).Output()
	if err != nil {
		t.Fatalf("sh splitting %q: %v", v, err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	want := []string{bin, HelperCommand, "--config-dir", cfgDir, "get"}
	if !slices.Equal(got, want) {
		t.Errorf("the shell splits helperValue(%q, %q) = %q into %q, want %q", bin, cfgDir, v, got, want)
	}
}

func TestReconcileHelper_AppendsResetThenValueAfterForeignEntries(t *testing.T) {
	gitcfg := isolateGit(t)
	writeGitConfig(t, gitcfg, "[user]\n\tname = someone\n"+
		"[credential \"https://github.com\"]\n\thelper = \n\thelper = !/usr/bin/gh auth git-credential\n")
	cfgDir := t.TempDir()
	want := mustHelperValue(t, "/opt/marotte/marotte", cfgDir)

	if err := reconcileHelper(t.Context(), "https://github.com", want, ownedBy(cfgDir)); err != nil {
		t.Fatalf("reconcileHelper: %v", err)
	}
	got := helperValuesFor(t, "https://github.com")
	wantList := []string{"", "!/usr/bin/gh auth git-credential", "", want}
	if !slices.Equal(got, wantList) {
		t.Errorf("credential.https://github.com.helper = %q, want %q", got, wantList)
	}
	if name, err := runGitConfig(t.Context(), "--get", "user.name"); err != nil || strings.TrimSpace(string(name)) != "someone" {
		t.Errorf("user.name after reconcile = %q, %v; want the foreign key untouched", name, err)
	}
}

func TestReconcileHelper_RewritesWhenTheBinaryMoved(t *testing.T) {
	gitcfg := isolateGit(t)
	cfgDir := t.TempDir()
	old := mustHelperValue(t, "/old place/marotte", cfgDir)
	moved := mustHelperValue(t, "/new/marotte", cfgDir)
	writeGitConfig(t, gitcfg, "[credential \"https://forge.test\"]\n\thelper = \n\thelper = !gh auth git-credential\n")
	if _, err := runGitConfig(t.Context(), "--add", "credential.https://forge.test.helper", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runGitConfig(t.Context(), "--add", "credential.https://forge.test.helper", old); err != nil {
		t.Fatal(err)
	}

	if err := reconcileHelper(t.Context(), "https://forge.test", moved, ownedBy(cfgDir)); err != nil {
		t.Fatalf("reconcileHelper: %v", err)
	}
	got := helperValuesFor(t, "https://forge.test")
	want := []string{"", "!gh auth git-credential", "", moved}
	if !slices.Equal(got, want) {
		t.Errorf("after a relocation credential.https://forge.test.helper = %q, want %q", got, want)
	}
}

func TestReconcileHelper_DisconnectRemovesOnlyItsPair(t *testing.T) {
	isolateGit(t)
	cfgDir := t.TempDir()
	v := mustHelperValue(t, "/opt/marotte/marotte", cfgDir)
	for _, url := range []string{"https://forge.test", "https://other.test"} {
		for _, val := range []string{"", "!gh auth git-credential", "", v} {
			if _, err := runGitConfig(t.Context(), "--add", "credential."+url+".helper", val); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := reconcileHelper(t.Context(), "https://forge.test", "", ownedBy(cfgDir)); err != nil {
		t.Fatalf("reconcileHelper(want empty): %v", err)
	}
	if got, want := helperValuesFor(t, "https://forge.test"), []string{"", "!gh auth git-credential"}; !slices.Equal(got, want) {
		t.Errorf("after disconnect credential.https://forge.test.helper = %q, want %q", got, want)
	}
	if got, want := helperValuesFor(t, "https://other.test"), []string{"", "!gh auth git-credential", "", v}; !slices.Equal(got, want) {
		t.Errorf("another origin's helpers = %q, want %q untouched", got, want)
	}
}

func TestReconcileHelper_WritesNothingWhenUnchanged(t *testing.T) {
	gitcfg := isolateGit(t)
	cfgDir := t.TempDir()
	v := mustHelperValue(t, "/opt/marotte/marotte", cfgDir)
	if err := reconcileHelper(t.Context(), "https://forge.test", v, ownedBy(cfgDir)); err != nil {
		t.Fatalf("first reconcileHelper: %v", err)
	}
	before, err := os.Stat(gitcfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := reconcileHelper(t.Context(), "https://forge.test", v, ownedBy(cfgDir)); err != nil {
		t.Fatalf("second reconcileHelper: %v", err)
	}
	after, err := os.Stat(gitcfg)
	if err != nil {
		t.Fatal(err)
	}
	// git config replaces the file by rename on every write, so an unchanged
	// inode is a write that did not happen.
	if !os.SameFile(before, after) {
		t.Error("reconcileHelper rewrote the git config although the helpers were already as wanted")
	}
}

func TestRunCredentialHelper_GetAnswersAnOwnedOrigin(t *testing.T) {
	cfgDir := t.TempDir()
	seedCredential(t, cfgDir, "github:github.test", creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://github.test", Token: "gh-token",
	})
	seedCredential(t, cfgDir, "gitlab:gitlab.test", creds.Record{
		Family: forgeapi.FamilyGitLab, WebBaseURL: "https://gitlab.test", Token: "gl-token",
	})
	seedCredential(t, cfgDir, "gitea:gitea.test", creds.Record{
		Family: forgeapi.FamilyGitea, WebBaseURL: "https://gitea.test", Token: "gt-token", Account: "carol",
	})

	for _, tc := range []struct {
		host string
		want string
	}{
		{host: "github.test", want: "username=x-access-token\npassword=gh-token\n"},
		{host: "gitlab.test", want: "username=oauth2\npassword=gl-token\n"},
		{host: "gitea.test", want: "username=carol\npassword=gt-token\n"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			code, out, diag := runHelper(t, cfgDir, "get", "protocol=https\nhost="+tc.host+"\n\n")
			if code != 0 || out != tc.want || diag != "" {
				t.Errorf("RunCredentialHelper(get %s) = %d, out %q, diag %q; want 0, %q, no diag", tc.host, code, out, diag, tc.want)
			}
		})
	}
}

func TestRunCredentialHelper_UnreadableStoreWritesOneLineAndExitsZero(t *testing.T) {
	cfgDir := t.TempDir()
	dir := filepath.Join(cfgDir, credentialStoreDir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	code, out, diag := runHelper(t, cfgDir, "get", "protocol=https\nhost=forge.test\n\n")
	if code != 0 || out != "" {
		t.Errorf("RunCredentialHelper over a widened store = %d, out %q; want 0 and nothing for git", code, out)
	}
	if strings.Count(diag, "\n") != 1 || !strings.Contains(diag, dir) {
		t.Errorf("diag = %q, want exactly one line naming %s", diag, dir)
	}
}

func TestRunCredentialHelper_AbsentStoreAnswersNothingAndCreatesNothing(t *testing.T) {
	cfgDir := t.TempDir()
	code, out, diag := runHelper(t, cfgDir, "get", "protocol=https\nhost=forge.test\n\n")
	if code != 0 || out != "" || diag != "" {
		t.Errorf("RunCredentialHelper with no store = %d, out %q, diag %q; want 0 and nothing", code, out, diag)
	}
	if _, err := os.Lstat(filepath.Join(cfgDir, credentialStoreDir)); err == nil {
		t.Error("a get created the credential store directory")
	}
}

func TestRunCredentialHelper_StoreAndEraseAnswerNothing(t *testing.T) {
	cfgDir := t.TempDir()
	seedCredential(t, cfgDir, "github:forge.test", creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://forge.test", Token: "kept-token",
	})
	for _, action := range []string{"store", "erase"} {
		code, out, diag := runHelper(t, cfgDir, action,
			"protocol=https\nhost=forge.test\nusername=x-access-token\npassword=other\n\n")
		if code != 0 || out != "" || diag != "" {
			t.Errorf("RunCredentialHelper(%s) = %d, out %q, diag %q; want 0 and nothing", action, code, out, diag)
		}
	}
	if code, out, _ := runHelper(t, cfgDir, "get", "protocol=https\nhost=forge.test\n\n"); code != 0 || !strings.Contains(out, "password=kept-token") {
		t.Errorf("get after store and erase = %d, %q; want the connection's own token still answered", code, out)
	}
}

func TestGitCredentialFill_ReachesTheHelper(t *testing.T) {
	isolateGit(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin dir")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "marotte")
	if err := os.WriteFile(bin, data, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(t.TempDir(), "config dir")
	if err := os.Mkdir(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seedCredential(t, cfgDir, "github:forge.test", creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://forge.test", Token: "fill-token",
	})
	if err := reconcileHelper(t.Context(), "https://forge.test", mustHelperValue(t, bin, cfgDir), ownedBy(cfgDir)); err != nil {
		t.Fatalf("Setup: register the helper: %v", err)
	}

	git, ok := systembin.Resolve("git")
	if !ok {
		t.Fatal("no system git")
	}
	cmd := exec.CommandContext(t.Context(), git, "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=forge.test\n\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git credential fill: %v; stderr %q", err, stderr.String())
	}
	if !strings.Contains(string(out), "username=x-access-token\npassword=fill-token\n") {
		t.Errorf("git credential fill = %q (stderr %q), want the helper's credential", out, stderr.String())
	}
}

func TestManagerRefresh_RegistersTheHelperForEveryRecordOnce(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	cfgDir := t.TempDir()
	m := NewManager(cfgDir)
	m.executable = func() (string, error) { return "/opt/marotte/marotte", nil }
	saveRecords(t, m.conns, githubRecord(),
		connectionRecord{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com"})
	gh := []string{"", "!/usr/bin/gh auth git-credential"}
	for _, v := range gh {
		if _, err := runGitConfig(t.Context(), "--add", "credential.https://github.com.helper", v); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := mustHelperValue(t, "/opt/marotte/marotte", cfgDir)
	for url, prefix := range map[string][]string{"https://github.com": gh, "https://gitlab.com": nil} {
		wantList := append(slices.Clone(prefix), "", want)
		if got := helperValuesFor(t, url); !slices.Equal(got, wantList) {
			t.Errorf("after the boot Refresh credential.%s.helper = %q, want %q", url, got, wantList)
		}
	}
	recs, err := m.conns.load()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.HelperValue != want {
			t.Errorf("record %s helper_value = %q, want %q", r.ID, r.HelperValue, want)
		}
	}

	m.executable = func() (string, error) { return "/elsewhere/marotte", nil }
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if got := helperValuesFor(t, "https://gitlab.com"); !slices.Equal(got, []string{"", want}) {
		t.Errorf("a later Refresh re-registered the helper: %q, want the boot value %q", got, want)
	}
}

func TestManagerRefresh_UnverifiedRecordsRegisterNothing(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	cfgDir := t.TempDir()
	m := NewManager(cfgDir)
	m.executable = func() (string, error) { return "/opt/marotte/marotte", nil }
	saveRecords(t, m.conns, githubRecord())
	orig := enforceFileMode
	t.Cleanup(func() { enforceFileMode = orig })
	enforceFileMode = func(string, os.FileMode) (os.FileMode, error) {
		return 0o660, atomicfile.ErrModeNotStored
	}

	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := helperValuesFor(t, "https://github.com"); len(got) != 0 {
		t.Errorf("a record file of unverifiable mode registered helpers %q, want none", got)
	}
}
