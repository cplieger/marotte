package agent

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

type bridgeState int

const (
	bridgeIdle      bridgeState = iota // ready for a new prompt
	bridgeStarting                     // bridge.Start in progress (held during spawnGeneration)
	bridgePrompting                    // prompt in flight
)

// mu guards the fields and state encodes the lifecycle phase.
type sharedBridge struct {
	bridge ACPBridge
	// revertStarting runs before a checkpoint revert with the session it reverts; nil on a run carrier.
	revertStarting func(session string)

	// promptCancel trips the in-flight prompt: session/cancel is an unacked notification, so a
	// silent KAS would block the Call forever. turnGen keeps an expired grace off a later turn;
	// the cause tells an expired grace from other cancellations.
	promptCancel context.CancelCauseFunc
	cancelTimer  *time.Timer
	// settled is the generation's readiness from the spawn's first instant: openBridge hands sb to
	// no caller before it settles. Nil for a carrier that never loads a session.
	settled *sessionSettle

	// liveLock serializes every live push to this process and guards live (syncLive).
	liveLock surfaceLock
	live     bridgeLive
	turnGen  uint64
	mu       sync.Mutex
	state    bridgeState
	// effortHealed latches the one reactive effort repair (bridgeCoordinator.healEffort).
	effortHealed bool
	retire       bool
	// runVerbs counts the run verbs that ever held this carrier, so a failed verb can tell a sole carrier from a shared one.
	runVerbs atomic.Int32
}

// current is the bridge sb holds now, read under the lock: a failed session/load swaps it.
func (sb *sharedBridge) current() ACPBridge {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.bridge
}

func (sb *sharedBridge) swapBridge(next ACPBridge) ACPBridge {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	old := sb.bridge
	sb.bridge = next
	return old
}

// tryAcquireForPrompt moves idle to prompting, reporting whether the caller owns the slot.
func (sb *sharedBridge) tryAcquireForPrompt() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgeIdle {
		return false
	}
	sb.state = bridgePrompting
	sb.turnGen++
	return true
}

func (sb *sharedBridge) releaseAfterPrompt() {
	sb.mu.Lock()
	sb.state = bridgeIdle
	sb.stopCancelTimerLocked()
	sb.mu.Unlock()
}

// The manager registers the record before Start, so map presence is not liveness.
func (sb *sharedBridge) startedPastSpawn() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.state != bridgeStarting
}

// setIdle publishes the idle transition under the lock: admission reads liveness during the spawn.
func (sb *sharedBridge) setIdle() {
	sb.mu.Lock()
	sb.state = bridgeIdle
	sb.mu.Unlock()
}

// Caller holds mu.
func (sb *sharedBridge) stopCancelTimerLocked() {
	if sb.cancelTimer != nil {
		sb.cancelTimer.Stop()
		sb.cancelTimer = nil
	}
}

// Four explicit forwards rather than an embedded ACPBridge, so a command.Bridge holder can
// never Start, Stop or SetModel behind the state machine.

func (sb *sharedBridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	sb.beforeCall(method)
	return sb.bridge.Call(ctx, method, params)
}

func (sb *sharedBridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	sb.beforeCall(method)
	return sb.bridge.CallAt(ctx, method, params)
}

// beforeCall announces a checkpoint revert before it is sent, since KAS writes the files back
// before it answers.
func (sb *sharedBridge) beforeCall(method string) {
	if method == marotte.MethodCheckpointRevertMultiple && sb.revertStarting != nil {
		sb.revertStarting(string(sb.SessionID()))
	}
}

func (sb *sharedBridge) Notify(ctx context.Context, method string, params any) error {
	return sb.bridge.Notify(ctx, method, params)
}

func (sb *sharedBridge) Respond(ctx context.Context, requestID int64, result any, err error) error {
	return sb.bridge.Respond(ctx, requestID, result, err)
}

func (sb *sharedBridge) SessionID() marotte.SessionID {
	return sb.bridge.SessionID()
}

func (sb *sharedBridge) TryAcquireForPrompt() bool {
	return sb.tryAcquireForPrompt()
}

func (sb *sharedBridge) ReleaseAfterPrompt() {
	sb.releaseAfterPrompt()
}

// BeginPromptCall records the in-flight prompt's cancel func and returns the
// generation of the turn it belongs to.
func (sb *sharedBridge) BeginPromptCall(cancel context.CancelCauseFunc) uint64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.promptCancel = cancel
	return sb.turnGen
}

// EndPromptCall forgets the in-flight prompt's cancel func and disarms any
// pending cancel-grace timer.
func (sb *sharedBridge) EndPromptCall() {
	sb.mu.Lock()
	sb.promptCancel = nil
	sb.stopCancelTimerLocked()
	sb.mu.Unlock()
}

// PromptGeneration returns the current turn generation.
func (sb *sharedBridge) PromptGeneration() uint64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.turnGen
}

// ArmCancelGrace starts the unresponsive-cancel budget for turn gen; an acked cancel costs one stopped timer.
func (sb *sharedBridge) ArmCancelGrace(gen uint64, d time.Duration) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgePrompting || sb.turnGen != gen || sb.promptCancel == nil {
		return false
	}
	sb.stopCancelTimerLocked()
	sb.cancelTimer = time.AfterFunc(d, func() {
		// Timer.Stop does not halt a running func, so decide again under the lock.
		cancel, ok := sb.shouldTripCancelGrace(gen)
		if !ok {
			return
		}
		slog.Warn("cancel unacked within grace; unblocking the turn",
			"grace", d, "turn_gen", gen)
		cancel(command.ErrCancelGraceExpired)
	})
	return true
}

// It refuses when the chat is no longer prompting, the generation moved on, or no prompt context is
// registered.
func (sb *sharedBridge) shouldTripCancelGrace(gen uint64) (context.CancelCauseFunc, bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgePrompting || sb.turnGen != gen || sb.promptCancel == nil {
		return nil, false
	}
	return sb.promptCancel, true
}

// cancelPromptCall trips the in-flight prompt so the failure path finalizes the turn, and
// reports whether there was one. It records no cause; that lives on the turn record.
func (sb *sharedBridge) cancelPromptCall() bool {
	sb.mu.Lock()
	if sb.state != bridgePrompting || sb.promptCancel == nil {
		sb.mu.Unlock()
		return false
	}
	cancel := sb.promptCancel
	sb.mu.Unlock()
	// Outside the lock: cancel runs arbitrary callbacks. No cause, so the turn concludes `interrupted`.
	cancel(nil)
	return true
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
