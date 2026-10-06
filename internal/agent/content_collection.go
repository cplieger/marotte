package agent

import (
	"context"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// contentCollectionEnabled resolves the value every kiro-cli process gets: the organization lock,
// else the stored switch, default off; an unreadable document reads off.
func contentCollectionEnabled(ctx context.Context, configDir string, locks map[string]marotte.GovernanceLock) bool {
	stored := settings.DefaultContentCollectionEnabled
	var b bool
	if settings.FieldInto(ctx, configDir, settings.KeyContentCollectionEnabled, &b) {
		stored = b
	}
	return lockedBool(locks, marotte.LockContentCollection, stored)
}

// contentCollectionResolver is StartOpts.ContentCollection, called on every assert so a mid-spawn change is not lost.
func contentCollectionResolver(configDir string, locks func() map[string]marotte.GovernanceLock) func(context.Context) bool {
	return func(ctx context.Context) bool {
		return contentCollectionEnabled(ctx, configDir, currentLocks(locks))
	}
}

// PushContentCollection re-asserts the resolved value on every live bridge and the utility session;
// KAS reads it per model request. Failures are logged.
func (rt *Runtime) PushContentCollection(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for chatID, sb := range rt.bridge.mgr.all() {
		wg.Go(func() {
			if enabled, err := sb.bridge.AssertContentCollection(cctx); err != nil {
				slog.Error("content collection not pushed; model requests from this chat's process may still be opted in",
					"chat_id", chatID, "enabled", enabled, "error", err)
			}
		})
	}
	wg.Go(func() { rt.utility.get().session.pushContentCollection(cctx) })
	wg.Wait()
}

// pushContentCollection re-asserts the value on a running utility session; a stopped
// one resolves it at its next start.
func (us *utilitySession) pushContentCollection(ctx context.Context) {
	us.mu.Lock()
	running, b := us.started, us.bridge
	us.mu.Unlock()
	if !running || b == nil {
		return
	}
	if enabled, err := b.AssertContentCollection(ctx); err != nil {
		slog.Error("content collection not pushed; utility-bridge model requests may still be opted in",
			"enabled", enabled, "error", err)
	}
}

func (rt *Runtime) onGovernanceLocksChanged(ctx context.Context) {
	rt.PushContentCollection(ctx)
	rt.powers.requestSync()
	if rt.locksHook != nil {
		rt.locksHook(ctx)
	}
}
