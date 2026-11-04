package agent

import (
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

type bridgeState int

const (
	bridgeIdle      bridgeState = iota // ready for a new prompt
	bridgeStarting                     // the session door is in progress (held during openGeneration)
	bridgePrompting                    // prompt in flight
)

// mu guards the fields and state encodes the lifecycle phase.
type sharedBridge struct {
	// leased is the entry's session; only session_lease.go reads or writes it.
	leased engineSession
	// life is the life the entry's leases are counted on (newSharedBridge).
	life leaseLife
	// revertStarting runs before a checkpoint revert with the session it reverts; nil on a run host.
	revertStarting func(session string)
	// settled is the generation's readiness from the session open's first instant: openBridge hands sb to
	// no caller before it settles.
	settled *sessionSettle
	// closeClaim is non-nil while a reload or a release owns the session's close, so its forward's end is not the
	// session ending on its own; finishReload closes it, and an open meanwhile waits on it (claimReload).
	closeClaim chan struct{}
	state      bridgeState
	mu         sync.Mutex
	// effortHealed latches the one reactive effort repair (bridgeCoordinator.healEffort).
	effortHealed bool
	// reload marks a session whose door settings moved: its next idle open closes it and loads it again.
	reload bool
}

// tryAcquireForPrompt moves idle to prompting, reporting whether the caller owns the slot.
func (sb *sharedBridge) tryAcquireForPrompt() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgeIdle {
		return false
	}
	sb.state = bridgePrompting
	return true
}

func (sb *sharedBridge) releaseAfterPrompt() {
	sb.mu.Lock()
	sb.state = bridgeIdle
	sb.mu.Unlock()
}

// The manager registers the record before the door, so map presence is not liveness.
func (sb *sharedBridge) startedPastOpen() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.state != bridgeStarting
}

// setIdle publishes the idle transition under the lock: admission reads liveness during the session open.
func (sb *sharedBridge) setIdle() {
	sb.mu.Lock()
	sb.state = bridgeIdle
	sb.mu.Unlock()
}

// The command layer's calls on an entry are leases (session_lease.go), so a command.Bridge holder can never close
// the session or SetModel behind the state machine. Before its door attaches a session, every call refuses with
// marotte.ErrSessionClosed and SessionID answers "".

// beforeCall announces a checkpoint revert before it is sent, since KAS writes the files back
// before it answers.
func (sb *sharedBridge) beforeCall(method string, session marotte.SessionID) {
	if method == marotte.MethodCheckpointRevertMultiple && sb.revertStarting != nil {
		sb.revertStarting(string(session))
	}
}

func (sb *sharedBridge) TryAcquireForPrompt() bool {
	return sb.tryAcquireForPrompt()
}

func (sb *sharedBridge) ReleaseAfterPrompt() {
	sb.releaseAfterPrompt()
}

// claimEffortHeal reports whether the caller won the one reactive effort repair, latching it.
func (sb *sharedBridge) claimEffortHeal() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.effortHealed {
		return false
	}
	sb.effortHealed = true
	return true
}
