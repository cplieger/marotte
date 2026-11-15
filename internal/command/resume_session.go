package command

// Resuming a KAS session marotte has no chat record for (a TUI session, or one whose chat was
// deleted while retention kept the session): resume creates a chat bound to that session id, and
// the next OpenBridge takes the session/load path, whose replay becomes the transcript. No messages
// are copied.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// cmdResumeSession creates a chat bound to an existing KAS session so the
// stored conversation can be opened, and returns the chat plus its tab.
// The id is minted here when the envelope carries none.
func cmdResumeSession(ctx context.Context, mem *Membership, conversations listedConversations, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.ResumeSessionCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// The id reaches a filesystem path inside KAS and the reaper keep-list, so it is validated like
	// a chat id.
	if !ids.ValidSessionID(p.SessionID) || !validIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	name := cmp.Or(p.Name, marotte.DefaultChatName)
	if len(name) > marotte.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if err := refuseUnlistedSession(ctx, conversations, p.SessionID); err != nil {
		return nil, err
	}

	// Minting per attempt would leave two chats bound to one KAS session.
	opened, err := mem.CreateChatAndOpen(ctx, &ChatCreate{
		OpID:         p.OpID,
		ChatID:       cmd.ChatID,
		ClaimSession: p.SessionID,
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

// errNotAConversation refuses resuming a session KAS does not list as one of the workspace's conversations, a
// workflow step's among them: the step's run still owns it.
var errNotAConversation = errors.New("this session is not a conversation in this workspace")

// refuseUnlistedSession fails closed: a step session loaded into a chat would answer the step's run from a second
// resident, and KAS's whole list is too large to name the steps, so only a listed conversation is resumed, and an
// unreadable list refuses too, retryably.
func refuseUnlistedSession(ctx context.Context, conversations listedConversations, sessionID string) error {
	if conversations == nil {
		return nil
	}
	listed, err := conversations.ListedConversation(ctx, sessionID)
	switch {
	case err != nil:
		slog.Warn("could not read whether a session is a conversation, so it was not resumed",
			"acp_session", sessionID, "error", err)
		return StatusError(http.StatusServiceUnavailable,
			errors.New("could not check this session just now; try again in a moment"))
	case !listed:
		slog.Info("refused resuming a session KAS does not list as a conversation", "acp_session", sessionID)
		return StatusError(http.StatusConflict, errNotAConversation)
	}
	return nil
}
