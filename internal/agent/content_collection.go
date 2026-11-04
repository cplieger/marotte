package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// contentCollectionEnabled resolves the value the engine gets: the organization lock, else the stored switch. readable
// is false only when no lock decides and stored is not config.json's current answer (current false).
func contentCollectionEnabled(stored, current bool, locks map[string]marotte.GovernanceLock) (enabled, readable bool) {
	if l, ok := locks[marotte.LockContentCollection]; ok {
		return l.Value, true
	}
	return stored, current
}

// contentCollection is EngineOpts.ContentCollection, called on every assert so a mid-spawn change is not lost. Over an
// unreadable document it answers the last readable switch, unconfirmed, so a refresh keeps what the engine holds.
func (rt *Runtime) contentCollection(ctx context.Context) (enabled, readable bool) {
	locks := currentLocks(rt.config.GovernanceLocks)
	if _, ok := locks[marotte.LockContentCollection]; ok {
		return contentCollectionEnabled(false, true, locks)
	}
	doc, src := rt.configDocument(ctx)
	return contentCollectionEnabled(doc.engine.contentCollection, src == fromDocument, locks)
}

func (rt *Runtime) onGovernanceLocksChanged(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "organization locks")
	rt.powers.requestSync()
	if rt.locksHook != nil {
		rt.locksHook(ctx)
	}
}
