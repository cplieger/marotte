package agent

import (
	"maps"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// bridgeManager is the registry of resident sessions: one entry per chat, and one per parentless run's host session
// under the run's chat key.
type bridgeManager struct {
	bridges map[marotte.ChatID]*sharedBridge
	// closeSession ends a session the registry let go of, off the caller's path.
	closeSession func(engineSession)
	// gates is the turn registry's life gate, which every door insert checks.
	gates *chatGates
	mu    sync.Mutex
}

func newBridgeManager() *bridgeManager {
	return &bridgeManager{bridges: make(map[marotte.ChatID]*sharedBridge), gates: newChatGates()}
}

func (bm *bridgeManager) get(chatID marotte.ChatID) *sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.bridges[chatID]
}

// orInsertAt returns chatID's entry for a door whose intent began at stamp, or creates and inserts one with no
// session yet for life, returning (new, false). A close committed since stamp answers
// marotte.ErrChatClosedDuringCall and a held one the channel to wait on, both inserting nothing, so a reopen never
// attaches to a dying generation. The caller's own serialization lets the entry be returned unlocked.
func (bm *bridgeManager) orInsertAt(
	chatID marotte.ChatID, stamp uint64, life leaseLife,
) (sb *sharedBridge, existed bool, wait <-chan struct{}, err error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	switch v, idle := bm.gates.admit(chatID, stamp); v {
	case gateClosedSince:
		return nil, false, nil, marotte.ErrChatClosedDuringCall
	case gateWait:
		return nil, false, idle, nil
	case gateAdmitted:
	}
	if existing, ok := bm.bridges[chatID]; ok {
		return existing, true, nil, nil
	}
	sb = newSharedBridge(life)
	bm.bridges[chatID] = sb
	return sb, false, nil, nil
}

// orInsertRun is orInsertAt for a run host's entry, which no close holds.
func (bm *bridgeManager) orInsertRun(chatID marotte.ChatID, life leaseLife) (sb *sharedBridge, existed bool) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if existing, ok := bm.bridges[chatID]; ok {
		return existing, true
	}
	sb = newSharedBridge(life)
	bm.bridges[chatID] = sb
	return sb, false
}

// It never replaces an entry, which would orphan a resident session.
func (bm *bridgeManager) insert(chatID marotte.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if _, exists := bm.bridges[chatID]; exists {
		return false
	}
	bm.bridges[chatID] = sb
	return true
}

// remove deletes chatID and returns the removed entry, or nil. Does not close its session.
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

// removeIfBridge removes chatID only if the entry holds session and nobody owns its end: a generation still opening
// and a granted close each retire the entry themselves.
func (bm *bridgeManager) removeIfBridge(chatID marotte.ChatID, session engineSession) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	sb, ok := bm.bridges[chatID]
	if !ok || !sb.endedOnItsOwn(session) {
		return false
	}
	delete(bm.bridges, chatID)
	return true
}

// carries reports whether origin is still chatID's registered session. Nil-safe.
func (bm *bridgeManager) carries(chatID marotte.ChatID, origin acpResponder) bool {
	if bm == nil || origin == nil {
		return false
	}
	sb := bm.get(chatID)
	return sb != nil && sb.holds(origin)
}

// close removes chatID's entry and releases its session (closeSession).
func (bm *bridgeManager) close(chatID marotte.ChatID) {
	if sb := bm.remove(chatID); sb != nil {
		sb.closeOwned(bm.closeSession)
	}
}

func (bm *bridgeManager) all() map[marotte.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	cp := make(map[marotte.ChatID]*sharedBridge, len(bm.bridges))
	maps.Copy(cp, bm.bridges)
	return cp
}

func (bm *bridgeManager) drain() map[marotte.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	out := bm.bridges
	bm.bridges = make(map[marotte.ChatID]*sharedBridge)
	return out
}
