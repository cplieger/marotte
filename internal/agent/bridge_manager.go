package agent

import (
	"log/slog"
	"maps"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"golang.org/x/sync/singleflight"
)

// bridgeManager owns the per-chat bridge map and serializes access to bridge
// lifecycle operations. Runtime composes it and owns dispatch.
type bridgeManager struct {
	spawnSF singleflight.Group
	bridges map[marotte.ChatID]*sharedBridge
	factory ACPBridgeFactory
	// hostsLiveRun reports whether the chat's bridge hosts an open step turn of a
	// chat-parented run. Such a bridge is BUSY to a retire whatever its prompt slot
	// says: a run's steps never take the slot, so the launching chat reads idle for
	// the whole time its workflow runs. Nil means no run registry, never busy.
	hostsLiveRun func(marotte.ChatID) bool
	mu           sync.Mutex
}

func newBridgeManager(factory ACPBridgeFactory) *bridgeManager {
	return &bridgeManager{
		bridges: make(map[marotte.ChatID]*sharedBridge),
		factory: factory,
	}
}

// get returns the bridge for chatID, or nil if none exists.
func (bm *bridgeManager) get(chatID marotte.ChatID) *sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.bridges[chatID]
}

// orInsert returns the existing bridge for chatID, or creates one via the factory,
// inserts it, and returns (newBridge, false). The caller's own serialization
// (OpenBridge's spawnSF, loadRunCarrier's run host lock) is what lets the new bridge
// be returned unlocked.
func (bm *bridgeManager) orInsert(chatID marotte.ChatID) (sb *sharedBridge, existed bool) {
	bm.mu.Lock()
	if existing, ok := bm.bridges[chatID]; ok {
		bm.mu.Unlock()
		return existing, true
	}
	sb = &sharedBridge{bridge: bm.factory(), state: bridgeStarting}
	bm.bridges[chatID] = sb
	bm.mu.Unlock()
	slog.Info("bridge spawned", "chat_id", chatID)
	return sb, false
}

// insert registers an ALREADY-STARTED bridge under chatID: a launched run bridge's map
// key is its workflow id, which only `workflow/new`'s reply knows. Replacing an entry
// would orphan a live process, so insert refuses and reports false.
func (bm *bridgeManager) insert(chatID marotte.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if _, exists := bm.bridges[chatID]; exists {
		return false
	}
	bm.bridges[chatID] = sb
	slog.Info("bridge registered", "chat_id", chatID)
	return true
}

// remove deletes chatID from the map and returns the removed bridge, or nil. Does NOT
// call Stop.
func (bm *bridgeManager) remove(chatID marotte.ChatID) *sharedBridge {
	bm.mu.Lock()
	sb := bm.bridges[chatID]
	if sb != nil {
		delete(bm.bridges, chatID)
	}
	bm.mu.Unlock()
	return sb
}

// removeIfSame removes chatID only if the current entry matches sb.
func (bm *bridgeManager) removeIfSame(chatID marotte.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if cur, ok := bm.bridges[chatID]; ok && cur == sb {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// removeIfBridge removes chatID only if the current entry's bridge is the SAME
// INSTANCE as bridge. The parameter is an identity, not a capability: it stays the
// full ACPBridge so a caller cannot pass something the map could never have held.
func (bm *bridgeManager) removeIfBridge(chatID marotte.ChatID, bridge ACPBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if sb, ok := bm.bridges[chatID]; ok && sb.bridge == bridge {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// close removes the bridge for chatID and stops it. Idempotent.
func (bm *bridgeManager) close(chatID marotte.ChatID) {
	sb := bm.remove(chatID)
	if sb != nil {
		sb.bridge.Stop()
	}
}

// count returns the number of active bridges.
func (bm *bridgeManager) count() int {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return len(bm.bridges)
}

// all returns a snapshot of every bridge, for callers that must inspect them all.
func (bm *bridgeManager) all() map[marotte.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	cp := make(map[marotte.ChatID]*sharedBridge, len(bm.bridges))
	maps.Copy(cp, bm.bridges)
	return cp
}

// drain removes every bridge from the map and returns them for teardown.
func (bm *bridgeManager) drain() []*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	out := make([]*sharedBridge, 0, len(bm.bridges))
	for id, sb := range bm.bridges {
		out = append(out, sb)
		delete(bm.bridges, id)
	}
	return out
}

// retireChatBridges stops idle chat bridges and marks busy ones for the next
// bridge open, answering the stopped bridges' chat ids so the caller can close the
// turns they hosted. Run bridges are durable work and rely on the relay's own
// token refresh, so an account change does not interrupt them; a chat bridge
// hosting a live run is busy for the same reason.
func (bm *bridgeManager) retireChatBridges() (closed []marotte.ChatID, marked int) {
	var victims []*sharedBridge
	bm.mu.Lock()
	for chatID, sb := range bm.bridges {
		if isRunChat(chatID) {
			continue
		}
		sb.mu.Lock()
		if sb.state == bridgeIdle && !bm.hostsRun(chatID) {
			delete(bm.bridges, chatID)
			victims = append(victims, sb)
			closed = append(closed, chatID)
		} else if !sb.retire {
			sb.retire = true
			marked++
		}
		sb.mu.Unlock()
	}
	bm.mu.Unlock()
	for _, sb := range victims {
		sb.bridge.Stop()
	}
	return closed, marked
}

// hostsRun is hostsLiveRun with the nil case answered.
func (bm *bridgeManager) hostsRun(chatID marotte.ChatID) bool {
	return bm.hostsLiveRun != nil && bm.hostsLiveRun(chatID)
}

// closeIfRetired removes and stops sb only after its active turn has released the
// prompt slot and its hosted runs have closed their last step turn. reopen is
// whether the caller must open a fresh bridge; stopped is whether THIS call stopped
// sb, the one case that owes closeTurnsOnRetire (a map holding a different bridge
// stops nothing).
func (bm *bridgeManager) closeIfRetired(chatID marotte.ChatID, sb *sharedBridge) (reopen, stopped bool) {
	bm.mu.Lock()
	if bm.bridges[chatID] != sb {
		bm.mu.Unlock()
		return true, false
	}
	sb.mu.Lock()
	ready := sb.retire && sb.state == bridgeIdle && !bm.hostsRun(chatID)
	if ready {
		delete(bm.bridges, chatID)
	}
	sb.mu.Unlock()
	bm.mu.Unlock()
	if ready {
		sb.bridge.Stop()
	}
	return ready, ready
}
