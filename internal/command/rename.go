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

// SessionRenamer renames a KAS session, resident or not, with a session-less engine call.
type SessionRenamer interface {
	RenameSession(ctx context.Context, sessionID, title string) error
}

var (
	errRenameEmpty   = errors.New("name is empty")
	errRenameTooLong = errors.New("name is too long")
	errRenameRefused = errors.New("_kiro/session/rename refused")
)

// BotReloader marks a Bot's resident session to reload at its next open, so an edited role reaches it. A chat with no
// resident session has nothing to mark: its next open builds the role from the record.
type BotReloader interface {
	MarkBotReload(chatID marotte.ChatID)
}

// cmdRenameChat records the user's own name for a chat and latches it on every KAS session in its
// chain, so every surface reads the same name and agent titles stop. The record is canonical: a
// failed rename RPC still answers success, and the session door reconcile repairs KAS on the next
// open. A started Bot's role carries its name, so a changed name marks its session for reload.
func cmdRenameChat(
	ctx context.Context,
	chats chatStore,
	renamer SessionRenamer,
	reload BotReloader,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.RenameChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	name, err := userChatName(p.Name)
	if err != nil {
		return nil, StatusError(http.StatusBadRequest, err)
	}
	var chain []string
	found, changed, startedBot := false, false, false
	if _, err := chats.Mutate(ctx, cmd.ChatID, marotte.BotWriter(func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		found = true
		chain = c.SessionChain()
		startedBot = c.Bot != nil && c.Bot.StartedAt != 0
		changed = setUserName(c, name)
		return changed
	})); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !found {
		return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	renameSessions(ctx, renamer, cmd.ChatID, chain, name)
	if changed && startedBot {
		reload.MarkBotReload(cmd.ChatID)
	}
	slog.Info("chat renamed by user", "chat_id", cmd.ChatID)
	return responseWith(map[string]any{"name": name}), nil
}

// userChatName is raw as a user's chat name: sanitized as a title, refused when that leaves it empty or past the cap.
func userChatName(raw string) (string, error) {
	name := translate.SanitizeTitle(raw)
	if name == "" {
		return "", errRenameEmpty
	}
	if userNameTooLong(name) {
		return "", errRenameTooLong
	}
	return name, nil
}

// setUserName writes name onto c as the user's own, reporting whether c changed.
func setUserName(c *marotte.Chat, name string) bool {
	if c.Name == name && c.NameSetByUser {
		return false
	}
	c.Name = name
	c.NameSetByUser = true
	return true
}

// renameSessions latches name on every KAS session in chain. A failed RPC only warns: the record is canonical.
func renameSessions(ctx context.Context, renamer SessionRenamer, chatID marotte.ChatID, chain []string, name string) {
	for _, sessionID := range chain {
		if err := renamer.RenameSession(ctx, sessionID, name); err != nil {
			slog.Warn("chat renamed; KAS session rename failed, the next open repairs it",
				"chat_id", chatID, "acp_session", sessionID, "error", err)
		}
	}
}

// userNameTooLong applies the user-name cap, counted in UTF-16 units because the
// client's input measures them that way.
func userNameTooLong(name string) bool {
	return len(utf16.Encode([]rune(name))) > marotte.MaxUserChatNameUnits
}

// RenameOutcome reads a _kiro/session/rename reply: a call error (the engine's in-band
// refusal arrives as one) or {success:false} is a failure.
func RenameOutcome(resp *marotte.RPCResponse, err error) error {
	if err != nil {
		return err
	}
	if resp == nil {
		return errRenameRefused
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
