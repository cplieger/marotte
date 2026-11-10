package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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

func TestToolsSlotTick_AfterCloseDoesNothing(t *testing.T) {
	for name, initial := range map[string]string{"up": `{"version":2,"tools":{}}`, "down": `{"version":1,"tools":{}}`} {
		t.Run(name, func(t *testing.T) {
			fx := newOwnedFilesFixtureWith(t, toolsSetup{manifest: initial})
			before := len(fx.enqueued())
			fx.tools.Close()
			if err := os.WriteFile(fx.manifest, []byte(`{"version":2,"tools":{"x":{"source":"manual","install":"true"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}

			fx.tools.tick()
			fx.tools.tick()

			if engine, reason := fx.tools.Engine(); engine != nil || !errors.Is(reason, errToolsClosed) {
				t.Errorf("Engine() after Close and two ticks = (%v, %v), want (nil, %v)", engine, reason, errToolsClosed)
			}
			if got := fx.enqueued(); len(got) != before {
				t.Errorf("jobs queued = %v, want none after the %d before Close", got, before)
			}
		})
	}
}

func TestToolsSlotTick_ABadManifestTakesTheEngineDownWithItsReasonAtTheSecondTick(t *testing.T) {
	fx := newOwnedFilesFixtureWith(t, toolsSetup{manifest: `{"version":2,"tools":{}}`})
	if engine, reason := fx.tools.Engine(); engine == nil {
		t.Fatalf("Setup: Engine() over a valid manifest = (nil, %v), want the engine up", reason)
	}
	if err := os.WriteFile(fx.manifest, []byte(`{"version":3,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	fx.tools.tick()
	if engine, reason := fx.tools.Engine(); engine == nil {
		t.Fatalf("Engine() after one tick over a version-3 manifest = (nil, %v), want still up (the read is held)", reason)
	}
	fx.tools.tick()

	engine, reason := fx.tools.Engine()
	if engine != nil {
		t.Fatal("Engine() after two ticks over a version-3 manifest = a live engine, want down")
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
	tools.tick()
	if builds != 1 {
		t.Errorf("a tick over the tools.json start read built the engine again: %d builds, want 1", builds)
	}
	if err := os.WriteFile(manifest, []byte(fmt.Sprintf(`{"version":%d,"tools":{"x":{"source":"manual","install":"true"}}}`,
		toolbelt.ManifestVersion)), 0o600); err != nil {
		t.Fatal(err)
	}
	tools.tick()
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
	tools.tick()
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

// within fails the test unless f returns inside d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s has not returned after %v", what, d)
	}
}

// fifoAt replaces path with a FIFO no one writes to.
func fifoAt(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Setup: mkfifo: %v", err)
	}
	// A writer end opened and closed releases a read the slot may have left blocked on the FIFO.
	t.Cleanup(func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
}

func TestToolsSlot_AFIFOAtToolsJSONNeverBlocks(t *testing.T) {
	t.Run("while_up", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		fifoAt(t, fx.manifest)

		within(t, time.Second, "the first tick over a FIFO", fx.tools.tick)
		if engine, reason := fx.tools.Engine(); engine == nil || len(loggedManifestRefusals(t, fx.logs)) != 0 {
			t.Errorf("after one tick: Engine() = (%v, %v), refusals %d; want up and nothing logged", engine, reason, len(loggedManifestRefusals(t, fx.logs)))
		}
		within(t, time.Second, "the second tick over a FIFO", fx.tools.tick)
		if engine, reason := fx.tools.Engine(); engine != nil || reason == nil || !strings.Contains(reason.Error(), "not a regular file") {
			t.Errorf("Engine() after two ticks = (%v, %v), want down with a reason naming a file that is not regular", engine, reason)
		}
		if n := len(loggedManifestRefusals(t, fx.logs)); n != 1 {
			t.Errorf("refusals logged after two ticks = %d, want 1", n)
		}
		if got := fx.enqueued(); len(got) != 0 {
			t.Errorf("jobs queued = %v, want none", got)
		}
	})
	t.Run("at_boot", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), toolsManifestName)
		fifoAt(t, manifest)
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

// countedSlot is a started tools slot over a fresh config dir whose build counts its calls: it
// fails with fail when that is set and runs toolbelt.New otherwise.
type countedSlot struct {
	fail     error
	slot     *toolsSlot
	logs     *bytes.Buffer
	manifest string
	builds   int
}

func newCountedSlot(t *testing.T, doc string, fail error) *countedSlot {
	t.Helper()
	configDir, toolsDir := fitTree(t)
	c := &countedSlot{manifest: filepath.Join(configDir, toolsManifestName), fail: fail}
	writeConfig(t, c.manifest, doc)
	c.logs = captureDefaultLogger(t)
	c.slot = newToolsSlot(c.manifest, func() (*toolbelt.Engine, error) {
		c.builds++
		if c.fail != nil {
			return nil, c.fail
		}
		return toolbelt.New(&toolbelt.Config{ConfigDir: configDir, ToolsDir: toolsDir})
	})
	t.Cleanup(c.slot.Close)
	if err := c.slot.start(); err != nil {
		t.Fatalf("Setup: start: %v", err)
	}
	return c
}

func (c *countedSlot) refusals(t *testing.T) int {
	t.Helper()
	return len(loggedManifestRefusals(t, c.logs))
}

func (c *countedSlot) remove(t *testing.T) {
	t.Helper()
	if err := os.Remove(c.manifest); err != nil {
		t.Fatal(err)
	}
}

func TestToolsSlotTick_ActsOnceOnEachChangeWhileDown(t *testing.T) {
	unfit := fmt.Errorf("toolbelt: %w", toolbelt.ErrRootIntegrity)
	invalid, otherInvalid := `{"version":3,"tools":{}}`, `{"version":4,"tools":{}}`
	t.Run("an invalid file", func(t *testing.T) {
		c := newCountedSlot(t, invalid, nil)
		if c.builds != 1 || c.refusals(t) != 1 {
			t.Fatalf("after start: %d builds, %d refusals; want 1 and 1", c.builds, c.refusals(t))
		}
		c.slot.tick()
		c.slot.tick()
		if c.builds != 1 || c.refusals(t) != 1 {
			t.Errorf("after two unchanged ticks: %d builds, %d refusals; want 1 and 1", c.builds, c.refusals(t))
		}
		writeConfig(t, c.manifest, otherInvalid)
		c.slot.tick()
		if n := c.refusals(t); n != 1 {
			t.Errorf("refusals after one tick over another invalid document = %d, want 1 (the read is held)", n)
		}
		c.slot.tick()
		if c.builds != 1 || c.refusals(t) != 2 {
			t.Errorf("after the second tick over it: %d builds, %d refusals; want 1 and 2", c.builds, c.refusals(t))
		}
		writeConfig(t, c.manifest, manifestOf())
		c.slot.tick()
		if engine, reason := c.slot.Engine(); c.builds != 2 || engine == nil {
			t.Errorf("after a valid edit and a tick: %d builds, Engine() = (%v, %v); want 2 and up", c.builds, engine, reason)
		}
	})
	t.Run("an unfit root over a valid manifest", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), unfit)
		c.slot.tick()
		c.slot.tick()
		if c.builds != 1 {
			t.Errorf("builds after start and two unchanged ticks = %d, want 1", c.builds)
		}
		writeConfig(t, c.manifest, manifestOf(disabledRow("x")))
		c.slot.tick()
		if c.builds != 2 {
			t.Errorf("builds after an edit and a tick = %d, want 2", c.builds)
		}
	})
	t.Run("an invalid edit while up", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), nil)
		writeConfig(t, c.manifest, invalid)
		c.slot.tick()
		if engine, _ := c.slot.Engine(); engine == nil || c.refusals(t) != 0 {
			t.Errorf("after one tick: engine up %v, %d refusals; want up and none", engine != nil, c.refusals(t))
		}
		c.slot.tick()
		if engine, _ := c.slot.Engine(); engine != nil || c.refusals(t) != 1 {
			t.Errorf("after two ticks: engine up %v, %d refusals; want down and 1", engine != nil, c.refusals(t))
		}
		c.slot.tick()
		if n := c.refusals(t); n != 1 {
			t.Errorf("refusals after a third unchanged tick = %d, want 1", n)
		}
	})
	t.Run("absent, then the person's manifest", func(t *testing.T) {
		c := newCountedSlot(t, invalid, nil)
		c.remove(t)
		c.slot.tick()
		if _, err := os.Stat(c.manifest); c.builds != 1 || !os.IsNotExist(err) {
			t.Errorf("after one tick over an absence: %d builds, stat %v; want 1 and still absent", c.builds, err)
		}
		doc := manifestOf(disabledRow("mine"))
		writeConfig(t, c.manifest, doc)
		c.slot.tick()
		if got, _ := os.ReadFile(c.manifest); c.builds != 2 || string(got) != doc {
			t.Errorf("after the person's manifest and a tick: %d builds, tools.json %q; want 2 and %q byte for byte", c.builds, got, doc)
		}
	})
	t.Run("absent for two ticks", func(t *testing.T) {
		c := newCountedSlot(t, invalid, nil)
		c.remove(t)
		c.slot.tick()
		c.slot.tick()
		got, err := os.ReadFile(c.manifest)
		if m, perr := toolbelt.ParseManifest(got); c.builds != 2 || err != nil || perr != nil || len(m.Tools) != 0 {
			t.Errorf("after two ticks over an absence: %d builds, tools.json %q (%v, %v); want 2 and the empty seed", c.builds, got, err, perr)
		}
		c.slot.tick()
		if c.builds != 2 {
			t.Errorf("builds after a third tick = %d, want 2", c.builds)
		}
	})
	t.Run("absent under an unfit root", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), unfit)
		c.remove(t)
		c.slot.tick()
		if c.builds != 1 {
			t.Errorf("builds after one tick over an absence = %d, want 1", c.builds)
		}
	})
	t.Run("a save ends a hold from before it", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), unfit)
		c.builds = 0
		c.remove(t)
		c.slot.tick()
		if _, err := c.slot.Replace(t.Context(), []byte(manifestOf(disabledRow("x"))), nil); err != nil {
			t.Fatalf("Replace = %v, want written", err)
		}
		if engine, _ := c.slot.Engine(); c.builds != 1 || engine != nil {
			t.Fatalf("after Replace: %d builds, engine up %v; want 1 and still down", c.builds, engine != nil)
		}
		c.remove(t)
		c.slot.tick()
		if c.builds != 1 {
			t.Errorf("builds after one tick over the absence that followed the save = %d, want 1 (a hold from before the save counted)", c.builds)
		}
	})
}

// pinnedManualRow is a manual row the fixture's state reports installed at 1.0.
func pinnedManualRow(name string) string {
	return fmt.Sprintf(`%q:{"source":"manual","install":"true","version":"1.0","pin":true}`, name)
}

// installedToolsSeed lists a alone.
func installedToolsSeed() *toolbelt.Manifest {
	return &toolbelt.Manifest{Version: toolbelt.ManifestVersion, Tools: map[string]toolbelt.Tool{
		"a": {Source: toolbelt.SourceManual, Install: "true", Version: "1.0", Pin: true},
	}}
}

// installedTools is an engine up over tools.json holding rows a and b, both installed, with a
// seed listing a alone.
func installedTools() toolsSetup {
	return toolsSetup{
		manifest: manifestOf(pinnedManualRow("a"), pinnedManualRow("b")),
		config:   func(c *toolbelt.Config) { c.Seed = installedToolsSeed() },
		prepare: func(t *testing.T, configDir, toolsDir string) {
			t.Helper()
			state := `{"tools":{"a":{"installed_version":"1.0","bins":["a"]},"b":{"installed_version":"1.0","bins":["b"]}}}`
			writeConfig(t, filepath.Join(configDir, "tools-state.json"), state)
			if err := os.MkdirAll(filepath.Join(toolsDir, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			syscall.ForkLock.RLock()
			defer syscall.ForkLock.RUnlock()
			for _, bin := range []string{"a", "b"} {
				if err := os.WriteFile(filepath.Join(toolsDir, "bin", bin), []byte("#!/bin/sh\necho 1.0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
}

// seedAsNewWritesIt is tools.json as toolbelt.New writes installedToolsSeed on a fresh config dir.
func seedAsNewWritesIt(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	engine, err := toolbelt.New(&toolbelt.Config{ConfigDir: dir, ToolsDir: filepath.Join(dir, "tools"), Seed: installedToolsSeed()})
	if err != nil {
		t.Fatalf("Setup: a second engine for the seed: %v", err)
	}
	engine.Close()
	data, err := os.ReadFile(filepath.Join(dir, toolsManifestName))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestToolsSlotTick_ARemovalWhileUpIsSeededAtTheSecondTick(t *testing.T) {
	remove := func(t *testing.T, fx *ownedFilesFixture) {
		t.Helper()
		if err := os.Remove(fx.manifest); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("removed", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		seed := seedAsNewWritesIt(t)
		remove(t, fx)
		fx.tools.tick()
		if _, err := os.Stat(fx.manifest); !os.IsNotExist(err) || len(fx.enqueued()) != 0 {
			t.Errorf("after one tick: stat %v, jobs %v; want still absent and none", err, fx.enqueued())
		}
		fx.tools.tick()
		if got, _ := os.ReadFile(fx.manifest); !bytes.Equal(got, seed) {
			t.Errorf("tools.json after two ticks = %q, want the seed as New writes it %q", got, seed)
		}
		if got := fx.enqueued(); !slices.Equal(got, []string{toolbelt.JobKindReconcile}) {
			t.Errorf("jobs after two ticks = %v, want one %q", got, toolbelt.JobKindReconcile)
		}
		fx.tools.tick()
		if got := fx.enqueued(); len(got) != 1 {
			t.Errorf("jobs after a third tick = %v, want still one", got)
		}
	})
	t.Run("removed, then the person's manifest", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		remove(t, fx)
		fx.tools.tick()
		doc := manifestOf(pinnedManualRow("a"), disabledRow("mine"))
		writeConfig(t, fx.manifest, doc)
		fx.tools.tick()
		if got, _ := os.ReadFile(fx.manifest); string(got) != doc {
			t.Errorf("tools.json = %q, want the person's %q byte for byte", got, doc)
		}
		if got := fx.enqueued(); !slices.Equal(got, []string{toolbelt.JobKindReconcile}) {
			t.Errorf("jobs = %v, want one %q", got, toolbelt.JobKindReconcile)
		}
	})
	t.Run("a save ends a hold from before it", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		remove(t, fx)
		fx.tools.tick()
		if _, err := fx.tools.Replace(t.Context(), []byte(manifestOf(pinnedManualRow("a"), pinnedManualRow("b"), disabledRow("x"))), nil); err != nil {
			t.Fatalf("Replace = %v, want written", err)
		}
		if got := fx.enqueued(); len(got) != 1 {
			t.Fatalf("Setup: jobs after the save = %v, want its one reconcile", got)
		}
		remove(t, fx)
		fx.tools.tick()
		if _, err := os.Stat(fx.manifest); !os.IsNotExist(err) || len(fx.enqueued()) != 1 {
			t.Errorf("after one tick over the removal that followed the save: stat %v, jobs %v; want still absent and no new job", err, fx.enqueued())
		}
	})
	t.Run("an Add inside the hold", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		remove(t, fx)
		fx.tools.tick()
		engine, reason := fx.tools.Engine()
		if engine == nil {
			t.Fatalf("Setup: Engine() = (nil, %v)", reason)
		}
		if _, err := engine.Add(t.Context(), &toolbelt.AddRequest{Name: "c", Source: toolbelt.SourceManual, Install: "true", Disabled: true}); err != nil {
			t.Fatalf("Add inside the hold = %v", err)
		}
		fx.tools.tick()
		got, _ := os.ReadFile(fx.manifest)
		m, err := toolbelt.ParseManifest(got)
		if err != nil || !slices.Equal(slices.Sorted(maps.Keys(m.Tools)), []string{"a", "b", "c"}) {
			t.Errorf("tools.json after an Add inside the hold and a tick = %q (%v), want rows a, b and c", got, err)
		}
		if got := fx.enqueued(); len(got) != 0 {
			t.Errorf("jobs = %v, want none (nothing was seeded)", got)
		}
	})
}
