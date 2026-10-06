package command

// Resuming a KAS session marotte has no chat record for (a TUI session, or one whose chat was
// deleted while retention kept the session): resume creates a chat bound to that session id, and
// the next OpenBridge takes the session/load path, whose replay becomes the transcript. No messages
// are copied.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// CmdResumeSession creates a chat bound to an existing KAS session so the
// stored conversation can be opened, and returns the chat plus its tab.
// The id is minted here when the envelope carries none.
func CmdResumeSession(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.ResumeSessionCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// The id reaches a filesystem path inside KAS and the reaper keep-list, so it is validated like
	// a chat id.
	if !ids.ValidSessionID(p.SessionID) || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	name := cmp.Or(p.Name, marotte.DefaultChatName)
	if len(name) > marotte.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// Minting per attempt would leave two chats bound to one KAS session.
	opened, err := mem.CreateChatAndOpen(ctx, ChatCreate{
		OpID:   p.OpID,
		ChatID: cmd.ChatID,
		Init: func(c *marotte.Chat) {
			// Init runs only when the record does not exist, which is what refuses to rebind a live
			// chat and strand its session.
			c.Name = name
			// RecordSession, not assignment: it keeps the reaper's keep-list chain invariant.
			c.RecordSession(p.SessionID)
		},
	})
	if err != nil {
		return nil, err
	}
	slog.Info("session resumed into a new chat",
		"chat_id", opened.Chat.ID, "acp_session", p.SessionID, "tab", opened.Subject.ID)
	return openedResponse(&opened, nil), nil
}
