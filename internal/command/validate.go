package command

import (
	"errors"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// maxPromptBytes caps the text field of a prompt command.
const maxPromptBytes = 512 * 1024

// Static command errors returned to the client.
var (
	ErrMissingChatID  = errors.New("missing chat_id")
	ErrInvalidPayload = errors.New("invalid payload")
	errEmptyPrompt    = errors.New("empty prompt")
	errPromptTooLong  = errors.New("prompt too long")
	errDraftTooLong   = errors.New("draft too long")
	// set_attachments' two refusals: too many entries (413), a bad entry (400).
	errTooManyAttachments   = errors.New("too many attachments")
	errBadAttachmentPath    = errors.New("attachment path is empty or too long")
	errMissingMessageID     = errors.New("missing message_id")
	errNoBridge             = errors.New("no bridge")
	errRewindTargetNotFound = errors.New("rewind target is not a user message in this chat")
	// Prose, not a code: the client appends the reason to "Couldn't rewind
	// chat: ", so it reaches the user verbatim.
	errRewindNoSession         = errors.New("this chat has no agent session yet, so there is nothing to roll back to")
	errRewindSessionNotResumed = errors.New("this chat's original session could not be resumed, so there is nothing to roll back to")
	// Stands in for an internal error the client must not be shown.
	errRewindNoBridge = errors.New("this chat's agent session could not be started, so the rewind was not attempted")
	// errRewindTurnOpen is the 409 for a rewind while a turn is running or admitted:
	// KAS refuses a mid-turn revert, and the log's cut must not race a fold.
	errRewindTurnOpen = errors.New("a turn is still running in this chat. Wait for it to finish, then rewind")
	// Appended to a refused revert whose target carries no agent-side id: a turn from
	// before the id was recorded, one whose assignment frame never arrived, and one whose
	// id was dropped when the chat retired the session that minted it. One sentence serves
	// all three — the remedy is identical and marotte cannot tell which it was.
	errRewindNoAgentID = errors.New("marotte has no id for this turn in the agent's current session, so the agent may not be able to locate it. Turns sent from now on can be rewound")
	errBusy            = errors.New("busy")
	// errAlreadyAnswered is the 409 for a decision another surface settled
	// first. A code rather than prose: the client keys off it.
	errAlreadyAnswered = errors.New("already_answered")
	// errPermissionOptionNotOffered rejects a choice absent from the request, without
	// echoing either identifier.
	errPermissionOptionNotOffered = errors.New("option_not_offered")
	// errChatNotCreated is the 409 for a chat absent after a Mutate that
	// reported no error — a client-supplied id naming a tombstoned chat.
	errChatNotCreated = errors.New("chat could not be created")
	ErrChatNotFound   = errors.New("chat not found")
)

// validChatID reports whether id is safe to use as a chat identifier.
func validChatID(id marotte.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// ValidMessageID reports whether id is safe to echo on SSE and store on disk.
func ValidMessageID(id string) bool {
	return ids.ValidMessageID(id)
}

// ValidIdent reports whether s is a safe agent or model identifier.
func ValidIdent(s string) bool {
	return ids.ValidIdent(s)
}
