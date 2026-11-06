package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/kasnode"
)

const fakeLauncher = "/app/marotte"

// kasNodeFixture is a HOME holding kiro-cli's extracted node (a script answering --version), its node.sha256, and a
// kiro-cli embedding the node's hash before KIRO_KAS_NODE_PATH, as 3.0.0 does. It returns the cli and node
// paths. Not parallel: it sets HOME and swaps selfExecutable.
func kasNodeFixture(t *testing.T, version string) (cli, node string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("KIRO_RUNTIME_DIR", "")
	for _, name := range []string{kasnode.NodePathVar, kasnode.LaunchVar} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	prev := selfExecutable
	selfExecutable = func() (string, error) { return fakeLauncher, nil }
	t.Cleanup(func() { selfExecutable = prev })

	dir := filepath.Join(home, ".local", "share", "kiro-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	node = filepath.Join(dir, "node")
	script := []byte("#!/bin/sh\necho " + version + "\n")
	syscall.ForkLock.RLock()
	err := os.WriteFile(node, script, 0o700)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(script)
	hash := hex.EncodeToString(sum[:])
	if err := os.WriteFile(node+".sha256", []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	cli = filepath.Join(t.TempDir(), "kiro-cli")
	if err := os.WriteFile(cli, []byte("\x7fELF..."+hash+kasnode.NodePathVar+"KIRO_KAS_SERVER_PATH"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cli, node
}

func kasNodeEngine(t *testing.T, cli string, on bool) *Engine {
	t.Helper()
	e := NewEngine(cli, t.TempDir())
	e.lifecycleCtx = t.Context()
	e.highThroughput = on
	return e
}

func launcherEnv(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		return !strings.HasPrefix(kv, kasnode.NodePathVar+"=") && !strings.HasPrefix(kv, kasnode.LaunchVar+"=")
	})
}

func TestProcessEnv_TheSettingNamesTheLauncher(t *testing.T) {
	cli, node := kasNodeFixture(t, kasnode.ValidatedNodeVersion)
	t.Setenv(nodeOptionsVar, "--max-semi-space-size=64")
	logs := capture.Default(t)

	t.Run("on", func(t *testing.T) {
		e := kasNodeEngine(t, cli, true)
		e.heapMiB = 4096
		env := e.processEnv()
		want := []string{kasnode.NodePathVar + "=" + fakeLauncher, kasnode.LaunchVar + "=" + node}
		if got := launcherEnv(env); !slices.Equal(got, want) {
			t.Errorf("launcher variables = %q, want %q", got, want)
		}
		if want := append(slices.Clone(kasnode.Flags), "--max-old-space-size=4096"); !slices.Equal(e.nodeArgs, want) {
			t.Errorf("nodeArgs = %q, want %q", e.nodeArgs, want)
		}
		if v, _ := envValue(env, kasnode.HeapLimitVar); v != "4096" {
			t.Errorf("%s = %q, want 4096 for the launcher", kasnode.HeapLimitVar, v)
		}
		// The flags and the heap limit ride the launcher's argv; NODE_OPTIONS, which every program the agent runs
		// inherits, stays as inherited.
		if opts, _ := envValue(env, nodeOptionsVar); opts != "--max-semi-space-size=64" {
			t.Errorf("NODE_OPTIONS = %q, want the inherited value only", opts)
		}
		for _, kv := range env {
			if strings.Contains(kv, "--experimental-async-context-frame") {
				t.Errorf("the engine environment carries %q; every program the agent runs would inherit it", kv)
			}
		}
	})
	t.Run("off", func(t *testing.T) {
		t.Setenv(kasnode.LaunchVar, "/inherited/node")
		e := kasNodeEngine(t, cli, false)
		e.heapMiB = 4096
		env := e.processEnv()
		if got := launcherEnv(env); len(got) != 0 {
			t.Errorf("launcher variables = %q, want none: off is kiro-cli's stock launch", got)
		}
		// kiro-cli starts node itself, so NODE_OPTIONS is the only way in.
		if opts, _ := envValue(env, nodeOptionsVar); opts != "--max-semi-space-size=64 --max-old-space-size=4096" {
			t.Errorf("NODE_OPTIONS = %q, want the inherited value and the heap limit", opts)
		}
		if e.nodeArgs != nil {
			t.Errorf("nodeArgs = %q, want nil", e.nodeArgs)
		}
	})
	t.Run("on with no heap limit", func(t *testing.T) {
		t.Setenv(kasnode.HeapLimitVar, "2048")
		e := kasNodeEngine(t, cli, true)
		env := e.processEnv()
		if v, ok := envValue(env, kasnode.HeapLimitVar); ok {
			t.Errorf("%s = %q, want unset with no limit configured", kasnode.HeapLimitVar, v)
		}
		if !slices.Equal(e.nodeArgs, kasnode.Flags) {
			t.Errorf("nodeArgs = %q, want kasnode.Flags %q", e.nodeArgs, kasnode.Flags)
		}
	})
	if n := logs.CountLevel(slog.LevelWarn, "high throughput"); n != 0 {
		t.Errorf("%d high throughput warnings, want none when every check passes", n)
	}
}

// Node ranks a NODE_OPTIONS heap percentage above a heap size on its argv, so the limit then goes into NODE_OPTIONS,
// replacing the percentage, as it does without the launcher.
func TestProcessEnv_AnInheritedHeapPercentageKeepsTheLimitInNodeOptions(t *testing.T) {
	cli, _ := kasNodeFixture(t, kasnode.ValidatedNodeVersion)
	t.Setenv(nodeOptionsVar, "--max-old-space-size-percentage=50 --trace-warnings")
	e := kasNodeEngine(t, cli, true)
	e.heapMiB = 4096
	env := e.processEnv()
	if opts, _ := envValue(env, nodeOptionsVar); opts != "--trace-warnings --max-old-space-size=4096" {
		t.Errorf("NODE_OPTIONS = %q, want the percentage replaced by the limit", opts)
	}
	if v, ok := envValue(env, kasnode.HeapLimitVar); ok {
		t.Errorf("%s = %q, want unset: the limit is in NODE_OPTIONS", kasnode.HeapLimitVar, v)
	}
	if !slices.Equal(e.nodeArgs, kasnode.Flags) {
		t.Errorf("nodeArgs = %q, want kasnode.Flags %q", e.nodeArgs, kasnode.Flags)
	}
}

// runtimeDirFixture moves kasNodeFixture's node into a KIRO_RUNTIME_DIR entry laid out as kiro-cli 3.0.0 unpacks it
// (<version>-<16 hex>/node, no node.sha256), with its .complete marker when complete is set. It returns the node.
func runtimeDirFixture(t *testing.T, dataNode string, complete bool) string {
	t.Helper()
	rt := t.TempDir()
	t.Setenv("KIRO_RUNTIME_DIR", rt)
	entry := filepath.Join(rt, "3.0.0-0123456789abcdef")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(entry, "node")
	if err := os.Rename(dataNode, node); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dataNode + ".sha256"); err != nil {
		t.Fatal(err)
	}
	if complete {
		if err := os.WriteFile(filepath.Join(entry, ".complete"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return node
}

// With KIRO_RUNTIME_DIR set kiro-cli runs the node of its runtime entry, which carries no node.sha256; the launch
// checks that node against the hash in the binary.
func TestProcessEnv_TheLauncherRunsTheRuntimeDirNode(t *testing.T) {
	t.Run("a complete entry", func(t *testing.T) {
		cli, dataNode := kasNodeFixture(t, kasnode.ValidatedNodeVersion)
		node := runtimeDirFixture(t, dataNode, true)
		logs := capture.Default(t)
		e := kasNodeEngine(t, cli, true)
		want := []string{kasnode.NodePathVar + "=" + fakeLauncher, kasnode.LaunchVar + "=" + node}
		if got := launcherEnv(e.processEnv()); !slices.Equal(got, want) {
			t.Errorf("launcher variables = %q, want %q", got, want)
		}
		if n := logs.CountLevel(slog.LevelWarn, "high throughput"); n != 0 {
			t.Errorf("%d high throughput warnings, want none", n)
		}
	})
	t.Run("an entry still unpacking", func(t *testing.T) {
		cli, dataNode := kasNodeFixture(t, kasnode.ValidatedNodeVersion)
		runtimeDirFixture(t, dataNode, false)
		logs := capture.Default(t)
		e := kasNodeEngine(t, cli, true)
		if got := launcherEnv(e.processEnv()); len(got) != 0 {
			t.Errorf("launcher variables = %q, want none before the entry is complete", got)
		}
		if !logs.AttrContains("starting without the high throughput optimizations", "reason", "has not extracted its node yet") {
			t.Error("the warning's reason does not say the node is not extracted yet")
		}
	})
	t.Run("another kiro-cli's entry", func(t *testing.T) {
		cli, dataNode := kasNodeFixture(t, kasnode.ValidatedNodeVersion)
		runtimeDirFixture(t, dataNode, true)
		if err := os.WriteFile(cli, []byte(strings.Repeat("a", 64)+kasnode.NodePathVar+"KIRO_KAS_SERVER_PATH"), 0o600); err != nil {
			t.Fatal(err)
		}
		cliNodeHashes.Lock()
		delete(cliNodeHashes.m, cli)
		cliNodeHashes.Unlock()
		logs := capture.Default(t)
		e := kasNodeEngine(t, cli, true)
		if got := launcherEnv(e.processEnv()); len(got) != 0 {
			t.Errorf("launcher variables = %q, want none for a node this kiro-cli does not bundle", got)
		}
		if !logs.AttrContains("starting without the high throughput optimizations", "reason", "is not the node this kiro-cli bundles") {
			t.Error("the warning's reason does not say the node is not this kiro-cli's")
		}
		// A runtime entry has no node.sha256, so the reason must not quote one.
		if logs.AttrContains("starting without the high throughput optimizations", "reason", "node.sha256") {
			t.Error("the warning's reason quotes a node.sha256 for a runtime entry, which has none")
		}
	})
}

// Each failed check starts the stock engine and logs one WARN naming why.
func TestProcessEnv_AFailedCheckStartsTheStockEngine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		mutate  func(t *testing.T, cli, node string)
		reason  []string
	}{
		{
			name: "another node version", version: "v24.11.0",
			reason: []string{"v24.11.0", kasnode.ValidatedNodeVersion},
		},
		{
			name: "a node kiro-cli did not extract", version: kasnode.ValidatedNodeVersion,
			mutate: func(t *testing.T, _, node string) {
				if err := os.WriteFile(node+".sha256", []byte(strings.Repeat("0", 64)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			reason: []string{"is not the node this kiro-cli bundles", "node.sha256 " + strings.Repeat("0", 64)},
		},
		{
			name: "no node extracted yet", version: kasnode.ValidatedNodeVersion,
			mutate: func(t *testing.T, _, node string) {
				if err := os.Remove(node + ".sha256"); err != nil {
					t.Fatal(err)
				}
			},
			reason: []string{"has not extracted its node yet"},
		},
		{
			name: "a kiro-cli with no node hash", version: kasnode.ValidatedNodeVersion,
			mutate: func(t *testing.T, cli, _ string) {
				if err := os.WriteFile(cli, []byte("KIRO_KAS_SERVER_PATH"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			reason: []string{"cannot read the node hash"},
		},
		{
			name: "an operator's own KIRO_KAS_NODE_PATH", version: kasnode.ValidatedNodeVersion,
			mutate: func(t *testing.T, _, _ string) { t.Setenv(kasnode.NodePathVar, "/opt/node") },
			reason: []string{kasnode.NodePathVar, "/opt/node"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli, node := kasNodeFixture(t, tc.version)
			if tc.mutate != nil {
				tc.mutate(t, cli, node)
			}
			logs := capture.Default(t)
			e := kasNodeEngine(t, cli, true)
			env := e.processEnv()
			if v, ok := envValue(env, kasnode.LaunchVar); ok {
				t.Errorf("%s = %q, want unset: the check failed", kasnode.LaunchVar, v)
			}
			if v, _ := envValue(env, kasnode.NodePathVar); v == fakeLauncher {
				t.Errorf("%s names the launcher, want kiro-cli's own node", kasnode.NodePathVar)
			}
			if e.nodeArgs != nil {
				t.Errorf("nodeArgs = %q, want nil", e.nodeArgs)
			}
			if n := logs.CountLevel(slog.LevelWarn, "starting without the high throughput optimizations"); n != 1 {
				t.Fatalf("%d warnings, want one", n)
			}
			for _, sub := range tc.reason {
				if !logs.AttrContains("starting without the high throughput optimizations", "reason", sub) {
					t.Errorf("the warning's reason does not name %q", sub)
				}
			}
		})
	}
}
