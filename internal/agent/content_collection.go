package agent

import (
	"context"
	"log/slog"
	"sync"

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

// ContentSharer is an EngineSource that writes kiro-cli's own content-collection switch. The relay sets its engine's
// process-wide value from that switch at every spawn, unset reading on, and KAS runs on it until a session door
// asserts marotte's value, so a run the engine resumes at initialize would otherwise send opted-in requests.
type ContentSharer interface {
	ShareContent(ctx context.Context, enabled bool) error
}

// kiroShare keeps kiro-cli's switch on the resolved value. mu spans a resolve and its write, so the switch always
// ends on the newest resolution.
type kiroShare struct {
	resolve func(context.Context) (enabled, readable bool)
	mu      sync.Mutex
	written bool
	known   bool
}

// assert writes the resolved value when force is set or when it differs from the last write that landed. Over
// unreadable settings an unforced call keeps that write. A spawn forces it, since kiro-cli's file can change by hand.
func (k *kiroShare) assert(ctx context.Context, src EngineSource, force bool) {
	sharer, ok := src.(ContentSharer)
	if !ok || k.resolve == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	enabled, readable := k.resolve(ctx)
	if !force && k.known && (!readable || k.written == enabled) {
		return
	}
	err := sharer.ShareContent(ctx, enabled)
	k.written, k.known = enabled, err == nil
	if err != nil {
		slog.Error("kiro-cli's content-collection switch not written; the agent engine may start opted in until a session door asserts the setting",
			"enabled", enabled, "error", err)
	}
}

func (rt *Runtime) onGovernanceLocksChanged(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "organization locks")
	rt.powers.requestSync()
	if rt.locksHook != nil {
		rt.locksHook(ctx)
	}
}
