package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/securityprofile"
)

// markSessionsForReload is the one way a setting a session reads only at its door reaches resident chat sessions:
// each is marked, and closes and reloads at its next idle open (reloadIfMarked). A busy chat reloads after its turn,
// one hosting a live run step after that step's turn. A run's host session is never marked: it keeps the presets its
// lease recorded at launch.
func (rt *Runtime) markSessionsForReload(reason string) {
	marked := rt.bridge.mgr.markSessionsForReload()
	slog.Info("open chats reload their session at their next message", "reason", reason, "marked", marked)
}

// SecurityProfileChanged applies a persisted profile change: presets ride the session door and KAS has no live
// setter, so resident sessions reload. A parentless run keeps the presets its lease recorded at launch.
func (rt *Runtime) SecurityProfileChanged(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "security profile")
	rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "",
		marotte.PermissionsChangedPayload{Status: "success"}))
}

// EnsureCustomProfile runs Customize unless the profile already is Custom, so a rule an answer
// saves lands in the policy the picker names. It returns once the user file is written, and
// leaves both files as they were on any refusal but securityprofile.ErrRestoreFailed.
func (rt *Runtime) EnsureCustomProfile(ctx context.Context) error {
	store := &securityprofile.Store{
		List:      rt.config.PolicyList,
		Publish:   rt.Broadcast,
		ConfigDir: rt.lifecycle.configDir,
		WorkDir:   rt.lifecycle.workDir,
	}
	switched, err := store.EnsureCustom(ctx)
	if err != nil {
		if errors.Is(err, securityprofile.ErrRestoreFailed) {
			rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "",
				marotte.PermissionsChangedPayload{Status: "failed"}))
		}
		return err
	}
	if !switched {
		return nil
	}
	slog.Info("security profile switched to custom by an always answer")
	rt.SecurityProfileChanged(ctx)
	return nil
}

// sessionBaseline holds the session-door fingerprints every resident session opened under, or was marked to
// reload from. Set at construction, when none is resident yet.
type sessionBaseline struct {
	chat string
	mu   sync.Mutex
}

// ReconcileSessionSettings marks resident sessions for reload when a setting they read only at their door moved
// since the last reconcile, recomputes whether an engine restart is pending, and pushes the engine each live setting
// it has not confirmed. Every writer calls it after its write lands, and it returns once each push landed or failed.
// A hand edit has no writer to call it: the next chat open syncs it.
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
