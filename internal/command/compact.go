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

// errCompactRefused is the one failure a caller can surface. KAS returns a
// bare {success: false} both for a turn in flight and for a compaction
// already running, with no field distinguishing them.
var errCompactRefused = errors.New("cannot compact right now, because a turn or another compaction is running. Try again when it finishes")

// CompactBridge is what Compact needs of a bridge: one call on its own session.
type CompactBridge interface {
	Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error)
	SessionID() marotte.SessionID
}

// Compact sends KAS's native `_kiro/session/compact`, the verb's one caller, and reports whether
// KAS accepted it. Accepted is not compacted: three of the five `{success: true}` outcomes compact
// nothing, so a caller must never synthesize the transcript boundary from the result. KAS answers
// after the summary commits, bounded at 300 s on its side, so ctx bounds only a wedged bridge.
func Compact(ctx context.Context, bridge CompactBridge) (bool, error) {
	resp, err := bridge.Call(ctx, marotte.MethodSessionCompact, sessionParams(bridge))
	if err != nil {
		return false, err
	}
	var result struct {
		Success bool `json:"success"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	return result.Success, nil
}

// cmdCompact compacts the chat's context through KAS's native verb. Requires
// a live resident session, since compaction operates on the session's own
// message log.
//
// The narrow bridgeAccess parameter is deliberate: no store and no broadcaster,
// so a synthesized compaction boundary is not expressible here.
func cmdCompact(ctx context.Context, bridges bridgeAccess, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		return nil, StatusError(http.StatusConflict, errNoBridge)
	}

	accepted, err := Compact(ctx, bridge)
	if err != nil {
		slog.Warn("compact: call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}
	if !accepted {
		slog.Info("compact refused", "chat", cmd.ChatID)
		return nil, StatusError(http.StatusConflict, errCompactRefused)
	}
	slog.Info("compact accepted", "chat", cmd.ChatID)
	return responseWith(nil), nil
}
