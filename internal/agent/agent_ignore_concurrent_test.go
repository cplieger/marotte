package agent

// A settings save landing mid-spawn reaches that bridge only through the spawn's own
// send: the push is refused before `initialize`, which is why StartOpts.IgnoreFiles is a resolver.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// errProbeNotStarted mirrors bridge.ErrBridgeNotStarted, the refusal a write takes before `initialize`.
var errProbeNotStarted = errors.New("bridge not started")

// ignoreProbeBridge parks inside Start, then resolves StartOpts.IgnoreFiles where applyIgnoreFiles would.
type ignoreProbeBridge struct {
	*fakeBridge
	// arrival fires once Start has its StartOpts.
	arrival chan struct{}
	once    sync.Once
	// release holds the spawn open; closing it is `initialize` returning.
	release chan struct{}

	mu       sync.Mutex
	started  bool
	resolved []string
	refused  int
}

func newIgnoreProbeBridge() *ignoreProbeBridge {
	return &ignoreProbeBridge{
		fakeBridge: newFakeBridge(),
		arrival:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (b *ignoreProbeBridge) Start(ctx context.Context, opts *marotte.StartOpts) error {
	b.once.Do(func() { close(b.arrival) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	first := !b.started
	b.started = true
	b.mu.Unlock()
	// Only the first spawn is recorded: one factory serves every bridge here.
	if first && opts.IgnoreFiles != nil {
		files := opts.IgnoreFiles(ctx)
		b.mu.Lock()
		b.resolved = files
		b.mu.Unlock()
	}
	return b.fakeBridge.Start(ctx, opts)
}

func (b *ignoreProbeBridge) Notify(_ context.Context, _ string, _ any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		b.refused++
		return errProbeNotStarted
	}
	return nil
}

func (b *ignoreProbeBridge) resolvedIgnoreFiles() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.resolved)
}

func (b *ignoreProbeBridge) refusedNotifies() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refused
}

// writeIgnoreFiles rewrites the whole settings document, as the PATCH handler's save does.
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

// TestSpawnIgnoreFiles_ConcurrentSaveReachesASpawningBridge pins send-time resolution;
// the refusal count proves the push could not have carried the new list.
func TestSpawnIgnoreFiles_ConcurrentSaveReachesASpawningBridge(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{"pre.ignore"})

	probe := newIgnoreProbeBridge()
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return probe }, cs, WithConfigDir(dir))
	cs.wire(h)

	released := sync.OnceFunc(func() { close(probe.release) })
	// Released on every exit path, or a parked spawn turns a failed assertion into a hang.
	t.Cleanup(func() { released(); shutdownHub(t, h) })

	ctx := t.Context()
	if _, err := cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	opened := make(chan error, 1)
	go func() {
		_, err := h.coord.OpenBridge(ctx, "c1", "")
		opened <- err
	}()

	<-probe.arrival // the StartOpts literal has been evaluated

	writeIgnoreFiles(t, dir, []string{"post.ignore"})
	h.PushAgentIgnoreFiles(ctx)

	released()
	if err := <-opened; err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := probe.refusedNotifies(); got != 1 {
		t.Errorf("notifies refused mid-spawn = %d, want 1: the push reaches the bridge and cannot write to it, which is why the spawn's own send has to carry the new list", got)
	}
	want := settings.AgentIgnoreList([]string{"post.ignore"})
	if got := probe.resolvedIgnoreFiles(); !slices.Equal(got, want) {
		t.Errorf("list the spawn sent = %v, want %v: IgnoreFiles must resolve at send time, not where the StartOpts literal is written", got, want)
	}
}
