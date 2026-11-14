package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func newContentCollectionHub(t *testing.T, br *fakeBridge, opts ...Option) (*Runtime, *testChatStore) {
	t.Helper()
	dir := writeSettings(t, `{"content_collection_enabled":true}`)
	cs := newTestChatStore()
	// Cold: these tests set the account's profile themselves, which a door's own profile would overwrite.
	src := fakeEngineOf(func() engineSession { return br }, br)
	src.noGovernance = true
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, append([]Option{WithConfigDir(dir)}, opts...)...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, cs
}

func contentCollectionWrites(b *fakeBridge) []bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.contentCollectionWrites)
}

// A lock change reaches the engine whatever config.json holds: the lock is the answer, and every session shares the
// one process KAS holds the value in, so the running engine is notified once, or a restart's initialize carries it.
func TestGovernanceLockChange_ReachesTheEngineReadableOrNot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
	}{
		{name: "readable_document"},
		{name: "unreadable_document", document: "{not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane := newFakeBridge()
			pushed := make(chan struct{}, 1)
			h, cs := newContentCollectionHub(t, lane,
				WithGovernanceLocksHook(func(context.Context) {
					select {
					case pushed <- struct{}{}:
					default:
					}
				}))
			settleBoot(t, h)
			if !awaitEngine(h) {
				t.Fatal("Setup: the engine never became ready")
			}
			before := len(contentCollectionWrites(lane))
			if tc.document != "" {
				rewriteConfigByHand(t, h.lifecycle.configDir, tc.document)
			}

			h.config.SetGovernance(t.Context(), orgTelemetry(true))
			select {
			case <-pushed:
			case <-time.After(10 * time.Second):
				t.Fatal("the lock change never finished its push")
			}
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			openChat(t, h, "c1")

			if writes := contentCollectionWrites(lane)[before:]; len(writes) > 1 || slices.Contains(writes, true) {
				t.Errorf("engine content-collection notifications after the lock = %v, want at most one false", writes)
			}
			e := fakeSourceOf(t, h).current()
			e.mu.Lock()
			restarted, startedOn := e.gen > 1, e.contentCollection
			e.mu.Unlock()
			if restarted && startedOn {
				t.Error("the engine restarted after the lock with content collection on, want the organization's off")
			}
			lane.mu.Lock()
			applied, known := lane.contentCollectionApplied, lane.contentCollectionKnown
			lane.mu.Unlock()
			if applied || !known {
				t.Errorf("the engine holds content collection applied = %v known = %v, want the organization's off, confirmed", applied, known)
			}
		})
	}
}
