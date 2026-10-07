package agent

import (
	"log/slog"
	"maps"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"golang.org/x/sync/singleflight"
)

// bridgeManager owns the per-chat bridge map and serializes bridge lifecycle operations.
type bridgeManager struct {
	spawnSF singleflight.Group
	bridges map[marotte.ChatID]*sharedBridge
	factory ACPBridgeFactory
	// hostsLiveRun reports whether the chat's bridge hosts an open step turn of a chat-parented
	// run; such a bridge is busy to a retire, since steps never take the prompt slot. Nil: never busy.
	hostsLiveRun func(marotte.ChatID) bool
	mu           sync.Mutex
}

func newBridgeManager(factory ACPBridgeFactory) *bridgeManager {
	return &bridgeManager{
		bridges: make(map[marotte.ChatID]*sharedBridge),
		factory: factory,
	}
}

// get returns the bridge for chatID, or nil.
func (bm *bridgeManager) get(chatID marotte.ChatID) *sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.bridges[chatID]
}

// orInsert returns chatID's bridge, or creates and inserts one, returning (new, false). The
// caller's own serialization lets it be returned unlocked.
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

// insert registers an already-started bridge (a run bridge keyed by its workflow id). It
// never replaces an entry, which would orphan a live process.
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

// remove deletes chatID and returns the removed bridge, or nil. Does not Stop it.
func (bm *bridgeManager) remove(chatID marotte.ChatID) *sharedBridge {
	bm.mu.Lock()
	sb := bm.bridges[chatID]
	if sb != nil {
		delete(bm.bridges, chatID)
	}
	bm.mu.Unlock()
	return sb
}

// removeIfSame removes chatID only if the current entry is sb.
func (bm *bridgeManager) removeIfSame(chatID marotte.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if cur, ok := bm.bridges[chatID]; ok && cur == sb {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// removeIfBridge removes chatID only if the entry's bridge is the same instance as bridge.
func (bm *bridgeManager) removeIfBridge(chatID marotte.ChatID, bridge ACPBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if sb, ok := bm.bridges[chatID]; ok && sb.bridge == bridge {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// close removes and stops chatID's bridge. Idempotent.
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

// all returns a snapshot of every bridge.
func (bm *bridgeManager) all() map[marotte.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	cp := make(map[marotte.ChatID]*sharedBridge, len(bm.bridges))
	maps.Copy(cp, bm.bridges)
	return cp
}

// drain removes and returns every bridge, for teardown.
func (bm *bridgeManager) drain() map[marotte.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	out := bm.bridges
	bm.bridges = make(map[marotte.ChatID]*sharedBridge)
	return out
}

// retireChatBridges stops idle chat bridges, marks busy ones for their next open, and
// returns the stopped chats so the caller closes their turns. Run bridges, and chat bridges
// hosting a live run, are not interrupted: they rely on the relay's token refresh.
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

// markChatBridgesForReopen flags every chat bridge so its next open stops it and opens a fresh
// one (closeIfRetired). It stops nothing, so an idle chat keeps its agent terminals until then.
// Run bridges are skipped: a run keeps the session it launched with.
func (bm *bridgeManager) markChatBridgesForReopen() (marked int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	for chatID, sb := range bm.bridges {
		if isRunChat(chatID) {
			continue
		}
		sb.mu.Lock()
		if !sb.retire {
			sb.retire = true
			marked++
		}
		sb.mu.Unlock()
	}
	return marked
}

// hostsRun is hostsLiveRun with the nil case answered.
func (bm *bridgeManager) hostsRun(chatID marotte.ChatID) bool {
	return bm.hostsLiveRun != nil && bm.hostsLiveRun(chatID)
}

// closeIfRetired removes and stops sb once its turn released the prompt slot and its hosted
// runs closed their last step turn. reopen: the caller must open a fresh bridge; stopped:
// THIS call stopped sb and owes closeTurnsOnRetire.
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
