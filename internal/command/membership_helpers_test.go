package command

import (
	"context"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// newTestMembership builds a coordinator over a chat store and NO tab store, bus or teardown seams,
// for tests of the chat half.
func newTestMembership(t *testing.T, chats ChatStore) *Membership {
	t.Helper()
	return NewMembership(&MembershipDeps{Chats: chats})
}

// newTabbedMembership builds a coordinator over a chat store, a REAL tab store in a temp dir and a
// recording bus, because the version, order and frames asserted are the real store's.
func newTabbedMembership(t *testing.T, chats ChatStore) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return NewMembership(&MembershipDeps{Chats: chats, Tabs: st, Bus: bus}), st, bus
}

// newTornDownMembership is newTabbedMembership plus a recording teardown, for the paths
// that run one. A separate constructor rather than a wider signature on the helper above,
// which has 27 call sites that want no teardown.
func newTornDownMembership(t *testing.T, chats ChatStore) (*Membership, *tabs.Store, *tabBus, *recordingTeardown) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	td := &recordingTeardown{}
	mem := NewMembership(&MembershipDeps{Chats: chats, Tabs: st, Bus: bus, Teardown: td})
	return mem, st, bus, td
}

// tabBus records the tabs_changed frames a coordinator emitted, under its own mutex for the -race
// tests.
type tabBus struct {
	events []marotte.ServerEvent
	mu     sync.Mutex
}

func (b *tabBus) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
}

// frames returns the tabs_changed payloads seen so far, in arrival order. The
// type assertion is a Fatalf rather than a skip: a frame of another shape on this
// event type is the bug, not a case to tolerate.
func (b *tabBus) frames(t *testing.T) []marotte.TabsChangedPayload {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []marotte.TabsChangedPayload
	for _, evt := range b.events {
		if evt.Type != marotte.EventTabsChanged {
			continue
		}
		p, ok := evt.Payload.(marotte.TabsChangedPayload)
		if !ok {
			t.Fatalf("tabs_changed payload = %T, want marotte.TabsChangedPayload", evt.Payload)
		}
		out = append(out, p)
	}
	return out
}

// removedIDs is every id the bus reported as closed, across every frame. The
// delete paths assert on the union rather than per frame, because a close of a
// parent with children is one frame while a retention sweep of two tabs is two.
func (b *tabBus) removedIDs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range b.frames(t) {
		out = append(out, p.RemovedIDs...)
	}
	return out
}
