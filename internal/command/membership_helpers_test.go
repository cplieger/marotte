package command

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// newTestMembership builds a coordinator over a chat store and NO tab store, bus or teardown seams,
// for tests of the chat half.
func newTestMembership(t *testing.T, chats chatStore) *Membership {
	t.Helper()
	return newMembership(&membershipDeps{Chats: withKeep(chats), Sessions: claimsOf(chats)})
}

// newTabbedMembership builds a coordinator over a chat store, a REAL tab store in a temp dir and a
// recording bus, because the version, order and frames asserted are the real store's.
func newTabbedMembership(t *testing.T, chats chatStore) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return newMembership(&membershipDeps{Chats: withKeep(chats), Tabs: st, Bus: bus}), st, bus
}

// A separate constructor rather than a wider signature on the helper above, which has 27 call sites
// that want no teardown.
func newTornDownMembership(t *testing.T, chats chatStore) (*Membership, *tabs.Store, *tabBus, *recordingTeardown) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	td := &recordingTeardown{}
	mem := newMembership(&membershipDeps{Chats: withKeep(chats), Tabs: st, Bus: bus, Teardown: td})
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

// withKeep gives a test chat store the coordinator's keep-fact writes. A store that already has them (the real
// chat.Store) is returned as is; any other gets an in-memory stand-in over its own Lookup and Mutate, which is enough
// for the tests that do not assert on keep facts. Keep-fact behaviour is asserted against the real store.
func withKeep(c chatStore) membershipChats {
	if mc, ok := c.(membershipChats); ok {
		return mc
	}
	return &keepingStore{chatStore: c}
}

type keepingStore struct {
	chatStore
	tokens int
	mu     sync.Mutex
}

func (k *keepingStore) Hold(ctx context.Context, id marotte.ChatID, key string, _ chat.KeepScope) (chat.Claim, error) {
	if _, found, err := k.Lookup(ctx, id); err != nil {
		return chat.Claim{}, err
	} else if !found {
		return chat.Claim{}, chat.ErrChatNotFound
	}
	k.mu.Lock()
	k.tokens++
	token := fmt.Sprint(k.tokens)
	k.mu.Unlock()
	claim := chat.Claim{ChatID: id, Key: key, Token: token}
	_, err := k.Mutate(ctx, id, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		if prev, ok := c.Keep[key]; ok {
			claim.Prior, claim.PriorFact = chat.PriorThisBoot, prev
		}
		if c.Keep == nil {
			c.Keep = map[string]marotte.KeepFact{}
		}
		c.Keep[key] = marotte.KeepFact{Token: token}
		return true
	})
	return claim, err
}

func (k *keepingStore) Rollback(ctx context.Context, cl *chat.Claim) error {
	_, err := k.Mutate(ctx, cl.ChatID, func(c *marotte.Chat, exists bool) bool {
		if f, ok := c.Keep[cl.Key]; !exists || !ok || f.Token != cl.Token {
			return false
		}
		if cl.Prior == chat.PriorAbsent {
			delete(c.Keep, cl.Key)
		} else {
			c.Keep[cl.Key] = cl.PriorFact
		}
		return true
	})
	return err
}

func (k *keepingStore) Release(ctx context.Context, id marotte.ChatID, key string, _ chat.Evidence) error {
	_, err := k.Mutate(ctx, id, func(c *marotte.Chat, exists bool) bool {
		if _, ok := c.Keep[key]; !exists || !ok {
			return false
		}
		delete(c.Keep, key)
		return true
	})
	return err
}

type headerLister interface {
	List(ctx context.Context) []marotte.ChatHeader
}

// claimsOf answers the owner scan from a test store's headers when it lists them, else nil.
func claimsOf(chats chatStore) sessionClaims {
	if l, ok := chats.(headerLister); ok {
		return headerClaims{chats: l}
	}
	return nil
}

// headerClaims answers the owner scan from a store's headers, every record read.
type headerClaims struct{ chats headerLister }

func (h headerClaims) SessionOwners(ctx context.Context) chat.OwnerScan {
	owners := chat.SessionOwners{}
	for _, hd := range h.chats.List(ctx) {
		for _, sess := range hd.SessionChain() {
			owners[sess] = append(owners[sess], marotte.ChatID(hd.ID))
		}
	}
	return chat.OwnerScan{Owners: owners, Listed: true, Complete: true}
}
