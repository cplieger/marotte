package command

import (
	"errors"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// MaxPromptBytes caps the text field of a prompt command.
const MaxPromptBytes = 512 * 1024

// Static command errors returned to the client.
var (
	ErrMissingChatID  = errors.New("missing chat_id")
	ErrInvalidPayload = errors.New("invalid payload")
	errEmptyPrompt    = errors.New("empty prompt")
	errPromptTooLong  = errors.New("prompt too long")
	errDraftTooLong   = errors.New("draft too long")
	// An attachment list's two refusals, on set_attachments and prompt: too
	// many entries (413), a bad entry (400).
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
	// errAnswerNotDelivered is the 502 for an answer the agent never received: the ask is
	// pending again, so the client re-enables its card. A code, like errAlreadyAnswered.
	errAnswerNotDelivered = errors.New("answer_not_delivered")
	// errAskWithdrawn is the 410 for an answer that reached nobody because the ask settled
	// first; the client leaves the card to that settlement. A code, like errAlreadyAnswered.
	errAskWithdrawn = errors.New("ask_withdrawn")
	// errPermissionOptionNotOffered rejects a choice absent from the request, without
	// echoing either identifier.
	errPermissionOptionNotOffered = errors.New("option_not_offered")
	// errRejectionReasonInvalid refuses a deny note over the cap or one paired
	// with turn-approval decisions, which KAS would ignore.
	errRejectionReasonInvalid = errors.New("rejection_reason_invalid")
	// errAlwaysResourceInvalid refuses an always answer with no pattern, a pattern the rule
	// format cannot hold, or a pattern on an answer that saves no rule.
	errAlwaysResourceInvalid = errors.New("always_resource_invalid")
	// errChatNotCreated is the 409 for a chat absent after a Mutate that
	// reported no error — a client-supplied id naming a tombstoned chat.
	errChatNotCreated = errors.New("chat could not be created")
	ErrChatNotFound   = errors.New("chat not found")
)

func validChatID(id marotte.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// validMessageID reports whether id is safe to echo on SSE and store on disk.
func validMessageID(id string) bool {
	return ids.ValidMessageID(id)
}

// validIdent reports whether s is a safe agent or model identifier.
func validIdent(s string) bool {
	return ids.ValidIdent(s)
}
