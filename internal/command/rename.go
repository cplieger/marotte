package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"unicode/utf16"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// SessionRenamer renames a KAS session no live process here holds, through the
// utility session.
type SessionRenamer interface {
	RenameSession(ctx context.Context, sessionID, title string) error
}

var (
	errRenameEmpty   = errors.New("name is empty")
	errRenameTooLong = errors.New("name is too long")
	errRenameRefused = errors.New("_kiro/session/rename refused")
)

// CmdRenameChat records the user's own name for a chat and latches it on every KAS session in its
// chain, so every surface reads the same name and agent titles stop. The record is canonical: a
// failed rename RPC still answers success, and the session door reconcile repairs KAS on the next
// open.
func CmdRenameChat(
	ctx context.Context,
	bridges BridgeAccess,
	chats ChatStore,
	renamer SessionRenamer,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.RenameChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	name := translate.SanitizeTitle(p.Name)
	if name == "" {
		return nil, StatusError(http.StatusBadRequest, errRenameEmpty)
	}
	if userNameTooLong(name) {
		return nil, StatusError(http.StatusBadRequest, errRenameTooLong)
	}
	var current string
	var chain []string
	found := false
	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		found = true
		current = c.ACPSessionID
		chain = c.SessionChain()
		if c.Name == name && c.NameSetByUser {
			return false
		}
		c.Name = name
		c.NameSetByUser = true
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !found {
		return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	bridge := bridges.Bridge(cmd.ChatID)
	for _, sessionID := range chain {
		if err := renameSession(ctx, bridge, renamer, sessionID, sessionID == current, name); err != nil {
			slog.Warn("chat renamed; KAS session rename failed, the next open repairs it",
				"chat_id", cmd.ChatID, "acp_session", sessionID, "error", err)
		}
	}
	slog.Info("chat renamed by user", "chat_id", cmd.ChatID)
	return responseWith(map[string]any{"name": name}), nil
}

// renameSession picks the one process allowed to write one session's title: the bridge holding it
// renames in place; otherwise the utility arm writes stored metadata, never for a session a bridge
// holds. A bridge on another session (or still spawning) sends nothing; the session door reconcile
// covers it.
func renameSession(ctx context.Context, b Bridge, renamer SessionRenamer, sessionID string, current bool, name string) error {
	if b != nil && string(b.SessionID()) == sessionID {
		resp, err := b.Call(ctx, marotte.MethodSessionRename, map[string]any{"sessionId": sessionID, "title": name})
		return RenameOutcome(resp, err)
	}
	if (current && b != nil) || renamer == nil {
		return nil
	}
	return renamer.RenameSession(ctx, sessionID, name)
}

// userNameTooLong applies the user-name cap, counted in UTF-16 units because the
// client's input measures them that way.
func userNameTooLong(name string) bool {
	return len(utf16.Encode([]rune(name))) > marotte.MaxUserChatNameUnits
}

// RenameOutcome reads a _kiro/session/rename reply: an RPC error, an in-band error
// or {success:false} are all failures.
func RenameOutcome(resp *marotte.RPCResponse, err error) error {
	if err != nil {
		return err
	}
	if resp == nil {
		return errRenameRefused
	}
	if resp.Error != nil {
		return resp.Error
	}
	return RenameResultOutcome(resp.Result)
}

// RenameResultOutcome reads the result half of a rename reply.
func RenameResultOutcome(result json.RawMessage) error {
	var r struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(result, &r) != nil || !r.Success {
		return errRenameRefused
	}
	return nil
}
