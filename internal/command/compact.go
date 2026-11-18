// Package command intercepts `/compact` before it reaches KAS, since typed
// slash commands are not parsed there and would otherwise reach the model
// as prose. It calls the real `_kiro/session/compact` verb instead.
package command

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

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

// CompactOutcome is how a reader's compaction request was taken.
type CompactOutcome uint8

const (
	// CompactStarted is a call sent to kiro-cli; its outcome reaches the chat later, as kiro-cli's compaction entry
	// or one toast.
	CompactStarted CompactOutcome = iota
	// CompactRunning is a press while the chat's call runs: no second call is sent.
	CompactRunning
	// CompactNoSession is a chat with no live agent session to compact.
	CompactNoSession
)

// cmdCompact sends the chat's one compaction call and answers once it is under way, since KAS answers after the
// summary commits, minutes later. A synthesized compaction boundary is not expressible here: the transcript's comes
// from KAS.
func cmdCompact(ctx context.Context, compacts compactRequester, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	out, err := compacts.RequestCompact(ctx, cmd.ChatID)
	switch {
	case err != nil:
		slog.Warn("compact: call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	case out == CompactNoSession:
		return nil, StatusError(http.StatusConflict, errCompactNoBridge)
	case out == CompactRunning:
		slog.Info("compact already running", "chat", cmd.ChatID)
	default:
		slog.Info("compact started", "chat", cmd.ChatID)
	}
	return responseWith(nil), nil
}
