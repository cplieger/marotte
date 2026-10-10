package composition

// marotte turns toolbelt's root-integrity check ON and answers a refusal by running tool-less
// rather than refusing to boot; both halves are pinned.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/mcp/prewarm"
	"github.com/cplieger/toolbelt/v3"
)

// unfitRootMsg must match logRootIntegrityRefusal's per-finding message. The
// per-path lines ARE the operator-facing contract (the refusal reports which
// root and why, and repairs nothing), so a silent rename is a regression rather
// than a cosmetic edit.
const unfitRootMsg = "tools: managed root is not fit to execute from"

// captureDefaultLogger redirects slog's default for one call; toolbelt's own refusal line lands
// here too, since marotte sets no Config.Logger.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// loggedUnfitPaths returns the paths the refusal named, sorted, and fails the
// test for any finding logged without a reason -- a path with no reason is a
// line an operator cannot act on, which defeats the point of reporting instead
// of repairing.
func loggedUnfitPaths(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var found []string
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec struct {
			Msg    string `json:"msg"`
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}
		if err := dec.Decode(&rec); err != nil {
			break
		}
		if rec.Msg != unfitRootMsg {
			continue
		}
		if rec.Reason == "" {
			t.Errorf("finding for %q logged no reason", rec.Path)
		}
		found = append(found, rec.Path)
	}
	slices.Sort(found)
	return found
}

// fitTree builds the layout production runs -- the tools dir inside the config
// dir -- with both roots fit, so a case's plant() is the only defect present.
func fitTree(t *testing.T) (configDir, toolsDir string) {
	t.Helper()
	configDir = t.TempDir()
	toolsDir = filepath.Join(configDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return configDir, toolsDir
}

// testRuntime is a real agent rather than nil: both failure arms return before the runtime is
// touched, so a nil would hide a regression that reached it.
func testRuntime(t *testing.T) *agent.Runtime {
	t.Helper()
	store, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	h := agent.New(t.Context(), t.TempDir(), nil, store)
	t.Cleanup(func() {
		if err := h.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	return h
}

// mkdirMode creates a directory at an EXACT mode. MkdirAll applies the process umask, so a
// group-writable fixture has to chmod afterwards or it silently lands at 0755 and the case asserts
// nothing.
func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// TestWireToolsEngineDegradesOnRootIntegrityRefusal asserts that an unfit managed root leaves marotte running
// without the tools subsystem.
func TestWireToolsEngineDegradesOnRootIntegrityRefusal(t *testing.T) {
	tests := map[string]struct {
		// plant introduces the defect and returns the paths the check must
		// name, in any order.
		plant func(t *testing.T, configDir, toolsDir string) []string
	}{
		"a symlinked bin is the launcher dir the install probe executes": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				bin := filepath.Join(toolsDir, "bin")
				if err := os.Symlink(t.TempDir(), bin); err != nil {
					t.Fatal(err)
				}
				return []string{bin}
			},
		},
		"a group-writable npm/bin lets a non-owner plant a launcher": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				npmBin := filepath.Join(toolsDir, "npm", "bin")
				mkdirMode(t, npmBin, 0o775)
				return []string{npmBin}
			},
		},
		"a regular file where opt belongs": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				opt := filepath.Join(toolsDir, "opt")
				if err := os.WriteFile(opt, []byte("not a dir"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{opt}
			},
		},
		"a symlinked npm redirects its bin out of the tree": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				outside := t.TempDir()
				if err := os.MkdirAll(filepath.Join(outside, "bin"), 0o750); err != nil {
					t.Fatal(err)
				}
				npm := filepath.Join(toolsDir, "npm")
				if err := os.Symlink(outside, npm); err != nil {
					t.Fatal(err)
				}
				return []string{npm, filepath.Join(npm, "bin")}
			},
		},
		"a group-writable tools dir": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				if err := os.Chmod(toolsDir, 0o775); err != nil {
					t.Fatal(err)
				}
				return []string{toolsDir}
			},
		},
		"a group-writable config dir": {
			plant: func(t *testing.T, configDir, _ string) []string {
				if err := os.Chmod(configDir, 0o775); err != nil {
					t.Fatal(err)
				}
				return []string{configDir}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			configDir, toolsDir := fitTree(t)
			want := tc.plant(t, configDir, toolsDir)
			slices.Sort(want)

			logs := captureDefaultLogger(t)
			tools, err := wireToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t), nil)
			if err != nil {
				t.Fatalf("an unfit root stopped the boot; a dev box whose volume drifted cannot be repaired from inside a container that will not start: %v", err)
			}
			engine, reason := tools.Engine()
			if engine != nil {
				t.Fatal("an engine was constructed over an unfit root; the integrity check is not enabled on the real config literal")
			}
			if !errors.Is(reason, errToolsRootUnfit) {
				t.Errorf("Engine() reason over an unfit root = %v, want %v", reason, errToolsRootUnfit)
			}
			if got := loggedUnfitPaths(t, logs); !slices.Equal(got, want) {
				t.Errorf("findings named %v, want %v; logs:\n%s", got, want, logs.String())
			}
		})
	}
}

// TestAppShutdownToleratesTheDegradedEngine asserts that no toolbelt method is nil-receiver safe, so Shutdown
// must skip the nil engine.
func TestAppShutdownToleratesTheDegradedEngine(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(toolsDir, "bin")); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	tools, err := wireToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t), nil)
	if err != nil {
		t.Fatalf("wireToolsEngine over an unfit root = %v, want the degraded slot", err)
	}
	if engine, _ := tools.Engine(); engine != nil {
		t.Fatal("Setup: an engine came up over an unfit root")
	}

	chatStore, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Runtime:        agent.New(t.Context(), t.TempDir(), nil, chatStore),
		purgeScheduler: chat.NewPurgeScheduler(chatStore, func() time.Duration { return 0 }),
		mcpPrewarm:     prewarm.NewRunner(t.Context(), nil),
		tools:          tools,
		stopKiro:       func() {},
	}
	app.Shutdown()
}

// TestWireToolsEngineKeepsOtherFailuresFatal asserts that degrading on any New error would turn an
// unrelated regression into a tool-less boot.
func TestWireToolsEngineKeepsOtherFailuresFatal(t *testing.T) {
	configDir, _ := fitTree(t)
	logs := captureDefaultLogger(t)

	tools, err := wireToolsEngine(t.Context(), &Config{ConfigDir: configDir}, testRuntime(t), nil)

	if err == nil {
		t.Fatal("a missing tools dir was absorbed into a tool-less boot; only an unfit root or an unusable tools.json may degrade")
	}
	if tools != nil {
		t.Error("a slot was returned alongside a fatal error")
	}
	if errors.Is(err, toolbelt.ErrRootIntegrity) {
		t.Errorf("a configuration failure classified as a root-integrity refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "tools engine:") {
		t.Errorf("error wrapping changed to %q; callers and logs read this prefix", err)
	}
	if got := loggedUnfitPaths(t, logs); len(got) != 0 {
		t.Errorf("a non-integrity failure logged root findings %v", got)
	}
}
