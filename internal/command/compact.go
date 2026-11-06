// Package command intercepts `/compact` before it reaches KAS, since typed
// slash commands are not parsed there and would otherwise reach the model
// as prose. It calls the real `_kiro/session/compact` verb instead.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

// errCompactRefused names every cause because KAS answers a bare {success: false} when it refuses
// to start (a turn, a prompt still being saved, a running compaction), when a started summary does
// not commit (the session, a prompt or the history moved, its 300 s bound, a model error), and when
// a PreCompact hook blocks it, with no field telling them apart.
var errCompactRefused = errors.New("the agent was still busy with a turn, a new message or another compaction, the summary failed or the chat changed before it finished, or a PreCompact hook blocked it (its card shows why). Try again once the chat is idle, or check the hook")

// compactBridge is what compact needs of a bridge: one call on its own session.
type compactBridge interface {
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
	SessionID() marotte.SessionID
}

// readLoopAwaiter is what cmdCompact needs of the steer queue.
type readLoopAwaiter interface {
	AwaitReadLoop(ctx context.Context, chatID marotte.ChatID, seq uint64) bool
}

// compact sends KAS's native `_kiro/session/compact`, the verb's one caller, and reports whether
// KAS accepted it. Accepted is not compacted: three of the five `{success: true}` outcomes compact
// nothing, so a caller must never synthesize the transcript boundary from the result. KAS answers
// after the summary commits, bounded at 300 s on its side, so ctx bounds only a wedged bridge.
// seq is the read loop position the reply arrived at.
func compact(ctx context.Context, bridge compactBridge) (accepted bool, seq uint64, err error) {
	resp, seq, err := bridge.CallAt(ctx, marotte.MethodSessionCompact, sessionParams(bridge))
	if err != nil {
		return false, 0, err
	}
	var result struct {
		Success bool `json:"success"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	return result.Success, seq, nil
}

// cmdCompact compacts the chat's context through KAS's native verb. Requires
// a live resident session, since compaction operates on the session's own
// message log.
//
// The narrow bridgeAccess parameter is deliberate: no store and no broadcaster,
// so a synthesized compaction boundary is not expressible here.
func cmdCompact(ctx context.Context, bridges bridgeAccess, folds readLoopAwaiter, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		return nil, StatusError(http.StatusConflict, errCompactNoBridge)
	}

	cctx, cancel := bridges.CallBudget(ctx, engineChangeBudget)
	defer cancel()
	accepted, seq, err := compact(cctx, bridge)
	if err != nil {
		slog.Warn("compact: call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}
	if !accepted {
		slog.Info("compact refused", "chat", cmd.ChatID)
		return nil, StatusError(http.StatusConflict, errCompactRefused)
	}
	// KAS writes summarization_completed before its reply, which reaches this call ahead of the
	// queued frame: answered at once, a prompt sent next opens its turn before the compaction folds.
	if folds != nil && !folds.AwaitReadLoop(ctx, cmd.ChatID, seq) {
		slog.Warn("compact: the frames before its reply never folded", "chat", cmd.ChatID)
	}
	slog.Info("compact accepted", "chat", cmd.ChatID)
	return responseWith(nil), nil
}
