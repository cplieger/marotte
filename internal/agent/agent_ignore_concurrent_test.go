package agent

// A settings save landing during the engine's spawn reaches it only through the spawn's own send, since a live push
// skips an engine that is not ready: EngineOpts.IgnoreFiles and TerminalTimeout are resolvers read at send time.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func writeIgnoreFiles(t *testing.T, dir string, entries []string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{settings.KeyAgentIgnoreFiles: entries})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), body, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

// engineSpawnProbe holds the engine's Start at its gate until released, and reports when Start was entered.
type engineSpawnProbe struct {
	src      *fakeEngineSource
	arrival  chan struct{}
	release  chan struct{}
	released func()
}

// newSpawnProbeHub builds a runtime whose engine spawn parks until the probe is released; save runs mid-spawn.
func newSpawnProbeHub(t *testing.T, dir string) (*Runtime, *engineSpawnProbe) {
	t.Helper()
	p := &engineSpawnProbe{arrival: make(chan struct{}), release: make(chan struct{})}
	p.released = sync.OnceFunc(func() { close(p.release) })
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.startGate = p.release
	src.onStart = sync.OnceFunc(func() { close(p.arrival) })
	p.src = src
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), src, cs, WithConfigDir(dir))
	cs.wire(h)
	// Released on every exit path, or a parked spawn turns a failed assertion into a hang.
	t.Cleanup(func() { p.released(); shutdownHub(t, h) })
	return h, p
}

// TestEngineSpawn_AConcurrentIgnoreSaveReachesTheSpawningEngine pins send-time resolution: no push reached the engine
// before its Start returned, so only the spawn's own send can carry the new list.
func TestEngineSpawn_AConcurrentIgnoreSaveReachesTheSpawningEngine(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{"pre.ignore"})
	h, probe := newSpawnProbeHub(t, dir)

	<-probe.arrival
	writeIgnoreFiles(t, dir, []string{"post.ignore"})
	h.ReconcileSessionSettings(t.Context())
	if got := probe.src.lane.notified(marotte.MethodPolicyIgnoreFilesChanged); len(got) != 0 {
		t.Errorf("ignore-list pushes written before the engine was ready = %v, want none", got)
	}

	probe.released()
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	want := settings.AgentIgnoreList([]string{"post.ignore"})
	if got, _ := probe.src.current().StartLive(); !slices.Equal(got, want) {
		t.Errorf("list the spawn sent = %v, want %v: IgnoreFiles must resolve at send time", got, want)
	}
}

// A mid-spawn timeout save reaches the engine through its own resolver.
func TestEngineSpawn_AConcurrentTimeoutSaveReachesTheSpawningEngine(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 30000}`)
	h, probe := newSpawnProbeHub(t, dir)

	<-probe.arrival
	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 300000}`)
	h.ReconcileSessionSettings(t.Context())
	if got := probe.src.lane.notified(marotte.MethodTerminalSettingsChanged); len(got) != 0 {
		t.Errorf("shell-timeout pushes written before the engine was ready = %v, want none", got)
	}

	probe.released()
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	if _, ms := probe.src.current().StartLive(); ms != 300000 {
		t.Errorf("timeout the spawn resolved = %d, want 300000: TerminalTimeout must read at send time", ms)
	}
}

// A settings write sends the shell timeout once, on the engine, however many sessions are open.
func TestSettingsWrite_SendsTheShellTimeoutOnceOnTheEngine(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 30000}`)
	cs := newTestChatStore()
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	h := newRuntimeOn(context.Background(), t.TempDir(), src, cs, WithConfigDir(dir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, id := range []marotte.ChatID{"c1", "c2"} {
		if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool { c.Name = string(id); return true }); err != nil {
			t.Fatalf("seed chat %s: %v", id, err)
		}
		if _, err := h.coord.openBridge(t.Context(), id, ""); err != nil {
			t.Fatalf("OpenBridge %s: %v", id, err)
		}
	}
	before := len(src.lane.notified(marotte.MethodTerminalSettingsChanged))

	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 45000}`)
	h.ReconcileSessionSettings(t.Context())

	frames := src.lane.notified(marotte.MethodTerminalSettingsChanged)[before:]
	want := `{"terminal":{"commandTimeoutMs":45000,"enabled":true}}`
	if len(frames) != 1 || frames[0] != want {
		t.Errorf("engine shell-timeout frames = %v, want exactly [%s]", frames, want)
	}
}
