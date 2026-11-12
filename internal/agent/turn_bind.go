package agent

import (
	"context"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// withLifecycle runs fn with the chat's mutex held, after waiting out a finalizing own turn, or returns ctx.Err().
// It is the one door making a turn_open append and its registry record one operation (lifecycle then store). A chat
// with no lifecycle answers errNoLife: every caller reaches it from a life its admission or its forward created.
func (r *turnRegistry) withLifecycle(ctx context.Context, chatID marotte.ChatID, fn func(lc *chatLifecycle) error) error {
	lc, ok := r.lookup(chatID)
	if !ok {
		return errNoLife
	}
	if !lc.awaitNotFinalizing(ctx) {
		return ctx.Err()
	}
	defer lc.mu.Unlock()
	return fn(lc)
}

// errNoLife is withLifecycle's answer for a chat with no lifecycle: it was closed and forgotten.
var errNoLife = fmt.Errorf("the chat has no live turn state: %w", marotte.ErrChatClosedDuringCall)

// bindPending binds a turn_start to the pending prompt turn when its session/prompt went out (awaitsSend) and the
// bracket's tag is that turn's id, answering its id. If not, it claims the own turn the bracket supersedes, an acked
// one (bound, or a wire turn, fold-opened included) or any own beside a bindable prompt, for the caller to close.
// Neither means open a turn. It waits out finalizing; "" and nil when ctx died.
func (r *turnRegistry) bindPending(ctx context.Context, chatID marotte.ChatID, tag string) (bound string, lost *activeTurn) {
	err := r.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		p, o := lc.pending, lc.own
		unbindable := p == nil || p.acked || p.finalizing || p.awaitsSend() || tag != p.ID
		if (unbindable && o != nil && o.acked) || (!unbindable && o != nil && o != p) {
			// withLifecycle waited out a finalizing own, so o is unclaimed and the claim holds.
			lc.claimIfLocked(o, nil)
			lost = o
			return nil
		}
		if unbindable {
			return nil
		}
		p.acked = true
		lc.pending = nil
		lc.own = p
		lc.setStateLocked(turnOpen)
		bound = p.ID
		return nil
	})
	if err != nil {
		return "", nil
	}
	return bound, lost
}

// foldTarget is the own turn's accumulator, false when none or finalizing, so the caller opens a wire turn instead.
func (r *turnRegistry) foldTarget(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing || lc.own.awaitsSend() {
		return nil, false
	}
	return lc.own.Log, true
}

// turnOpen reports whether chatID has a turn no closer has claimed: one owed its bracket or one of its own.
func (r *turnRegistry) turnOpen(chatID marotte.ChatID) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return (lc.pending != nil && !lc.pending.finalizing) || (lc.own != nil && !lc.own.finalizing)
}

func (r *turnRegistry) promptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.pending != nil && !lc.pending.finalizing {
		return lc.pending.Log, true
	}
	if lc.own != nil && lc.own.Source.Acknowledgeable() && !lc.own.finalizing {
		return lc.own.Log, true
	}
	return nil, false
}
