package composition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/testsupport"
)

// The install's probe and assertions run the binary before app.disableAutoupdates is set, so every command the
// install runs carries kirorelease.NoAutoUpdateVar, under an `env -i` parent too.
func TestStartKiroCLI_EveryInstallCommandRunsWithSelfUpdateOff(t *testing.T) {
	const version = "9.9.9"
	testsupport.EmptyEnv(t)
	toolsDir := t.TempDir()
	log := filepath.Join(t.TempDir(), "runs.log")
	versionDir := filepath.Join(toolsDir, "kiro-cli-versions", version)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatalf("Setup: create version dir: %v", err)
	}
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"${KIRO_NO_AUTO_UPDATE-unset}\" >>'" + log + "'\n" +
		"[ \"$1\" = --version ] && printf 'kiro-cli " + version + "\\n'\nexit 0\n"
	syscall.ForkLock.RLock()
	err := os.WriteFile(filepath.Join(versionDir, "kiro-cli"), []byte(script), 0o700) // #nosec G306 -- a dispatcher fake must be executable
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: write fake kiro-cli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, ".complete"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatalf("Setup: write sentinel: %v", err)
	}
	cfg := pinnedKiroConfig(toolsDir, version)
	// Not t.Context(): the manager must outlive the t.Cleanup(kiro.stop) teardown.
	kiro := startKiroCLI(context.Background(), &cfg)
	t.Cleanup(kiro.stop)
	select {
	case <-kiro.installed:
	case <-time.After(20 * time.Second):
		t.Fatal("Setup: the staged version never became active")
	}
	if ok, err := kiro.rescan(t.Context()); !ok || err != nil {
		t.Fatalf("Setup: rescan() = (%v, %v), want (true, nil)", ok, err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the fake kiro-cli never ran: %v", err)
	}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		verb, value, _ := strings.Cut(line, " ")
		seen[verb] = true
		if value != "1" {
			t.Errorf("kiro-cli %s saw KIRO_NO_AUTO_UPDATE %q, want 1", verb, value)
		}
	}
	for _, verb := range []string{"--version", "settings"} {
		if !seen[verb] {
			t.Errorf("the install never ran kiro-cli %s; runs: %q", verb, data)
		}
	}
}
