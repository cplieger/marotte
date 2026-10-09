package agent

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
)

type liveSettings struct {
	notifications     map[marotte.PushKind]bool
	ignoreFiles       []string
	terminalTimeoutMs int
	// readable is false over an unparseable document, whose readers all answer their defaults, or one that never
	// held still across a read: nothing is pushed.
	readable        bool
	ignoreReadable  bool
	mcpWaitForReady bool
	debugLogs       bool
}

// liveReadAttempts bounds the re-reads of a document a writer keeps replacing; a shell redirect settles in one.
const liveReadAttempts = 3

// readLiveSettings takes every field from one generation of config.json: the field readers each consult the
// cache, so a write landing between two of them would otherwise mix two documents.
func readLiveSettings(ctx context.Context, configDir string, fields func(context.Context, string) liveSettings) liveSettings {
	// sessionFingerprint owns why the reads drop the caller's cancellation.
	ctx = context.WithoutCancel(ctx)
	var now liveSettings
	for range liveReadAttempts {
		before, readable := settings.Generation(ctx, configDir)
		now = fields(ctx, configDir)
		if after, _ := settings.Generation(ctx, configDir); after == before {
			now.readable = readable
			return now
		}
	}
	now.readable = false
	return now
}

func readLiveFields(ctx context.Context, configDir string) liveSettings {
	files, ignoreReadable := readAgentIgnoreFiles(ctx, configDir)
	mcpWait, _ := settings.MCPWaitForReady(ctx, configDir)
	return liveSettings{
		ignoreFiles:       files,
		ignoreReadable:    ignoreReadable,
		terminalTimeoutMs: terminalCommandTimeoutMs(ctx, configDir),
		mcpWaitForReady:   mcpWait,
		debugLogs:         settings.DebugLogs(ctx, configDir),
		notifications:     push.ResolvePreferences(ctx, configDir),
	}
}

func (rt *Runtime) liveSettings(ctx context.Context) liveSettings {
	return readLiveSettings(ctx, rt.lifecycle.configDir, readLiveFields)
}

// surfaceLock serializes the live pushes to one surface and is ready at its zero value. A channel rather than a
// sync.Mutex, so a waiter can give up with its caller and testing/synctest counts a parked waiter as durably blocked.
type surfaceLock struct {
	ch   chan struct{}
	once sync.Once
}

// lock takes a free lock even after ctx ended: a sync whose caller left still delivers what it found.
func (l *surfaceLock) lock(ctx context.Context) bool {
	l.once.Do(func() { l.ch = make(chan struct{}, 1) })
	select {
	case l.ch <- struct{}{}:
		return true
	default:
	}
	select {
	case l.ch <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *surfaceLock) unlock() { <-l.ch }

// heldContext is the context a lock holder pushes under: once it holds the lock, its caller leaving is no push
// failure to log or retry.
func heldContext(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// bridgeLive is what one chat or run process last confirmed of the ignore list and the shell timeout; nil files or
// !terminalKnown is unconfirmed. Content collection's record is the bridge's own (RefreshContentCollection).
type bridgeLive struct {
	ignoreFiles       []string
	terminalTimeoutMs int
	terminalKnown     bool
}

// startedLive is what b's Start left its process holding; read only after Start returned nil.
func startedLive(b utilityBridge) bridgeLive {
	files, ms := b.StartLive()
	return bridgeLive{ignoreFiles: slices.Clone(files), terminalTimeoutMs: ms, terminalKnown: true}
}

func (sb *sharedBridge) adoptSpawn() {
	live := startedLive(sb.current())
	sb.liveLock.lock(context.Background())
	sb.live = live
	sb.liveLock.unlock()
}

// syncLive sends sb each live setting the document holds that its process has not confirmed. The document is read
// under sb's lock, so a later push never carries an older read. A spawning bridge is skipped: its spawn reads the
// document itself and adoptSpawn records what it delivered. A failed push keeps the record, so the next use resends.
func (sb *sharedBridge) syncLive(ctx context.Context, chatID marotte.ChatID, read func(context.Context) liveSettings) {
	if !sb.liveLock.lock(ctx) {
		return
	}
	defer sb.liveLock.unlock()
	ctx = heldContext(ctx)
	if !sb.startedPastSpawn() {
		return
	}
	refreshContentCollection(ctx, sb.current(), "chat_id", chatID)
	now := read(ctx)
	if !now.readable {
		return
	}
	if now.ignoreReadable && !slices.Equal(sb.live.ignoreFiles, now.ignoreFiles) &&
		sb.notifyLive(ctx, chatID, marotte.MethodPolicyIgnoreFilesChanged, map[string]any{marotte.ParamIgnoreFiles: now.ignoreFiles}) {
		sb.live.ignoreFiles = now.ignoreFiles
	}
	if (!sb.live.terminalKnown || sb.live.terminalTimeoutMs != now.terminalTimeoutMs) &&
		sb.notifyLive(ctx, chatID, marotte.MethodTerminalSettingsChanged, marotte.TerminalSettingsParams(now.terminalTimeoutMs)) {
		sb.live.terminalTimeoutMs, sb.live.terminalKnown = now.terminalTimeoutMs, true
	}
}

func (sb *sharedBridge) notifyLive(ctx context.Context, chatID marotte.ChatID, method string, params any) bool {
	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	if err := sb.Notify(cctx, method, params); err != nil {
		slog.Warn("live setting not pushed; it is resent at the next chat message or settings write",
			"chat_id", chatID, "method", method, "error", err)
		return false
	}
	return true
}

// refreshContentCollection sends whatever the document's readability: the bridge's resolver answers an organization
// lock over an unreadable config.json.
func refreshContentCollection(ctx context.Context, b utilityBridge, attrs ...any) {
	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	if enabled, err := b.RefreshContentCollection(cctx); err != nil {
		slog.Error("content collection not pushed; model requests from this process may still be opted in",
			append(attrs, "enabled", enabled, "error", err)...)
	}
}

func (us *utilitySession) syncLive(ctx context.Context, read func(context.Context) liveSettings) {
	if !us.liveLock.lock(ctx) {
		return
	}
	defer us.liveLock.unlock()
	ctx = heldContext(ctx)
	us.mu.Lock()
	running, b, gen, sent := us.started, us.bridge, us.gen, us.liveIgnore
	us.mu.Unlock()
	if !running || b == nil {
		return
	}
	refreshContentCollection(ctx, b, "session", "utility")
	now := read(ctx)
	if !now.readable {
		return
	}
	if us.hooks.ignoreFiles != nil && now.ignoreReadable && !slices.Equal(sent, now.ignoreFiles) {
		cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
		err := b.Notify(cctx, marotte.MethodPolicyIgnoreFilesChanged, map[string]any{marotte.ParamIgnoreFiles: now.ignoreFiles})
		cancel()
		if err != nil {
			slog.Warn("live setting not pushed; it is resent at the next chat message or settings write",
				"session", "utility", "method", marotte.MethodPolicyIgnoreFilesChanged, "error", err)
		} else {
			us.recordIgnore(gen, now.ignoreFiles)
		}
	}
}

func (us *utilitySession) recordIgnore(gen uint64, files []string) {
	us.mu.Lock()
	defer us.mu.Unlock()
	if us.started && us.gen == gen {
		us.liveIgnore = files
	}
}

// processLive serializes the syncs of the settings this server applies itself (log level, notification toggles)
// and of the KAS MCP file it renders. Each sync compares the document with what its collaborator reports in force,
// so a value applied before the runtime existed, or never applied, is compared as it stands.
type processLive struct {
	lock surfaceLock
	// ignoreUnreadable is the last sync's verdict; unreadableReported is set once that spell was reported, so it is
	// reported once, not at every use.
	ignoreUnreadable   bool
	unreadableReported bool
}

// syncProcessLive gives up once ignorePushTimeout or ctx's deadline passes, whichever is first, across the lock wait and
// the MCP render alike; what it skipped is retried at the next chat message or settings write.
func (rt *Runtime) syncProcessLive(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	p := &rt.processLive
	if !p.lock.lock(ctx) {
		warnIfLockWaitTimedOut(ctx, "settings: another live-settings sync held the lock past the bound; "+
			"the MCP render, log level, notification preferences and the unreadable agent ignore list report are retried at the next "+
			"chat message or settings write")
		return
	}
	defer p.lock.unlock()
	deadline, _ := ctx.Deadline()
	ctx = heldContext(ctx)
	now := rt.liveSettings(ctx)
	rt.noteIgnoreReadable(ctx, now.ignoreReadable)
	if !now.readable {
		return
	}
	if rt.mcpRender != nil {
		if rendered, known := rt.mcpRender.RenderedWaitForReady(); !known || rendered != now.mcpWaitForReady {
			rctx, rcancel := context.WithDeadline(ctx, deadline)
			err := rt.mcpRender.RenderKASConfig(rctx)
			rcancel()
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				slog.Warn("settings: the MCP render waited past the bound; it is retried at the next chat message or settings write",
					"error", err)
			case err != nil:
				slog.Error("settings: re-rendering the MCP config failed; it is retried at the next chat message or settings write",
					"error", err)
			}
		}
	}
	if rt.debugLogs != nil && rt.setDebugLogs != nil && rt.debugLogs() != now.debugLogs {
		rt.setDebugLogs(now.debugLogs)
	}
	if rt.push != nil && !maps.Equal(rt.push.Preferences(), now.notifications) {
		rt.push.SetPreferences(now.notifications)
	}
}

// reportUnreadableOwed re-reads the ignore list once an open's own process is live, so the report describes the
// document that process was spawned over rather than the one the pre-spawn sync read. Like syncProcessLive it waits
// for the lock until ignorePushTimeout or ctx's deadline, whichever is first; a report it skips is owed again at the
// next open.
func (rt *Runtime) reportUnreadableOwed(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	p := &rt.processLive
	if !p.lock.lock(ctx) {
		warnIfLockWaitTimedOut(ctx, "settings: another live-settings sync held the lock past the bound; "+
			"an unreadable agent ignore list is reported at the next chat open")
		return
	}
	defer p.lock.unlock()
	ctx = heldContext(ctx)
	_, readable := readAgentIgnoreFiles(ctx, rt.lifecycle.configDir)
	rt.noteIgnoreReadable(ctx, readable)
}

// warnIfLockWaitTimedOut logs msg when a lock wait under ctx ended on its deadline; a caller who left needs no line.
func warnIfLockWaitTimedOut(ctx context.Context, msg string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.Warn(msg)
	}
}

// noteIgnoreReadable runs under processLive.lock.
func (rt *Runtime) noteIgnoreReadable(ctx context.Context, readable bool) {
	p := &rt.processLive
	p.ignoreUnreadable = !readable
	if readable {
		p.unreadableReported = false
	}
	if p.ignoreUnreadable && !p.unreadableReported {
		p.unreadableReported = rt.reportIgnoreFilesUnreadable(ctx)
	}
}

func (rt *Runtime) pushLiveToAll(ctx context.Context) {
	rt.syncLiveExcept(ctx, liveSurface{})
}

type liveSurface struct {
	chat    marotte.ChatID
	utility bool
}

func (rt *Runtime) syncLiveExcept(ctx context.Context, skip liveSurface) {
	var wg sync.WaitGroup
	for chatID, sb := range rt.bridge.mgr.all() {
		if skip.chat != "" && chatID == skip.chat {
			continue
		}
		wg.Go(func() { sb.syncLive(ctx, chatID, rt.liveSettings) })
	}
	if u := rt.utility.peek(); u != nil && !skip.utility {
		wg.Go(func() { u.session.syncLive(ctx, rt.liveSettings) })
	}
	wg.Wait()
}

// liveFanout runs the passes that carry a hand edit found at one process's use to every other live process, off
// that use's path so a wedged process delays no open. One pass runs at a time; requests made during it coalesce
// into one more pass, which skips a process only when every coalesced request had synced it already.
type liveFanout struct {
	skip    liveSurface
	mu      sync.Mutex
	running bool
	queued  bool
}

func (f *liveFanout) request(skip liveSurface) (startRunner bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case !f.queued:
		f.skip, f.queued = skip, true
	case f.skip != skip:
		f.skip = liveSurface{}
	}
	if f.running {
		return false
	}
	f.running = true
	return true
}

func (f *liveFanout) takeOrStop() (skip liveSurface, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.queued {
		f.running = false
		return liveSurface{}, false
	}
	skip, f.skip, f.queued = f.skip, liveSurface{}, false
	return skip, true
}

func (f *liveFanout) abandon() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skip, f.queued, f.running = liveSurface{}, false, false
}

// fanOutLive drops its pass at shutdown, which stops every process the pass would sync.
func (rt *Runtime) fanOutLive(skip liveSurface) {
	if !rt.fanout.request(skip) {
		return
	}
	if !rt.lifecycle.goUnlessDraining(rt.runFanOut) {
		rt.fanout.abandon()
	}
}

func (rt *Runtime) runFanOut() {
	for {
		skip, ok := rt.fanout.takeOrStop()
		if !ok {
			return
		}
		rt.syncLiveExcept(rt.lifecycle.shutdownCtx, skip)
	}
}
