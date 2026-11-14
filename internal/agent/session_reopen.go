package agent

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// markSessionsForReload is the one way a setting a session reads only at its door reaches resident chat sessions:
// each entry's life is marked (chatLifecycle.markReload) and closes from the chat's next door (reloadIfMarked). A
// run's host session is never marked: it keeps the presets its lease recorded at launch.
func (rt *Runtime) markSessionsForReload(reason string) {
	marked := 0
	for chatID, sb := range rt.bridge.mgr.all() {
		if lc, ok := rt.coord.turns.closeOwner(chatID, sb); ok && lc.markReload(sb) {
			marked++
		}
	}
	slog.Info("open chats reload their session at their next message", "reason", reason, "marked", marked)
}

// SecurityProfileChanged applies a persisted Settings profile change, the default for new chats: it reloads no
// resident session, since each chat keeps the profile its header names. A parentless run keeps the presets its
// lease recorded at launch.
func (rt *Runtime) SecurityProfileChanged(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "security profile")
	rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "",
		marotte.PermissionsChangedPayload{}))
}

// SettingsWritten is the after-write rule for a config.json write that brings none of its own. keys are the keys
// whose value changed; nil means either side did not parse, so any key may have moved.
func (rt *Runtime) SettingsWritten(ctx context.Context, keys []string) {
	if keys == nil || slices.Contains(keys, settings.KeySecurityProfile) {
		rt.SecurityProfileChanged(ctx)
		return
	}
	rt.ReconcileSessionSettings(ctx)
}

// sessionBaseline holds the session-door fingerprints every resident session opened under, or was marked to
// reload from. Set at construction, when none is resident yet.
type sessionBaseline struct {
	chat string
	mu   sync.Mutex
}

// ReconcileSessionSettings marks resident sessions for reload when a setting they read only at their door moved
// since the last reconcile, recomputes whether an engine restart is pending, and pushes the engine each live setting
// it has not confirmed, and returns once each push landed or failed. A config.json writer without its own after-write
// rule reaches it through SettingsWritten, which goes through SecurityProfileChanged when security_profile moved; a
// cli.json write calls it directly. An outside edit of config.json reaches it through the config.json watch's
// settings.Poll.
func (rt *Runtime) ReconcileSessionSettings(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "session settings")
}

func (rt *Runtime) reconcileSettingsWrite(ctx context.Context, reason string) {
	deadline := time.Now().Add(ignorePushTimeout)
	rt.reconcileSessionSettings(ctx, reason, deadline)
	syncCtx, cancel := context.WithDeadline(ctx, deadline)
	rt.engine.syncLive(syncCtx, rt.liveSettings)
	cancel()
	rt.standing.refetchSoon()
}

// reconcileSessionSettings marks sessions for reload as the fingerprints moved, asks the engine owner whether a
// restart is now pending, and syncs what this server applies itself, waiting no later than deadline.
func (rt *Runtime) reconcileSessionSettings(ctx context.Context, reason string, deadline time.Time) {
	b := &rt.sessionSeen
	b.mu.Lock()
	chat, ok := rt.sessionFingerprint(ctx)
	reload := ok && chat != b.chat
	if ok {
		b.chat = chat
	}
	if reload {
		rt.markSessionsForReload(reason)
	}
	b.mu.Unlock()
	rt.engine.reconcile(ctx)
	syncCtx, cancel := context.WithDeadline(ctx, deadline)
	rt.syncProcessLive(syncCtx)
	cancel()
}
