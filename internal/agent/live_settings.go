package agent

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
)

type liveSettings struct {
	notifications     map[marotte.PushKind]bool
	ignoreFiles       []string
	terminalTimeoutMs int
	// readable is false over a document that cannot be read or parsed: nothing is pushed.
	readable        bool
	ignoreReadable  bool
	mcpWaitForReady bool
	debugLogs       bool
}

// readLiveSettings takes every field from one parsed generation of config.json (readSnapshot); over an unreadable one
// it decodes nothing.
func readLiveSettings(ctx context.Context, configDir string, fields func(settings.Document) liveSettings) liveSettings {
	doc, _, readable := readSnapshot(ctx, configDir)
	if !readable {
		return liveSettings{}
	}
	now := fields(doc)
	now.readable = true
	return now
}

func readLiveFields(d settings.Document) liveSettings {
	files, ignoreErr := readAgentIgnoreFiles(d)
	return liveSettings{
		ignoreFiles:       files,
		ignoreReadable:    ignoreErr == nil,
		terminalTimeoutMs: terminalCommandTimeoutMs(d),
		mcpWaitForReady:   settings.MCPWaitForReadyIn(d),
		debugLogs:         settings.DebugLogsIn(d),
		notifications:     push.ResolvePreferences(d),
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

// engineLive is what the engine last confirmed of the ignore list and the shell timeout; nil files or !terminalKnown
// is unconfirmed. Content collection's record is the engine's own (RefreshContentCollection).
type engineLive struct {
	ignoreFiles       []string
	terminalTimeoutMs int
	terminalKnown     bool
}

// syncLive sends the engine each live setting the document holds that it has not confirmed, once for every session.
// The document is read under the engine's live lock, so a later push never carries an older read. A down engine reads
// the document itself at its spawn. A failed push keeps the record, so the next use resends.
func (o *engineOwner) syncLive(ctx context.Context, read func(context.Context) liveSettings) {
	if !o.liveLock.lock(ctx) {
		return
	}
	defer o.liveLock.unlock()
	ctx = heldContext(ctx)
	h, gen := o.current()
	if h == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	if enabled, err := h.RefreshContentCollection(cctx); err != nil {
		slog.Error("content collection not pushed; model requests may still be opted in",
			"generation", gen, "enabled", enabled, "error", err)
	}
	cancel()
	now := read(ctx)
	if !now.readable {
		return
	}
	if now.ignoreReadable && !slices.Equal(o.live.ignoreFiles, now.ignoreFiles) &&
		notifyLive(ctx, h, marotte.MethodPolicyIgnoreFilesChanged, map[string]any{marotte.ParamIgnoreFiles: now.ignoreFiles}) {
		o.live.ignoreFiles = now.ignoreFiles
	}
	if (!o.live.terminalKnown || o.live.terminalTimeoutMs != now.terminalTimeoutMs) &&
		notifyLive(ctx, h, marotte.MethodTerminalSettingsChanged, marotte.TerminalSettingsParams(now.terminalTimeoutMs)) {
		o.live.terminalTimeoutMs, o.live.terminalKnown = now.terminalTimeoutMs, true
	}
}

func notifyLive(ctx context.Context, h engineHandle, method string, params any) bool {
	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	if err := h.Notify(cctx, method, params); err != nil {
		slog.Warn("live setting not pushed; it is resent at the next chat message or settings write",
			"method", method, "error", err)
		return false
	}
	return true
}

// processLive serializes the syncs of the settings this server applies itself (log level, notification toggles)
// and of the KAS MCP file it renders. Each sync compares the document with what its collaborator reports in force,
// so a value applied before the runtime existed, or never applied, is compared as it stands.
type processLive struct {
	lock surfaceLock
	// reported is what the unreadable ignore list report last said, so a spell is reported once per answer, not at
	// every use; ignoreReportNone while the list reads.
	reported ignoreReport
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
			case errors.Is(err, mcp.ErrKASUnreadable):
				// The store reports each unreadable file state once.
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

// warnIfLockWaitTimedOut logs msg when a lock wait under ctx ended on its deadline; a caller who left needs no line.
func warnIfLockWaitTimedOut(ctx context.Context, msg string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.Warn(msg)
	}
}

// noteIgnoreReadable runs under processLive.lock.
func (rt *Runtime) noteIgnoreReadable(ctx context.Context, readable bool) {
	p := &rt.processLive
	if readable {
		p.reported = ignoreReportNone
		return
	}
	p.reported = rt.reportIgnoreFilesUnreadable(ctx, p.reported)
}
