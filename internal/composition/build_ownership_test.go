package composition

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

// A second marotte booted on a live instance's KIRO_HOME must refuse before it renders its own
// (empty) MCP store over the live kiro-cli config.
func TestBuild_RefusesAKiroHomeAnotherInstanceHolds(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	root := t.TempDir()
	home := filepath.Join(root, "home")
	kiro := filepath.Join(home, ".kiro")
	t.Setenv("HOME", home)
	t.Setenv("KIRO_HOME", kiro)
	kasFile := filepath.Join(kiro, "settings", "mcp.json")
	seeded := []byte(`{"mcpServers":{"agent-mail":{"url":"http://127.0.0.1:1/mcp"}}}`)
	if err := os.MkdirAll(filepath.Dir(kasFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kasFile, seeded, 0o600); err != nil {
		t.Fatal(err)
	}

	live, err := os.OpenFile(filepath.Join(kiro, "marotte.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	if err := syscall.Flock(int(live.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := live.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		ConfigDir:       mkdir(t, root, "config"),
		WorkDir:         mkdir(t, root, "work"),
		ToolsDir:        filepath.Join(root, "tools"),
		ToolCatalogPath: filepath.Join(root, "absent-catalog.json"),
		ToolCatalogURL:  "http://127.0.0.1:1/",
	}
	app, err := Build(t.Context(), cfg, fstest.MapFS{"static/index.html": {Data: []byte("x")}})
	if app != nil {
		app.Shutdown()
	}
	if err == nil || !strings.Contains(err.Error(), kiro) {
		t.Errorf("Build = %v, want a refusal naming %s", err, kiro)
	}
	if got, _ := os.ReadFile(kasFile); !bytes.Equal(got, seeded) {
		t.Errorf("live kiro-cli MCP config was rewritten:\n got %s\nwant %s", got, seeded)
	}
	if _, err := os.Stat(filepath.Join(kiro, "steering", "environment.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("steering/environment.md stat = %v, want it never written", err)
	}
}

func mkdir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
