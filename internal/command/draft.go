package command

// Composer drafts are server-side so they follow the user across devices. The client autosaves on a
// 600ms debounce, so the handler is minimal: no bridge call, a write that does not move the
// retention clock (ChatStore.SetDraft), and one broadcast only when something changed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// CmdSetDraft records the chat's unsent composer text. An empty Text is a
// legitimate value (how a sent or abandoned message clears). The reply
// carries the byte length rather than the text.
func CmdSetDraft(ctx context.Context, chats ChatStore, bus Broadcaster, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetDraftCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if len(p.Text) > marotte.MaxDraftBytes {
		return nil, StatusError(http.StatusRequestEntityTooLarge, errDraftTooLong)
	}
	// No UTF-8 check here: encoding/json already replaced any invalid byte
	// sequence in the decoded string. The store keeps its own, which guards
	// the Go-level API.

	state, err := chats.SetDraft(ctx, cmd.ChatID, p.Text)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	broadcastComposer(ctx, bus, cmd.ChatID, state)

	// Debug, not Info: fires every 600ms of typing. Never log the text.
	slog.Debug("draft set", "chat", cmd.ChatID, "bytes", len(p.Text))
	return responseWith(map[string]any{"bytes": len(p.Text)}), nil
}

// broadcastComposer publishes draft_changed for a write that landed, and
// does nothing for one that did not. A nil state means no record, or the
// same value already stored. The frame carries the `chat` stamp from
// state.Version, which the store filled under the chat's lock, because the
// composer is part of the chat projection the digest certifies.
func broadcastComposer(ctx context.Context, bus Broadcaster, chatID marotte.ChatID, state *marotte.ComposerState) {
	if state == nil {
		return
	}
	frame := marotte.NewEvent(marotte.EventDraftChanged, chatID, marotte.DraftChangedPayload{
		Text:        state.Text,
		Attachments: state.Attachments,
	})
	frame.Subject = marotte.NewSubjectStamp(string(subject.KindChat), string(chatID), state.Version)
	bus.Broadcast(ctx, frame)
}
