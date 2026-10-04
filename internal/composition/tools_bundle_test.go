package composition

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/toolbelt/v3"
)

// forgeCLIs are the names a forge CLI goes by in a manifest, a seed, the
// bundle and the image's required list: the seed spelled GitHub's `gh`, the
// bundle `github-cli`.
var forgeCLIs = []string{"gh", "github-cli", "glab", "tea"}

func readBundleEntries(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "bundled-tools.json"))
	if err != nil {
		t.Fatalf("read bundled-tools.json: %v", err)
	}
	var doc struct {
		Entries map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode bundled-tools.json: %v", err)
	}
	return doc.Entries
}

func readRequiredTools(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "required-tools.txt"))
	if err != nil {
		t.Fatalf("open required-tools.txt: %v", err)
	}
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			names = append(names, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan required-tools.txt: %v", err)
	}
	return names
}

func TestBundledTools_NamesNoForgeCLI(t *testing.T) {
	entries := readBundleEntries(t)
	if _, ok := entries["node"]; !ok {
		t.Fatalf("bundled-tools.json entries = %v, want the node row the MCP servers need", slices.Sorted(maps.Keys(entries)))
	}
	required := readRequiredTools(t)
	if !slices.Contains(required, "gopls") {
		t.Fatalf("required-tools.txt names %v, want the seeded gopls template", required)
	}
	for _, cli := range forgeCLIs {
		if _, ok := entries[cli]; ok {
			t.Errorf("bundled-tools.json bundles %q, want no forge CLI (ADR-0003)", cli)
		}
		if slices.Contains(required, cli) {
			t.Errorf("required-tools.txt requires %q, want no forge CLI (ADR-0003)", cli)
		}
	}
}

// TestToolsSeed_NamesNoForgeCLI reads the manifest the production engine
// literal seeds a fresh volume with, so the seed buildToolsEngine passes is
// the one under test.
func TestToolsSeed_NamesNoForgeCLI(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	engine, err := buildToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir, WorkDir: t.TempDir()}, testRuntime(t))
	if err != nil || engine == nil {
		t.Fatalf("buildToolsEngine over a fit root = (%v, %v), want an engine", engine, err)
	}
	t.Cleanup(engine.Close)

	raw, err := os.ReadFile(filepath.Join(configDir, "tools.json"))
	if err != nil {
		t.Fatalf("read the seeded manifest: %v", err)
	}
	var m toolbelt.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode the seeded manifest %s: %v", raw, err)
	}
	if _, ok := m.Tools["gopls"]; !ok {
		t.Fatalf("seeded manifest = %s, want the gopls template", raw)
	}
	for _, cli := range forgeCLIs {
		if _, ok := m.Tools[cli]; ok {
			t.Errorf("a fresh volume seeds %q, want no forge CLI template (ADR-0003)", cli)
		}
	}
}

// installedForgeCLIs is what an existing install's manifest holds: the three
// CLIs Marotte installed on demand before ADR-0003, each row hydrated with the
// source its install ran. The sources here are offline stand-ins writing the
// binary each probe looks for.
var installedForgeCLIs = []struct{ name, bin string }{
	{"github-cli", "gh"},
	{"glab", "glab"},
	{"tea", "tea"},
}

func toolsEngineOver(t *testing.T, configDir, toolsDir string, output func(string)) *toolbelt.Engine {
	t.Helper()
	e, err := toolbelt.New(&toolbelt.Config{
		ConfigDir:       configDir,
		ToolsDir:        toolsDir,
		CatalogOverlays: []string{filepath.Join("..", "..", "bundled-tools.json")},
		Seed:            toolsSeed(),
		OnJobOutput: func(_ string, lines []string) {
			for _, l := range lines {
				output(l)
			}
		},
	})
	if err != nil {
		t.Fatalf("toolbelt.New: %v", err)
	}
	return e
}

func waitDone(t *testing.T, e *toolbelt.Engine, jv *toolbelt.Job, what string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	final, err := e.Wait(ctx, jv.ID)
	if err != nil {
		t.Fatalf("wait %s: %v", what, err)
	}
	if final.State != toolbelt.JobDone {
		t.Fatalf("%s ended %s: %s", what, final.State, final.Error)
	}
}

// TestToolsReconcile_KeepsInstalledForgeCLIsWithoutTheirOverlayRows is
// ADR-0003's accepted path: the boot after the bundle drops the forge CLIs
// converges an existing volume without uninstalling them.
func TestToolsReconcile_KeepsInstalledForgeCLIsWithoutTheirOverlayRows(t *testing.T) {
	entries := readBundleEntries(t)
	for _, c := range installedForgeCLIs {
		if _, ok := entries[c.name]; ok {
			t.Fatalf("bundled-tools.json still carries %q, so this boot is not the one without its overlay row", c.name)
		}
	}
	configDir, toolsDir := fitTree(t)

	before := toolsEngineOver(t, configDir, toolsDir, func(string) {})
	for _, c := range installedForgeCLIs {
		jv, err := before.Add(t.Context(), &toolbelt.AddRequest{
			Name: c.name, Source: toolbelt.SourceManual, Version: "1", Probe: c.bin,
			Install: fmt.Sprintf(`printf '#!/bin/sh\necho %[1]s 1\n' > "$BIN/%[1]s" && chmod 755 "$BIN/%[1]s"`, c.bin),
		})
		if err != nil {
			t.Fatalf("add %s: %v", c.name, err)
		}
		waitDone(t, before, jv, "install "+c.name)
	}
	before.Close()

	var mu sync.Mutex
	var lines []string
	after := toolsEngineOver(t, configDir, toolsDir, func(l string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, l)
	})
	t.Cleanup(after.Close)
	jv, enqueued, err := after.Reconcile(toolbelt.ReconcileMissing)
	if err != nil || !enqueued {
		t.Fatalf("Reconcile = (%v, %v, %v), want an enqueued job over a non-empty manifest", jv, enqueued, err)
	}
	waitDone(t, after, jv, "boot reconcile")

	inv, err := after.Inventory()
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	installed := map[string]bool{}
	for i := range inv.Tools {
		installed[inv.Tools[i].Name] = inv.Tools[i].Installed && !inv.Tools[i].Disabled
	}
	for _, c := range installedForgeCLIs {
		if !installed[c.name] {
			t.Errorf("after the boot reconcile %s is not an enabled, installed row: %+v", c.name, inv.Tools)
		}
		if _, err := os.Stat(filepath.Join(toolsDir, "bin", c.bin)); err != nil {
			t.Errorf("after the boot reconcile bin/%s is gone: %v", c.bin, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range lines {
		if strings.Contains(l, "uninstall") || strings.Contains(l, "sweeping") {
			t.Errorf("the boot reconcile removed something: %q", l)
		}
	}
}
