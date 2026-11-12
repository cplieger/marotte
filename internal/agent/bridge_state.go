package agent

import "sync"

type bridgeState int

const (
	bridgeReady    bridgeState = iota // the door attached the session
	bridgeStarting                    // the session door is in progress (held during openGeneration)
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
	// roleMissing is KAS reporting a Bot's role absent from its session since setBotRole's last send (noteBotRoleMissing).
	roleMissing bool
}

// The manager registers the record before the door, so map presence is not liveness.
func (sb *sharedBridge) startedPastOpen() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.state != bridgeStarting
}

// setReady publishes the door's end under the lock: admission reads liveness during the session open.
func (sb *sharedBridge) setReady() {
	sb.mu.Lock()
	sb.state = bridgeReady
	sb.mu.Unlock()
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

func (sb *sharedBridge) markRoleMissing() {
	sb.mu.Lock()
	sb.roleMissing = true
	sb.mu.Unlock()
}

// takeRoleMissing reports whether KAS reported the role absent since the last take, clearing it.
func (sb *sharedBridge) takeRoleMissing() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	missing := sb.roleMissing
	sb.roleMissing = false
	return missing
}
