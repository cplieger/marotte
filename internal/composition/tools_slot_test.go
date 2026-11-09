package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/workspace"
	"github.com/cplieger/toolbelt/v3"
)

// manifestRefusalMsg must match refuseManifest's message: it is the one line naming the
// file and the parse error an operator searches the boot log for.
const manifestRefusalMsg = "tools engine disabled: tools.json is unusable; marotte is running without the tools subsystem"

func loggedManifestRefusals(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var found []map[string]any
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			break
		}
		if rec["msg"] == manifestRefusalMsg {
			found = append(found, rec)
		}
	}
	return found
}

func TestWireToolsEngineBootsOverAnInvalidManifestAndSaysWhy(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	manifest := filepath.Join(configDir, "tools.json")
	doc := fmt.Sprintf(`{"version":%d,"tools":{}}`, toolbelt.ManifestVersion+1)
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := captureDefaultLogger(t)

	tools, err := wireToolsEngine(&Config{ConfigDir: configDir, Tools: toolsRoot{dir: toolsDir}}, testRuntime(t), nil)
	if err != nil {
		t.Fatalf("a tools.json toolbelt refuses stopped the boot: %v", err)
	}
	t.Cleanup(tools.Close)

	engine, reason := tools.Engine()
	if engine != nil {
		t.Fatal("an engine came up over a manifest toolbelt refuses")
	}
	wantVersion := fmt.Sprintf("manifest version %d, want %d", toolbelt.ManifestVersion+1, toolbelt.ManifestVersion)
	if reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid: "+wantVersion) {
		t.Errorf("Engine() reason = %v, want it to say tools.json is invalid: %s", reason, wantVersion)
	}
	recs := loggedManifestRefusals(t, logs)
	if len(recs) != 1 {
		t.Fatalf("ERROR records naming the unusable tools.json = %d, want 1; logs:\n%s", len(recs), logs.String())
	}
	if recs[0]["level"] != "ERROR" || recs[0]["path"] != manifest ||
		!strings.Contains(fmt.Sprint(recs[0]["error"]), wantVersion) {
		t.Errorf("refusal record = %v, want an ERROR naming %s and %q", recs[0], manifest, wantVersion)
	}
	if after, _ := os.ReadFile(manifest); string(after) != doc {
		t.Errorf("tools.json after the degraded boot = %q, want %q untouched", after, doc)
	}
}

func TestWireToolsEngineBootsOverAnUnreadableManifest(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	if err := os.Mkdir(filepath.Join(configDir, "tools.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	tools, err := wireToolsEngine(&Config{ConfigDir: configDir, Tools: toolsRoot{dir: toolsDir}}, testRuntime(t), nil)
	if err != nil {
		t.Fatalf("an unreadable tools.json stopped the boot: %v", err)
	}
	t.Cleanup(tools.Close)

	if engine, reason := tools.Engine(); engine != nil || reason == nil ||
		!strings.Contains(reason.Error(), "tools.json cannot be read") {
		t.Errorf("Engine() over a directory at tools.json = (%v, %v), want down saying it cannot be read", engine, reason)
	}
}

func TestToolsSlot_ManifestChangedAfterCloseStartsNothing(t *testing.T) {
	fx := newManifestSaveFixture(t, `{"version":1,"tools":{}}`)
	if err := os.WriteFile(fx.manifest, []byte(`{"version":2,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.tools.Close()

	fx.tools.manifestChanged()

	if engine, reason := fx.tools.Engine(); engine != nil || !errors.Is(reason, errToolsClosed) {
		t.Errorf("Engine() after Close and a save = (%v, %v), want (nil, %v)", engine, reason, errToolsClosed)
	}
}

func TestToolsSlot_ManifestChangedOverABadManifestTakesTheEngineDownWithItsReason(t *testing.T) {
	fx := newManifestSaveFixture(t, `{"version":2,"tools":{}}`)
	if engine, reason := fx.tools.Engine(); engine == nil {
		t.Fatalf("Setup: Engine() over a valid manifest = (nil, %v), want the engine up", reason)
	}
	if err := os.WriteFile(fx.manifest, []byte(`{"version":3,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	fx.tools.manifestChanged()

	engine, reason := fx.tools.Engine()
	if engine != nil {
		t.Fatal("Engine() after a version-3 manifest landed = a live engine, want down")
	}
	if reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid: manifest version 3") {
		t.Errorf("Engine() reason = %v, want it to say tools.json is invalid: manifest version 3", reason)
	}
	if recs := loggedManifestRefusals(t, fx.logs); len(recs) != 1 || recs[0]["path"] != fx.manifest {
		t.Errorf("ERROR records naming the unusable tools.json = %v, want one naming %s", recs, fx.manifest)
	}
}

func TestToolsSlot_StartRetriesARefusalWhoseManifestWasRepairedMeanwhile(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	manifest := filepath.Join(configDir, "tools.json")
	if err := os.WriteFile(manifest, []byte(`{"version":3,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)
	builds := 0
	tools := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
		builds++
		engine, err := toolbelt.New(&toolbelt.Config{ConfigDir: configDir, ToolsDir: toolsDir})
		if builds == 1 {
			if werr := os.WriteFile(manifest, []byte(`{"version":2,"tools":{}}`), 0o600); werr != nil {
				t.Errorf("repair tools.json during the first build: %v", werr)
			}
		}
		return engine, err
	})
	t.Cleanup(tools.Close)

	if err := tools.start(); err != nil {
		t.Fatalf("start() with tools.json repaired while toolbelt refused it = %v, want no fatal boot", err)
	}

	if engine, reason := tools.Engine(); engine == nil {
		t.Errorf("Engine() after the repaired start = (nil, %v), want the engine up on the repaired file", reason)
	}
}

func TestToolsSlot_AnUnusableToolsFolderIsBuiltOncePerAttempt(t *testing.T) {
	configDir, _ := fitTree(t)
	manifest := filepath.Join(configDir, "tools.json")
	doc := fmt.Sprintf(`{"version":%d,"tools":{}}`, toolbelt.ManifestVersion)
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)
	problem := checkToolsRoot("MAROTTE_TOOLS_DIR", "/tools:x").err
	builds := 0
	tools := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
		builds++
		return nil, problem
	})
	t.Cleanup(tools.Close)

	if err := tools.start(); err != nil {
		t.Fatalf("start() = %v, want the boot to go on", err)
	}
	if builds != 1 {
		t.Errorf("start() built the engine %d times, want 1", builds)
	}
	tools.manifestChanged()
	if builds != 2 {
		t.Errorf("start() and one tools.json write built the engine %d times, want 2", builds)
	}
}

// A tools folder the engine cannot use is a setting, so the boot goes on with the engine down,
// the Tools tab says why, and a tools.json save does not start it there.
func TestWireToolsEngine_StaysDownOverAnUnusableToolsFolder(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	paths := resolveBootPaths(envSource(map[string]string{"KIRO_CONFIG_DIR": configDir, "MAROTTE_TOOLS_DIR": toolsDir + ":x"}))
	captureDefaultLogger(t)
	cfg := &Config{ConfigDir: configDir, Tools: paths.tools}

	tools, err := wireToolsEngine(cfg, testRuntime(t), nil)
	if err != nil {
		t.Fatalf("an unusable tools folder stopped the boot: %v", err)
	}
	t.Cleanup(tools.Close)
	if engine, reason := tools.Engine(); engine != nil || !errors.Is(reason, errToolsDirUnusable) {
		t.Fatalf("Engine() = (%v, %v), want down with the folder's reason", engine, reason)
	}
	doc := fmt.Sprintf(`{"version":%d,"tools":{}}`, toolbelt.ManifestVersion)
	if err := os.WriteFile(filepath.Join(configDir, "tools.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	tools.manifestChanged()
	if engine, reason := tools.Engine(); engine != nil || !errors.Is(reason, errToolsDirUnusable) {
		t.Errorf("after a tools.json write Engine() = (%v, %v), want still down with the folder's reason", engine, reason)
	}
	if _, err := os.Stat(toolsDir + ":x"); err == nil {
		t.Errorf("the engine created %s, a folder it cannot use", toolsDir+":x")
	}
}

func TestToolsSnapshot_KeepsWhatTheProbeReportsInstalledAndEnabled(t *testing.T) {
	inv := &toolbelt.Inventory{
		Tools: []toolbelt.ToolInfo{
			{Name: "go", Source: "aqua:golang/go", InstalledVersion: "1.26.2", Installed: true, Commands: []string{"go", "gofmt"}},
			{Name: "black", Source: "pip:black", InstalledVersion: "24.1.0", Commands: []string{"black"}},
			{Name: "ripgrep", Source: "apt:ripgrep", InstalledVersion: "14.1.1-1", Installed: true, Disabled: true, Commands: []string{"rg"}},
			{Name: "libfoo", Source: "apt:libfoo", InstalledVersion: "2.0", Installed: true},
			{Name: "fd", Installed: true},
		},
		System: []toolbelt.SystemTool{{Name: "git", Installed: true}, {Name: "ssh"}, {Name: "jq", Installed: true}},
	}

	got := toolsSnapshot(inv)

	want := []steering.InstalledTool{
		{Version: "1.26.2", Commands: []string{"go", "gofmt"}},
		{Version: "2.0", Apt: true},
		{},
	}
	same := func(a, b steering.InstalledTool) bool {
		return a.Version == b.Version && slices.Equal(a.Commands, b.Commands) && a.Apt == b.Apt
	}
	if !slices.EqualFunc(got.Tools, want, same) {
		t.Errorf("toolsSnapshot rows = %+v, want %+v", got.Tools, want)
	}
	if want := []string{"git", "jq"}; !slices.Equal(got.System, want) {
		t.Errorf("toolsSnapshot system = %v, want the present ones %v", got.System, want)
	}
}

func TestToolsSlot_InstalledToolsAnswersOnlyWhileTheEngineIsUp(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	manifest := filepath.Join(configDir, "tools.json")
	doc := fmt.Sprintf(`{"version":%d,"tools":{}}`, toolbelt.ManifestVersion)
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	down := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
		return nil, checkToolsRoot("MAROTTE_TOOLS_DIR", "/tools:x").err
	})
	t.Cleanup(down.Close)
	if err := down.start(); err != nil {
		t.Fatal(err)
	}
	if tools, ok := down.installedTools(); ok || tools.Tools != nil || tools.System != nil {
		t.Errorf("installedTools() with the engine down = (%v, %t), want (empty, false)", tools, ok)
	}

	up := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
		return toolbelt.New(&toolbelt.Config{ConfigDir: configDir, ToolsDir: toolsDir})
	})
	t.Cleanup(up.Close)
	if err := up.start(); err != nil {
		t.Fatal(err)
	}
	if tools, ok := up.installedTools(); !ok || len(tools.Tools) != 0 {
		t.Errorf("installedTools() with the engine up over an empty tools.json = (%v, %t), want (none, true)", tools, ok)
	}
}

// The boot regenerates environment.md once the engine is up, listing the commands of what its
// probe reports installed.
func TestWireToolsSteering_ListsTheEnginesInstalledTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configDir, toolsDir := fitTree(t)
	manifest := filepath.Join(configDir, toolsManifestName)
	doc := fmt.Sprintf(`{"version":%d,"tools":{"hello":{"source":"manual","install":"true","version":"1.0","pin":true}}}`,
		toolbelt.ManifestVersion)
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	state := `{"tools":{"hello":{"installed_version":"1.0","bins":["hello","hello-cli"]}}}`
	if err := os.WriteFile(filepath.Join(configDir, "tools-state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(toolsDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	syscall.ForkLock.RLock()
	for _, bin := range []string{"hello", "hello-cli"} {
		if err := os.WriteFile(filepath.Join(toolsDir, "bin", bin), []byte("#!/bin/sh\necho 1.0\n"), 0o755); err != nil {
			syscall.ForkLock.RUnlock()
			t.Fatal(err)
		}
	}
	syscall.ForkLock.RUnlock()
	captureDefaultLogger(t)
	tools := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
		return toolbelt.New(&toolbelt.Config{ConfigDir: configDir, ToolsDir: toolsDir})
	})
	t.Cleanup(tools.Close)
	if err := tools.start(); err != nil {
		t.Fatal(err)
	}

	wireToolsSteering(context.WithoutCancel(t.Context()), tools, steering.New(t.TempDir(), configDir, toolsDir))

	path := workspace.KiroSteeringPath("environment.md")
	want := "## Installed tools\n\nCommands: hello 1.0, hello-cli 1.0\n\n"
	waitFor(t, "environment.md to list the installed tool", func() bool {
		data, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(data), want)
	})
}

func TestToolsSlot_AFIFOAtToolsJSONNeverBlocks(t *testing.T) {
	t.Run("at_boot", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), toolsManifestName)
		if err := syscall.Mkfifo(manifest, 0o600); err != nil {
			t.Fatalf("Setup: mkfifo: %v", err)
		}
		// A writer end opened and closed releases a read the slot may have left blocked on the FIFO.
		t.Cleanup(func() {
			if f, err := os.OpenFile(manifest, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
		captureDefaultLogger(t)
		slot := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
			return nil, fmt.Errorf("toolbelt: init %s: %w", manifest, errors.New("not a regular file"))
		})
		started := make(chan error, 1)
		go func() { started <- slot.start() }()
		select {
		case err := <-started:
			if err != nil {
				t.Fatalf("start over a FIFO = %v, want the engine down with its reason", err)
			}
		case <-time.After(time.Second):
			t.Fatal("start over a FIFO at tools.json has not returned after 1s")
		}
		engine, reason := slot.Engine()
		if engine != nil || reason == nil || !strings.Contains(reason.Error(), "not a regular file") {
			t.Errorf("Engine() over a FIFO = (%v, %v), want down with a reason naming a file that is not regular", engine, reason)
		}
	})
}
