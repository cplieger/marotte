package command

import (
	"context"
	"errors"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// CancelScope selects which of a chat's asks a cancel answers.
type CancelScope uint8

const (
	// ScopeChatSession is the chat's own session and its subagents: a chat-parented run's step asks stay pending,
	// because a Stop does not stop the run.
	ScopeChatSession CancelScope = iota + 1
	// ScopeChatAndRuns is every ask filed under the chat, for a teardown that cancels the chat's runs next.
	ScopeChatAndRuns
)

// ErrCancelNotDelivered is a permission card a cancel could not answer on a session that is still live, so no
// session/cancel was written and the reply goes on.
var ErrCancelNotDelivered = errors.New("a permission card could not be answered")

// cancelSession answers every pending permission ask in scope `cancelled`, then writes session/cancel on the same
// pipe, so KAS reads the answers first (ACP: a client cancelling a turn MUST answer its open permission requests).
// It is detached from the caller, so a client that leaves cannot strand a taken ask or skip the cancel. An engine
// end during the answers (marotte.ErrBridgeExited) and ErrCancelNotDelivered both write no session/cancel. A
// session still opening has no turn on the wire yet, so its refusal is nil: the stop's grace ends the admitted prompt.
func cancelSession(ctx context.Context, perms permissionCanceller, sb Bridge, chatID marotte.ChatID, scope CancelScope) error {
	// No deadline of its own: each write is bounded by the bridge, which waits out an engine that reads late.
	ctx = durable.Context(ctx)
	if err := perms.CancelPendingPermissions(ctx, chatID, scope); err != nil {
		return err
	}
	if sb == nil {
		return nil
	}
	err := sb.Notify(ctx, marotte.MethodCancel, sessionParams(sb))
	if errors.Is(err, marotte.ErrSessionOpening) {
		return nil
	}
	return err
}
