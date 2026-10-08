package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// contentCollectionEnabled resolves the value every kiro-cli process gets: the organization lock,
// else the stored switch, default off. readable is false only when no lock decides and the
// document cannot be read; enabled is then off.
func contentCollectionEnabled(ctx context.Context, configDir string, locks map[string]marotte.GovernanceLock) (enabled, readable bool) {
	if l, ok := locks[marotte.LockContentCollection]; ok {
		return l.Value, true
	}
	return settings.FieldOr(ctx, configDir, settings.KeyContentCollectionEnabled, settings.DefaultContentCollectionEnabled)
}

// contentCollectionResolver is StartOpts.ContentCollection, called on every assert so a mid-spawn change is not lost.
func contentCollectionResolver(configDir string, locks func() map[string]marotte.GovernanceLock) func(context.Context) (bool, bool) {
	return func(ctx context.Context) (bool, bool) {
		return contentCollectionEnabled(ctx, configDir, currentLocks(locks))
	}
}

func (rt *Runtime) onGovernanceLocksChanged(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "organization locks")
	rt.powers.requestSync()
	if rt.locksHook != nil {
		rt.locksHook(ctx)
	}
}
