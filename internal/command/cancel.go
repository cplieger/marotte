package command

import (
	"context"
	"errors"
	"time"

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

// cancelBudget is the shared stdin's no-progress window: a write that cannot finish within it has reaped the engine.
const cancelBudget = 30 * time.Second

// cancelSession answers every pending permission ask in scope `cancelled`, then writes session/cancel on the same
// pipe, so KAS reads the answers first (ACP: a client cancelling a turn MUST answer its open permission requests).
// It is detached from the caller, so a client that leaves cannot strand a taken ask or skip the cancel. An engine
// end during the answers (marotte.ErrBridgeExited) and ErrCancelNotDelivered both write no session/cancel.
func cancelSession(ctx context.Context, perms permissionCanceller, sb Bridge, chatID marotte.ChatID, scope CancelScope) error {
	ctx, cancel := context.WithTimeout(durable.Context(ctx), cancelBudget)
	defer cancel()
	if err := perms.CancelPendingPermissions(ctx, chatID, scope); err != nil {
		return err
	}
	if sb == nil {
		return nil
	}
	return sb.Notify(ctx, marotte.MethodCancel, sessionParams(sb))
}
