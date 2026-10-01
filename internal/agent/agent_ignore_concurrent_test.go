package agent

// A settings write that lands while a bridge is mid-spawn reaches that bridge, and
// the only channel it can reach it by is the spawn's OWN send.
//
// StartOpts.IgnoreFiles is a RESOLVER rather than a slice for exactly this window.
// The eager shape read the list where the StartOpts literal is written, before
// `initialize`, so a concurrent save was overwritten by the pre-save list for the
// connection's whole life: the push cannot help, because a bridge that has not
// finished `initialize` refuses the write (bridge.ErrBridgeNotStarted), and nothing
// re-sends the list afterwards.

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

// errProbeNotStarted mirrors bridge.ErrBridgeNotStarted, the refusal a write takes
// before `initialize` has run. Modelling it is what makes the test tell the whole
// mechanism rather than only its remedy: without it the push looks like a second
// channel that could have carried the new list.
var errProbeNotStarted = errors.New("bridge not started")

// ignoreProbeBridge parks inside Start where the real bridge sits during
// `initialize`, then resolves StartOpts.IgnoreFiles at the point
// bridge_process.go's applyIgnoreFiles would. It embeds the shared fake so it
// satisfies ACPBridge without restating it, and overrides the two methods whose
// timing is the subject.
type ignoreProbeBridge struct {
	*fakeBridge
	// arrival fires once Start has been handed its StartOpts, so the test knows the
	// literal has been evaluated without polling for it.
	arrival chan struct{}
	once    sync.Once
	// release holds the spawn open; closing it is the moment `initialize` returns.
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
	// Only the FIRST spawn is recorded: one factory serves every bridge in this
	// runtime, so a later spawn would overwrite the answer under test.
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

// writeIgnoreFiles rewrites the whole settings document, which is what the PATCH
// handler's own save does.
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

// A save landing between the StartOpts literal and `initialize` reaches the
// spawning bridge, because the list is resolved at SEND time.
//
// The refusal count is asserted alongside, because it is the half that makes the
// resolver necessary: PushAgentIgnoreFiles does reach into the bridge map (the
// record is inserted before Start, so concurrent opens coalesce) and its write is
// refused, so the spawn's own send is the only channel left.
func TestSpawnIgnoreFiles_ConcurrentSaveReachesASpawningBridge(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{"pre.ignore"})

	probe := newIgnoreProbeBridge()
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return probe }, cs, WithConfigDir(dir))
	cs.wire(h)
	h.mcpRegistry.SignalReady()

	released := sync.OnceFunc(func() { close(probe.release) })
	// Released on every exit path: a parked spawn would otherwise hold Shutdown's
	// inflight wait for its whole budget, turning one failed assertion into a hang.
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
