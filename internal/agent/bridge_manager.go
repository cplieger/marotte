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
	// hostsLiveRun reports whether the chat's session hosts an open step turn of a chat-parented
	// run; such a session is busy to a reload, since steps never take the prompt slot. Nil: never busy.
	hostsLiveRun func(marotte.ChatID) bool
	// revertStarting is told of every checkpoint revert a chat's session sends. Nil: nobody listens.
	revertStarting func(chatID marotte.ChatID, session string)
	// closeSession ends a session the registry let go of, off the caller's path.
	closeSession func(engineSession)
	mu           sync.Mutex
}

func newBridgeManager() *bridgeManager {
	return &bridgeManager{bridges: make(map[marotte.ChatID]*sharedBridge)}
}

func (bm *bridgeManager) get(chatID marotte.ChatID) *sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.bridges[chatID]
}

// orInsert returns chatID's entry, or creates and inserts one with no session yet, returning (new, false). The
// caller's own serialization lets it be returned unlocked.
func (bm *bridgeManager) orInsert(chatID marotte.ChatID) (sb *sharedBridge, existed bool) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if existing, ok := bm.bridges[chatID]; ok {
		return existing, true
	}
	sb = &sharedBridge{state: bridgeStarting, settled: newSessionSettle()}
	if notify := bm.revertStarting; notify != nil {
		sb.revertStarting = func(session string) { notify(chatID, session) }
	}
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
// and a reload closing it each retire the entry themselves.
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
	if sb == nil {
		return false
	}
	cur := sb.current()
	return cur != nil && acpResponder(cur) == origin
}

// close removes chatID's entry and closes its session.
func (bm *bridgeManager) close(chatID marotte.ChatID) {
	if sb := bm.remove(chatID); sb != nil {
		if s := sb.current(); s != nil {
			bm.closeSession(s)
		}
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

// markSessionsForReload flags every resident chat session so its next idle open closes and reloads it
// (reloadIfMarked). It closes nothing, so an idle chat keeps its agent terminals until then.
func (bm *bridgeManager) markSessionsForReload() (marked int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	for chatID, sb := range bm.bridges {
		if isRunChat(chatID) {
			continue
		}
		sb.mu.Lock()
		if !sb.reload {
			sb.reload = true
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

// reloadClaim is claimReload's answer. busy is the claim another reload or release holds, which the caller waits
// out before it asks again; otherwise due says whether the caller now owns session's close.
type reloadClaim struct {
	session engineSession
	busy    <-chan struct{}
	due     bool
	// stale: sb is no longer registered.
	stale bool
}

// claimReload reports whether sb's session is due its reload: marked (or no longer open), its turn released the
// prompt slot and its hosted runs closed their last step turn. A claimed entry stays registered while the caller
// closes its session, and the forward's end leaves its removal to finishReload.
func (bm *bridgeManager) claimReload(chatID marotte.ChatID, sb *sharedBridge) reloadClaim {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if bm.bridges[chatID] != sb {
		return reloadClaim{stale: true}
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.closeClaim != nil {
		return reloadClaim{busy: sb.closeClaim}
	}
	dead := sb.bridge == nil || !sb.bridge.Open()
	if !dead && (!sb.reload || sb.state != bridgeIdle || bm.hostsRun(chatID)) {
		return reloadClaim{}
	}
	sb.closeClaim = make(chan struct{})
	return reloadClaim{session: sb.bridge, due: true}
}

// claimRelease claims chatID's chat session for the release check: registered, open, idle, hosting no run step and
// not already claimed. The caller ends the claim with finishReload, as a reload does.
func (bm *bridgeManager) claimRelease(chatID marotte.ChatID) (*sharedBridge, engineSession, bool) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	sb := bm.bridges[chatID]
	if sb == nil {
		return nil, nil, false
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.closeClaim != nil || sb.bridge == nil || !sb.bridge.Open() || sb.state != bridgeIdle || bm.hostsRun(chatID) {
		return nil, nil, false
	}
	sb.closeClaim = make(chan struct{})
	return sb, sb.bridge, true
}

// finishReload ends a claimed reload: a closed session's entry leaves the registry, so the next open loads the
// session again; a refused close keeps the entry, its session and its mark. Opens waiting on the claim go on after
// the registry reflects the outcome.
func (bm *bridgeManager) finishReload(chatID marotte.ChatID, sb *sharedBridge, closed bool) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if closed && bm.bridges[chatID] == sb {
		delete(bm.bridges, chatID)
	}
	sb.mu.Lock()
	claim := sb.closeClaim
	sb.closeClaim = nil
	sb.mu.Unlock()
	if claim != nil {
		close(claim)
	}
}
