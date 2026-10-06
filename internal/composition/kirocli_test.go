package composition

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/pinstall/v3"
)

// TestStartKiroCLIShapes pins the runtimes startKiroCLI returns for a configuration it cannot
// install from, since each answers /api/health differently and a wrong choice is silent.
func TestStartKiroCLIShapes(t *testing.T) {
	const goodDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	tests := map[string]struct {
		cfg       Config
		wantPath  string
		wantGate  bool
		wantReady bool
		reason    pinstall.Reason
		rescan    bool
	}{
		"no pins resolves the bare name and installs nothing": {
			cfg:      Config{ToolsDir: t.TempDir()},
			wantPath: "kiro-cli",
		},
		"pins present but no tools dir falls back to the bare name": {
			cfg:      Config{KiroCLIVersion: "2.14.2", KiroCLISHA256: goodDigest},
			wantPath: "kiro-cli",
		},
		"unusable pins report unready rather than pretending": {
			cfg:      Config{KiroCLIVersion: "2.14.2", KiroCLISHA256: "not-a-digest", ToolsDir: t.TempDir()},
			wantPath: "",
			wantGate: true,
			reason:   pinstall.ReasonUnavailable,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := tc.cfg
			kiro := startKiroCLI(t.Context(), &cfg)
			defer kiro.stop()

			if got := kiro.cliPath(); got != tc.wantPath {
				t.Errorf("cliPath() = %q, want %q", got, tc.wantPath)
			}
			// Called unconditionally by the bridge factory on every spawn, so a nil here panics on
			// the first chat.
			if got := kiro.env(); tc.wantPath == "" && got != nil {
				t.Errorf("env() = %v, want nil when no version is active", got)
			}
			switch {
			case tc.wantGate && kiro.ready == nil:
				t.Fatal("no readiness verdict published; /api/health would report healthy with no usable kiro-cli")
			case !tc.wantGate && kiro.ready != nil:
				t.Fatal("a readiness verdict was published for an install this server does not own")
			}
			if tc.wantGate {
				ready, reason := kiro.ready()
				if ready != tc.wantReady || reason != tc.reason {
					t.Errorf("ready() = (%v, %s), want (%v, %s)", ready, reason, tc.wantReady, tc.reason)
				}
			}
			if (kiro.rescan != nil) != tc.rescan {
				t.Errorf("rescan wired = %v, want %v", kiro.rescan != nil, tc.rescan)
			}
		})
	}
}

// TestStartKiroCLIAdoptsACompleteVersionDirectory drives the managed path end to end with the
// pinned version already complete, so nothing is downloaded.
func TestStartKiroCLIAdoptsACompleteVersionDirectory(t *testing.T) {
	const version = "9.9.9"
	toolsDir := t.TempDir()
	versionDir := filepath.Join(toolsDir, "kiro-cli-versions", version)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatalf("create version dir: %v", err)
	}
	script := "#!/bin/sh\ncase \"$1\" in --version) printf 'kiro-cli " + version + "\\n' ;; esac\nexit 0\n"
	for _, name := range []string{"kiro-cli", "kiro-cli-chat"} {
		if err := os.WriteFile(filepath.Join(versionDir, name), []byte(script), 0o700); err != nil { // #nosec G306 -- a dispatcher fake must be executable
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(versionDir, ".complete"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	cfg := Config{
		KiroCLIVersion:     version,
		KiroCLISHA256:      strings.Repeat("a", 64),
		KiroCLISHA256ARM64: strings.Repeat("b", 64),
		ToolsDir:           toolsDir,
	}
	// Not t.Context(): the manager must outlive the t.Cleanup(kiro.stop) teardown, and t.Context()
	// is cancelled before cleanups run.
	kiro := startKiroCLI(context.Background(), &cfg)
	t.Cleanup(kiro.stop)

	if kiro.ready == nil || kiro.rescan == nil {
		t.Fatalf("managed runtime is missing wiring: ready=%v rescan=%v",
			kiro.ready != nil, kiro.rescan != nil)
	}
	deadline := time.Now().Add(20 * time.Second)
	var reason pinstall.Reason
	for {
		ok, why := kiro.ready()
		if ok {
			break
		}
		reason = why
		if time.Now().After(deadline) {
			t.Fatalf("no version became active within the deadline; last readiness reason %s", reason)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got, want := kiro.cliPath(), filepath.Join(versionDir, "kiro-cli"); got != want {
		t.Errorf("cliPath() = %q, want the absolute version-directory path %q", got, want)
	}
	env := kiro.env()
	if len(env) != 1 || !strings.HasPrefix(env[0], "PATH="+versionDir+string(os.PathListSeparator)) {
		t.Errorf("env() = %v, want a single PATH entry leading with %q", env, versionDir)
	}
}

// TestStartKiroCLIRejectsASidecarLessVersionDirectory asserts that `--version` is answered by the main binary,
// so a directory with no chat sidecar must not be adopted.
func TestStartKiroCLIRejectsASidecarLessVersionDirectory(t *testing.T) {
	const version = "9.9.9"
	toolsDir := t.TempDir()
	versionDir := filepath.Join(toolsDir, "kiro-cli-versions", version)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatalf("create version dir: %v", err)
	}
	script := "#!/bin/sh\ncase \"$1\" in --version) printf 'kiro-cli " + version + "\\n' ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(versionDir, "kiro-cli"), []byte(script), 0o700); err != nil { // #nosec G306 -- a dispatcher fake must be executable
		t.Fatalf("write fake dispatcher: %v", err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, ".complete"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	cfg := Config{
		KiroCLIVersion:     version,
		KiroCLISHA256:      strings.Repeat("a", 64),
		KiroCLISHA256ARM64: strings.Repeat("b", 64),
		ToolsDir:           toolsDir,
	}
	// Not t.Context(): the manager must outlive the t.Cleanup(kiro.stop) teardown, and t.Context()
	// is cancelled before cleanups run.
	kiro := startKiroCLI(context.Background(), &cfg)
	t.Cleanup(kiro.stop)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok, why := kiro.ready(); ok {
			t.Fatalf("a sidecar-less version directory was adopted (readiness %v, reason %s); "+
				"kiro-cli acp would fail at every chat spawn", ok, why)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := kiro.cliPath(); got != "" {
		t.Errorf("cliPath() = %q, want empty: no version may be active", got)
	}
}

// TestKiroSettingsLeavesTheIntegrityGateToTheManager asserts that app.disableAutoupdates is not in the list,
// because kirocli.Release() declares it Mandatory.
func TestKiroSettingsLeavesTheIntegrityGateToTheManager(t *testing.T) {
	settings := kiroSettings()
	if len(settings) == 0 {
		t.Fatal("no kiro-cli settings configured; the Settings UI would misreport every toggle until a second boot")
	}
	seen := map[string]bool{}
	for _, a := range settings {
		if a.Name == "app.disableAutoupdates" {
			t.Error("app.disableAutoupdates is listed as a best-effort assertion; the release profile owns it as Mandatory")
		}
		if seen[a.Name] {
			t.Errorf("setting %q is listed twice", a.Name)
		}
		seen[a.Name] = true
		if a.Required {
			t.Errorf("setting %q is marked Required; only the profile's own mandatory assertion may gate readiness", a.Name)
		}
		want := []string{"settings", a.Name}
		if len(a.Args) != 3 || !slices.Equal(a.Args[:2], want) || a.Args[2] == "" {
			t.Errorf("setting %q has argv %v, want %v plus a non-empty value", a.Name, a.Args, want)
		}
	}
}
