package composition

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

const toolsManifestName = "tools.json"

// ownedFilePoll is how often the watches read config.json and tools.json for an outside write.
// An absent or unreadable read is acted on at the second consecutive one, so this is also how
// long an editor's rename-away save may leave the file missing without effect.
const ownedFilePoll = 2 * time.Second

// settingsReactor is what a config.json owner tells after a write: every page, and the runtime's
// after-write rule. HoldUnlessDraining is the runtime's in-flight wait the config watch runs under.
type settingsReactor interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
	SettingsWritten(ctx context.Context, keys []string)
	HoldUnlessDraining() (release func(), ok bool)
}

var _ settingsReactor = (*agent.Runtime)(nil)

// ownedFileDeps are the owners the file browser routes an owned file's writes through.
type ownedFileDeps struct {
	Config *configWatch
	Tools  *toolsSlot
	Dirs   settings.AgentDirs
}

// ownedFileOptions registers every file marotte owns with the file browser. The agent's file
// tools never reach them, because no workspace or additional directory may cover the config
// directory (validateConfig, settings.AgentDirs).
func ownedFileOptions(d ownedFileDeps) []filebrowse.Option {
	return []filebrowse.Option{
		filebrowse.WithOwner(filepath.Join(d.Dirs.Config, settings.Filename), configOwner{dirs: d.Dirs, watch: d.Config}),
		filebrowse.WithOwner(d.Tools.manifest, d.Tools),
	}
}

// configOwner is the editor's save of config.json: settings.Replace, then the after-write rule
// queued on the config watch's worker, so the save's answer does not wait for it.
type configOwner struct {
	watch *configWatch
	dirs  settings.AgentDirs
}

var _ filebrowse.Owner = configOwner{}

func (o configOwner) Replace(ctx context.Context, content []byte, check func([]byte) error) ([]byte, error) {
	written, changed, err := settings.Replace(ctx, o.dirs, content, check, o.watch.reactor.Broadcast)
	if errors.Is(err, settings.ErrRefused) {
		return nil, filebrowse.SaveRefusalError{Err: err}
	}
	if err != nil {
		return nil, err
	}
	if changed == nil {
		o.watch.effects.queueAll()
	} else {
		o.watch.effects.queue(changed)
	}
	return written, nil
}

// settingsEffects is the config.json after-write worker's queue: the keys pending, and all for a
// change whose keys are unknown. signal holds at most one wake.
type settingsEffects struct {
	keys   map[string]struct{}
	signal chan struct{}
	mu     sync.Mutex
	all    bool
}

func newSettingsEffects() *settingsEffects {
	return &settingsEffects{keys: map[string]struct{}{}, signal: make(chan struct{}, 1)}
}

func (e *settingsEffects) queue(keys []string) {
	if len(keys) == 0 {
		return
	}
	e.mu.Lock()
	for _, k := range keys {
		e.keys[k] = struct{}{}
	}
	e.mu.Unlock()
	e.wake()
}

func (e *settingsEffects) queueAll() {
	e.mu.Lock()
	e.all = true
	e.mu.Unlock()
	e.wake()
}

func (e *settingsEffects) wake() {
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

// take empties the queue. run is false when nothing is pending; keys is nil only when all was set.
func (e *settingsEffects) take() (keys []string, run bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.keys) == 0 && !e.all {
		return nil, false
	}
	if !e.all {
		keys = slices.Sorted(maps.Keys(e.keys))
	}
	e.keys, e.all = map[string]struct{}{}, false
	return keys, true
}

// configWatch picks up an outside write of config.json and runs every config.json effect queued
// on its worker, one at a time on its own loop.
type configWatch struct {
	reactor   settingsReactor
	effects   *settingsEffects
	configDir string
}

func newConfigWatch(configDir string, reactor settingsReactor) *configWatch {
	return &configWatch{reactor: reactor, effects: newSettingsEffects(), configDir: configDir}
}

// pass is one outside-write check followed by a drain, both held on the runtime's in-flight wait
// so neither reaches a runtime whose Shutdown began.
func (w *configWatch) pass(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	release, ok := w.reactor.HoldUnlessDraining()
	if !ok {
		return
	}
	defer release()
	changed, applies, err := settings.Poll(ctx, w.configDir, w.reactor.Broadcast)
	switch {
	case err != nil:
		slog.Debug("settings: config.json watch pass failed", "error", err)
	case applies && changed == nil:
		w.effects.queueAll()
	case applies:
		w.effects.queue(changed)
	}
	w.drainHeld(ctx)
}

// drain runs the queued effects, held as pass is.
func (w *configWatch) drain(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	release, ok := w.reactor.HoldUnlessDraining()
	if !ok {
		return
	}
	defer release()
	w.drainHeld(ctx)
}

func (w *configWatch) drainHeld(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if keys, run := w.effects.take(); run {
		w.reactor.SettingsWritten(ctx, keys)
	}
}

// startConfigWatch runs the config.json watch until its stop, which cancels it and then waits,
// with no grace give-up, for a pass or an effect in flight: SettingsWritten pushes to the engine
// past its context's end (agent's resolveLock and live pushes), and must not run on into Shutdown.
func startConfigWatch(ctx context.Context, w *configWatch) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(ownedFilePoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.pass(ctx)
			case <-w.effects.signal:
				w.drain(ctx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// startToolsWatch runs the tools.json watch until its stop; Close waits out a tick in flight on
// the slot's lock.
func startToolsWatch(ctx context.Context, tools *toolsSlot) (stop func()) {
	return runBackground(ctx, "tools.json watch", func(ctx context.Context) {
		ticker := time.NewTicker(ownedFilePoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tools.tick()
			}
		}
	})
}
