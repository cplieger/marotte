package forges

// git's credential helper registrations follow the usable connections, healed
// at every Load and every keeper pass, and the helper process reads only the
// records file's durable verdicts.

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/slogx/capture"
)

func TestReconcileGit_HealsALostEntryAtLoadAndEachPass(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	want := []string{"", h.helperValue(t)}
	lose := func() {
		t.Helper()
		if _, err := runGitConfig(t.Context(), "--unset-all", "credential."+githubOrigin+".helper"); err != nil {
			t.Fatalf("Setup: remove the entry: %v", err)
		}
	}

	lose()
	h.m.Load(t.Context())
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, want) {
		t.Errorf("credential.%s.helper after a Load = %q, want %q restored", githubOrigin, got, want)
	}
	lose()
	NewKeeper(h.m).pass(t.Context())
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, want) {
		t.Errorf("credential.%s.helper after a keeper pass = %q, want %q restored", githubOrigin, got, want)
	}
}

func TestReconcileGit_HelperFollowsUsableRows(t *testing.T) {
	gitcfg := isolateGit(t)
	const foreign = "!/usr/bin/gh auth git-credential"
	writeGitConfig(t, gitcfg, "[credential \""+githubOrigin+"\"]\n\thelper =\n\thelper = "+foreign+"\n")
	cfg := t.TempDir()
	seedConnections(t, cfg, "bob", githubRecord())
	core := &whoCore{}
	m, _ := loadedManager(t, cfg, core)
	ours := mustHelperValue(t, testHelperBin, cfg)
	registered := []string{"", foreign, "", ours}
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, registered) {
		t.Fatalf("Setup: credential.%s.helper = %q, want %q", githubOrigin, got, registered)
	}

	core.answer(errForbidden)
	if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
		t.Fatal(err)
	}
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, []string{"", foreign}) {
		t.Errorf("credential.%s.helper after a refusal = %q, want only the foreign pair", githubOrigin, got)
	}
	core.answer(nil)
	if _, err := m.check(t.Context(), ghID, checkBudget); err != nil {
		t.Fatal(err)
	}
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, registered) {
		t.Errorf("credential.%s.helper after a revival = %q, want %q", githubOrigin, got, registered)
	}

	moved := githubRecord()
	moved.WebBaseURL, moved.PlaintextHTTP = "http://github.com", true
	signInNow(t, m, moved, "ghp_moved")
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, []string{"", foreign}) {
		t.Errorf("credential.%s.helper after re-addressing = %q, want only the foreign pair", githubOrigin, got)
	}
	if got := helperValuesFor(t, "http://github.com"); !slices.Equal(got, []string{"", ours}) {
		t.Errorf("credential.http://github.com.helper after re-addressing = %q, want ours", got)
	}
}

// parkedOnGitMu reports whether a goroutine waits for gitMu inside reconcileGit.
func parkedOnGitMu() bool {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "[sync.Mutex.Lock") && strings.Contains(g, ".(*Manager).reconcileGit(") {
			return true
		}
	}
	return false
}

// queuedReconcile starts reconcileGit on ctx while the test holds gitMu, and
// returns once it waits for the lock; release lets it run and waits for it.
func queuedReconcile(t *testing.T, ctx context.Context, m *Manager) (release func()) {
	t.Helper()
	m.gitMu.Lock()
	done := make(chan struct{})
	go func() {
		m.reconcileGit(ctx)
		close(done)
	}()
	var once sync.Once
	release = func() {
		once.Do(func() {
			m.gitMu.Unlock()
			<-done
		})
	}
	t.Cleanup(release)
	waitFor(t, "the reconcile to wait for gitMu", parkedOnGitMu)
	return release
}

func TestReconcileGit_AQueuedReconcileReadsTheRowsItWaitedFor(t *testing.T) {
	m, _ := checkManager(t, &whoCore{})
	release := queuedReconcile(t, t.Context(), m)

	added := specFor(KindGitea, "gitea.test")
	m.mu.Lock()
	c := *m.conns[ghID]
	c.spec = added
	m.conns[added.ID] = &c
	m.mu.Unlock()
	release()

	want := []string{"", mustHelperValue(t, testHelperBin, m.configDir)}
	if got := helperValuesFor(t, added.webBase()); !slices.Equal(got, want) {
		t.Errorf("credential.%s.helper after a reconcile queued before the row was installed = %q, want %q",
			added.webBase(), got, want)
	}
}

func TestReconcileGit_AnEndedCallerLogsNoWarning(t *testing.T) {
	noWarning := func(t *testing.T, logs *capture.Recorder) {
		t.Helper()
		for _, r := range logs.Records() {
			if r.Level >= slog.LevelWarn {
				t.Errorf("a reconcile whose caller left logged %s %q, want no warning: git did not fail", r.Level, r.Message)
			}
		}
	}

	t.Run("cancelled while it waits for the lock", func(t *testing.T) {
		m, _ := checkManager(t, &whoCore{})
		logs := capture.Default(t)
		ctx, cancel := context.WithCancel(t.Context())
		release := queuedReconcile(t, ctx, m)
		cancel()
		release()
		noWarning(t, logs)
	})

	t.Run("cancelled during the rewrite", func(t *testing.T) {
		m, _ := checkManager(t, &whoCore{})
		if _, err := runGitConfig(t.Context(), "--unset-all", "credential."+githubOrigin+".helper"); err != nil {
			t.Fatalf("Setup: remove the entry: %v", err)
		}
		const stale = "credential.https://stale.test.helper"
		for _, v := range []string{"", mustHelperValue(t, testHelperBin, m.configDir)} {
			if _, err := runGitConfig(t.Context(), "--add", stale, v); err != nil {
				t.Fatalf("Setup: register a stale helper: %v", err)
			}
		}
		logs := capture.Default(t)
		ctx, cancel := context.WithCancel(t.Context())
		m.executable = func() (string, error) {
			cancel()
			return testHelperBin, nil
		}
		m.reconcileGit(ctx)
		noWarning(t, logs)
	})
}

func TestCredentialHelper_ReadsTheRecordsReadOnly(t *testing.T) {
	seeded := func(t *testing.T) string {
		t.Helper()
		cfg := t.TempDir()
		seedHelperConnections(t, cfg, map[string]helperConnection{"forge": helperOn(KindGitHub, "forge.test", "kept-token")})
		return cfg
	}
	get := func(t *testing.T, cfg string) (out, diag string) {
		t.Helper()
		code, out, diag := runHelper(t, cfg, "get", "protocol=https\nhost=forge.test\n\n")
		if code != 0 {
			t.Fatalf("the helper exited %d, want 0", code)
		}
		return out, diag
	}

	t.Run("an unverified mode", func(t *testing.T) {
		cfg := seeded(t)
		path := filepath.Join(cfg, connectionsFileName)
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if out, _ := get(t, cfg); !strings.Contains(out, "password=kept-token") {
			t.Errorf("the helper over a 0640 records file answered %q, want the usable credential", out)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("records file mode after the helper read = %v (%v), want 0640 untouched", info.Mode().Perm(), err)
		}
	})
	for name, prepare := range map[string]func(t *testing.T, cfg string){
		"an unparseable file": func(t *testing.T, cfg string) {
			if err := os.WriteFile(filepath.Join(cfg, connectionsFileName), []byte(`{"connections":[`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a directory at the name": func(t *testing.T, cfg string) {
			path := filepath.Join(cfg, connectionsFileName)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(path, "inside"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := seeded(t)
			prepare(t, cfg)
			out, diag := get(t, cfg)
			if out != "" || strings.Count(diag, "\n") != 1 || !strings.Contains(diag, connectionsFileName) ||
				strings.Contains(diag, credentialStoreDir) {
				t.Errorf("the helper over %s answered %q with diag %q, want nothing and one line naming the records file",
					name, out, diag)
			}
		})
	}
	t.Run("a missing file", func(t *testing.T) {
		cfg := seeded(t)
		if err := os.Remove(filepath.Join(cfg, connectionsFileName)); err != nil {
			t.Fatal(err)
		}
		if out, diag := get(t, cfg); out != "" || diag != "" {
			t.Errorf("the helper with no records file answered %q, diag %q; want nothing", out, diag)
		}
	})
}

// unopenableStore makes cfg's forge store a directory forgeapi refuses to open,
// and answers the func that narrows it again.
func unopenableStore(t *testing.T, cfg string) (narrow func()) {
	t.Helper()
	dir := filepath.Join(cfg, credentialStoreDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	narrow = func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("narrow the store: %v", err)
		}
	}
	t.Cleanup(narrow)
	return narrow
}

func TestLoad_AnUnusablePersistenceHealsWithoutARestart(t *testing.T) {
	t.Run("the records file's mode", func(t *testing.T) {
		isolateGit(t)
		cfg := t.TempDir()
		seedConnections(t, cfg, "bob", githubRecord())
		orig := enforceFileMode
		t.Cleanup(func() { enforceFileMode = orig })
		enforceFileMode = func(string, os.FileMode) (os.FileMode, error) { return 0o660, os.ErrPermission }
		core := &whoCore{}
		m, changes := loadedManager(t, cfg, core)
		k := NewKeeper(m)

		if got := viewFor(t, m, ghID); got.Status != ForgeUnusable {
			t.Fatalf("row over an unverified records file = %+v, want unusable", got)
		}
		k.pass(t.Context())
		if core.calls() != 0 {
			t.Errorf("a pass over unusable persistence sent %d requests, want none", core.calls())
		}
		spec := githubRecord()
		if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &spec, new(patFor(githubOrigin, "t"))); err == nil {
			t.Error("signIn() over unusable persistence = nil, want it refused")
		}

		enforceFileMode = orig
		k.pass(t.Context())
		if got := viewFor(t, m, ghID); got.Status != ForgeConnected {
			t.Errorf("row after the file was repaired and a pass = %+v, want connected", got)
		}
		if _, err := m.reader(ghID); err != nil {
			t.Errorf("reader() after the heal = %v, want a client", err)
		}
		if got := changes.all(); len(got) == 0 || got[0] != (Change{}) || slices.Index(got[1:], Change{}) >= 0 {
			t.Errorf("the heal notified %+v, want one change of every row first and no other", got)
		}
	})

	t.Run("the store directory's mode", func(t *testing.T) {
		isolateGit(t)
		cfg := t.TempDir()
		seedConnections(t, cfg, "bob", githubRecord())
		narrow := unopenableStore(t, cfg)
		m, _ := loadedManager(t, cfg, &whoCore{})
		if got := viewFor(t, m, ghID); got.Status != ForgeUnusable {
			t.Fatalf("row over an unopenable store = %+v, want unusable", got)
		}
		narrow()
		NewKeeper(m).pass(t.Context())
		if got := viewFor(t, m, ghID); got.Status != ForgeConnected {
			t.Errorf("row after the store was repaired and a pass = %+v, want connected", got)
		}
	})

	t.Run("one Error line per verdict", func(t *testing.T) {
		isolateGit(t)
		cfg := t.TempDir()
		seedConnections(t, cfg, "bob", githubRecord())
		narrow := unopenableStore(t, cfg)
		logs := capture.Default(t)
		m, _ := loadedManager(t, cfg, &whoCore{})
		k := NewKeeper(m)
		k.pass(t.Context())
		k.pass(t.Context())
		narrow()
		k.pass(t.Context())

		var errorLines []string
		for _, r := range logs.Records() {
			if r.Level == slog.LevelError {
				errorLines = append(errorLines, r.Message)
			}
		}
		if len(errorLines) != 1 || !strings.Contains(string(rendered(t, logs)), credentialStoreDir) {
			t.Errorf("a boot Load, two passes and a heal logged Error lines %q, want one naming the store", errorLines)
		}
	})
}

func TestGrantStart_ReadsTheManagersHealth(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	h := newConnectHarness(t, nil)
	restore := widenStore(t, h.m)

	if rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("start over an unreadable store = %d %s, want 503", rec.Code, rec.Body)
	}
	restore()
	NewKeeper(h.m).pass(t.Context())
	if rec := h.do(t, http.MethodPost, githubGrantStart, e.startBody(applianceClientID)); rec.Code != http.StatusOK {
		t.Errorf("start after the heal = %d %s, want 200 with no restart", rec.Code, rec.Body)
	}
}
