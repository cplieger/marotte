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

// CompactBridge is what Compact needs of a bridge: one call on its own session.
type CompactBridge interface {
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
	SessionID() marotte.SessionID
}

// Compact sends KAS's native `_kiro/session/compact`, the verb's one caller, and reports whether
// KAS accepted it. Accepted is not compacted: three of the five `{success: true}` outcomes compact
// nothing, so a caller must never synthesize the transcript boundary from the result. KAS answers
// after the summary commits, bounded at 300 s on its side, so ctx bounds only a wedged bridge.
// seq is the read loop position the reply arrived at.
func Compact(ctx context.Context, bridge CompactBridge) (accepted bool, seq uint64, err error) {
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

// CompactOutcome is how a reader's compaction request ended.
type CompactOutcome uint8

const (
	// CompactAccepted is KAS accepting the call, which is not a compaction (Compact).
	CompactAccepted CompactOutcome = iota
	// CompactQueued is a compaction waiting on the chat's run order behind what runs.
	CompactQueued
	// CompactRefused is KAS's {success: false}.
	CompactRefused
	// CompactNoSession is a chat with no live agent session to compact.
	CompactNoSession
)

// cmdCompact asks the chat's compactor for a compaction: now on an idle chat, else at the running turn's close,
// answered queued. A synthesized compaction boundary is not expressible here: the transcript's comes from KAS.
func cmdCompact(ctx context.Context, compacts compactRequester, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	out, err := compacts.RequestCompact(ctx, cmd.ChatID)
	switch {
	case err != nil:
		slog.Warn("compact: call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	case out == CompactQueued:
		slog.Info("compact queued for the turn's end", "chat", cmd.ChatID)
		return responseWith(map[string]any{"queued": true}), nil
	case out == CompactNoSession:
		return nil, StatusError(http.StatusConflict, errCompactNoBridge)
	case out == CompactRefused:
		slog.Info("compact refused", "chat", cmd.ChatID)
		return nil, StatusError(http.StatusConflict, errCompactRefused)
	}
	slog.Info("compact accepted", "chat", cmd.ChatID)
	return responseWith(nil), nil
}

// cmdCancelCompact withdraws the compaction entry the payload names (the chat's one when it names none); a stale id
// is success.
func cmdCancelCompact(ctx context.Context, compacts compactRequester, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.CancelCompactCommand
	if len(cmd.Payload) > 0 && json.Unmarshal(cmd.Payload, &p) != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if err := compacts.CancelCompact(ctx, cmd.ChatID, p.ID); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	return responseOK, nil
}
