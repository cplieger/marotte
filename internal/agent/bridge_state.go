package agent

import "sync"

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
	// settled is the generation's readiness from the session open's first instant: openBridge hands sb to
	// no caller before it settles.
	settled *sessionSettle
	state   bridgeState
	mu      sync.Mutex
	// effortHealed latches the one reactive effort repair (bridgeCoordinator.healEffort).
	effortHealed bool
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
