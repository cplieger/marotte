package composition

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// TestBuild_StartsTheRecentlyClosedExpiry boots the real Build over a tabs.json whose one entry's
// default window ends a few seconds after the boot: only a running expiry writes a new version,
// since no mutation happens, and the entry stays held for a wider window.
func TestBuild_StartsTheRecentlyClosedExpiry(t *testing.T) {
	capture.SwapDefault(t, slog.Default())

	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("KIRO_HOME", filepath.Join(home, ".kiro"))
	cfg := &Config{
		ConfigDir:       mkdir(t, root, "config"),
		WorkDir:         mkdir(t, root, "work"),
		Tools:           toolsRoot{dir: filepath.Join(root, "tools")},
		ToolCatalogPath: filepath.Join(root, "absent-catalog.json"),
		ToolCatalogURL:  "http://127.0.0.1:1/",
	}
	cfg.BrowseRoots = []string{cfg.WorkDir}
	chats, err := chat.NewStore(filepath.Join(cfg.ConfigDir, "chats"))
	if err != nil {
		t.Fatalf("Setup: chat store: %v", err)
	}
	if _, err := chats.Mutate(t.Context(), "c-old", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Old"
		return true
	}); err != nil {
		t.Fatalf("Setup: seed c-old: %v", err)
	}
	closedAt := time.Now().Add(-settings.DefaultHistoryWindow + 3*time.Second).UnixMilli()
	doc, err := json.Marshal(map[string]any{
		"tabs":    []any{},
		"recent":  []map[string]any{{"ref": "c-old", "name": "Old", "closed_at": closedAt}},
		"version": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	tabsFile := filepath.Join(cfg.ConfigDir, "tabs.json")
	if err := os.WriteFile(tabsFile, doc, 0o600); err != nil {
		t.Fatalf("Setup: write tabs.json: %v", err)
	}

	app, err := Build(t.Context(), cfg, fstest.MapFS{"static/index.html": {Data: []byte("x")}})
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	defer app.Shutdown()

	type tabsDoc struct {
		Recent  []marotte.RecentClose `json:"recent"`
		Version uint64                `json:"version"`
	}
	read := func() (doc tabsDoc) {
		data, err := os.ReadFile(tabsFile)
		if err == nil {
			_ = json.Unmarshal(data, &doc)
		}
		return doc
	}
	if got := read(); got.Version != 3 {
		t.Fatalf("tabs.json version right after the boot = %d, want 3: nothing but the expiry may write it", got.Version)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := read()
		if got.Version == 4 {
			if len(got.Recent) != 1 || got.Recent[0].Ref != "c-old" {
				t.Errorf("tabs.json recent at the expiry's version = %+v, want c-old still held", got.Recent)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tabs.json = %+v 15s after c-old's window ended, want the expiry's version 4", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestApp_ShutdownStopsTheRecentlyClosedExpiry drives App.Shutdown, the function production calls,
// because the runner lives on the app's lifetime and only the stop the App holds ends it.
func TestApp_ShutdownStopsTheRecentlyClosedExpiry(t *testing.T) {
	var exited atomic.Bool
	started := make(chan struct{})
	app := &App{
		stopRecentExpiry: runBackground(context.Background(), "recently closed expiry",
			func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				exited.Store(true)
			}),
	}
	<-started

	app.Shutdown()

	if !exited.Load() {
		t.Error("the Recently closed expiry was still running after App.Shutdown returned; " +
			"it would keep writing tabs.json after the process stopped owning it")
	}
}
