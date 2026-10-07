package marotte

// Wire shapes for POST /api/command; routing lives in agent/command.go.

import (
	"encoding/json"
	"strings"
	"unicode/utf16"
)

// CommandType identifies the kind of client command posted to /api/command.
type CommandType string

// Command type constants; each is a key in agent/command.go's dispatch map.
const (
	CmdCreateChat          CommandType = "create_chat"
	CmdResumeSession       CommandType = "resume_session"
	CmdForkChat            CommandType = "fork_chat"
	CmdPrompt              CommandType = "prompt"
	CmdCancel              CommandType = "cancel"
	CmdDeleteChat          CommandType = "delete_chat"
	CmdSwitchModel         CommandType = "switch_model"
	CmdPermissionResponse  CommandType = "permission_response"
	CmdElicitationResponse CommandType = "elicitation_response"
	CmdUserInputResponse   CommandType = "user_input_response"
	CmdRewindChat          CommandType = "rewind_chat"
	CmdCompact             CommandType = "compact"
	CmdSetEffort           CommandType = "set_effort"
	CmdSetThinking         CommandType = "set_thinking"
	CmdSetDraft            CommandType = "set_draft"
	CmdSetAttachments      CommandType = "set_attachments"
	CmdSetMode             CommandType = "set_mode"
	CmdCreateHook          CommandType = "create_hook"
	CmdSetSupervisedMode   CommandType = "set_supervised_mode"
	CmdSteer               CommandType = "steer"
	CmdSteerClear          CommandType = "steer_clear"
	CmdQueuePrompt         CommandType = "queue_prompt"
	CmdUnqueuePrompt       CommandType = "unqueue_prompt"
	CmdSetInterruptMode    CommandType = "set_interrupt_mode"
	CmdRenameChat          CommandType = "rename_chat"
	// CmdSteerRemove drops ONE waiting steer: KAS has no per-steer verb, so the
	// server clears the buffer and resends the others together, in order.
	CmdSteerRemove CommandType = "steer_remove"
	// The four tab commands. Membership is a mutation, so it rides this envelope
	// rather than a second REST surface with its own failure semantics.
	CmdOpenTab     CommandType = "open_tab"
	CmdCloseTab    CommandType = "close_tab"
	CmdReorderTabs CommandType = "reorder_tabs"
	CmdPinTab      CommandType = "pin_tab"
	CmdReparentTab CommandType = "reparent_tab"
	// CmdApproveSpecPhase records a human sign-off on one phase of a spec. It is
	// a COMMAND rather than a REST POST because invariant 1 puts every mutation
	// through this envelope; its chat_id is EMPTY, because a spec is
	// workspace-global rather than a chat's.
	CmdApproveSpecPhase CommandType = "approve_spec_phase"
)

// ClientCommand is the envelope for every command the browser posts. Type
// determines how Payload unmarshals; idempotency is the Idempotency-Key header
// (internal/server/idempotency.go), never a body field. A payload's own
// request_id means something else: the ACP request id being answered.
type ClientCommand struct {
	Type    CommandType     `json:"type"`
	ChatID  ChatID          `json:"chat_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// PromptCommand is the payload for type="prompt". Text or at least one attachment is required; Text
// is capped at 512 KiB (413), Attachments at MaxAttachments (413) of MaxAttachmentPathBytes each
// (400), each confined before it is read.
type PromptCommand struct {
	Text        string       `json:"text"`
	MessageID   string       `json:"message_id"`
	Model       string       `json:"model,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is a file staged beside a prompt; its extension decides whether it
// travels as a document content block or as a text path reference.
type Attachment struct {
	Path string `json:"path"` // workspace-relative
	Name string `json:"name"`
}

// CreateChatCommand is the payload for type="create_chat".
//
// Name is optional, capped at maxChatNameBytes, defaulting to "New
// conversation". Model is optional (validIdent). Invalid input returns HTTP 400.
type CreateChatCommand struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
	// OpID correlates every attempt of one create gesture, so a repeat resolves
	// to the chat the first attempt made. Optional; unlike Idempotency-Key it
	// covers the fall-through past that cache's TTL (command/create_ledger.go).
	OpID string `json:"op_id,omitempty"`
}

// ResumeSessionCommand is the payload for type="resume_session": adopt a KAS
// session as a new chat, bound to SessionID at creation so the next bridge takes
// the session/load path and the replay projection supplies the transcript.
type ResumeSessionCommand struct {
	// SessionID is the KAS session to adopt. Validated with ids.ValidSessionID.
	SessionID string `json:"session_id"`
	// Name seeds the chat title, normally the session title KAS reported.
	Name string `json:"name,omitempty"`
	// OpID correlates every attempt of one resume. See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
}

// ForkChatCommand is the payload for type="fork_chat": a TANGENT that starts with the parent's
// conversation behind it and diverges, nothing syncing the two afterwards. The new chat's id is
// minted server-side and returned.
type ForkChatCommand struct {
	ParentChatID ChatID `json:"parent_chat_id"`
	Title        string `json:"title,omitempty"`
	// OpID correlates every attempt of one fork; without it a retry would fork
	// again, producing a second session and chat. See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
}

// ForkOutcomeFresh and ForkOutcomeForked are the two paths fork_chat can take.
// `forked` means KAS returned a session id carrying the parent's context;
// `fresh` means no usable forked session was returned. The tangent opens either
// way, but only a forked session inherits context.
const (
	ForkOutcomeForked = "forked"
	ForkOutcomeFresh  = "fresh"
)

// SwitchModelCommand is the payload for type="switch_model". A live session is
// switched in place. The fallback restarts the bridge and first attempts to load
// the existing session before creating a fresh one.
type SwitchModelCommand struct {
	// Empty or "auto" keeps the current model: a bare restart of the bridge,
	// useful when the session is wedged.
	Model string `json:"model,omitempty"`
}

// PermissionResponseCommand is the payload for type="permission_response".
type PermissionResponseCommand struct {
	// FileDecisions answers a TURN APPROVAL: action id → accept. KAS treats an
	// OMITTED id as a reject, so the client sends a decision per offered file.
	FileDecisions map[string]bool `json:"file_decisions,omitempty"`
	OptionID      string          `json:"option_id"`
	// RejectionReason is the user's note on a deny, at most
	// MaxRejectionReasonRunes once trimmed. Never paired with FileDecisions.
	RejectionReason string `json:"rejection_reason,omitempty"`
	// AlwaysResource is the pattern an allow_always or reject_always answer saves as a
	// user rule, required with those options and refused with any other.
	AlwaysResource string `json:"always_resource,omitempty"`
	RequestID      int64  `json:"request_id"`
}

// MaxRejectionReasonRunes caps a deny note, matching the TUI's own cap.
const MaxRejectionReasonRunes = 1000

// ElicitationResponseCommand is the payload for type="elicitation_response".
// RequestID echoes the elicitation_needed event. Action is "accept" | "decline"
// | "cancel"; Content carries the filled form values on accept only, forwarded
// verbatim to kiro-cli.
type ElicitationResponseCommand struct {
	Action    string          `json:"action"`
	Content   json.RawMessage `json:"content,omitempty"`
	RequestID int64           `json:"request_id"`
}

// UserInputActionAnswered and UserInputActionDismissed are the accepted actions
// for type="user_input_response". KAS treats anything non-answered as dismissed
// and advances the agent to its next phase, so no other values exist.
const (
	UserInputActionAnswered  = "answered"
	UserInputActionDismissed = "dismissed"
)

// UserInputResponseCommand is the payload for type="user_input_response".
// RequestID echoes the user_input_needed event. Answer is the answer TEXT
// (kiro-cli's contract is a plain string): an option's title, "Title [Sub1,
// Sub2]" for sub-options, or the typed text. Required when Action is "answered".
type UserInputResponseCommand struct {
	Action    string `json:"action"`
	Answer    string `json:"answer,omitempty"`
	RequestID int64  `json:"request_id"`
}

// RewindChatCommand is the payload for type="rewind_chat": revert THIS chat to a past turn,
// dropping the addressed message and everything after it and rolling the files back. MessageID must
// name a USER message, which KAS's revert verb enforces.
type RewindChatCommand struct {
	MessageID string `json:"message_id"`
	// Confirmed says the reader was told which live runs the cut would stop and
	// chose to go on. Without it a cut holding a live run's launch answers 409
	// naming the runs, and nothing is reverted.
	Confirmed bool `json:"confirmed,omitempty"`
}

// SetEffortCommand is the payload for type="set_effort", applying a reasoning
// effort level to the active session.
type SetEffortCommand struct {
	// Level is a tier id from the model's own catalog, shape-validated only:
	// KAS owns the vocabulary.
	Level EffortLevel `json:"level"`
}

// SetDraftCommand is the payload for type="set_draft": this chat's unsent composer text, capped at
// MaxDraftBytes. Empty is a value (how a message clears); a no-op on a chat that is not a server
// record yet.
type SetDraftCommand struct {
	Text string `json:"text"`
}

// SetAttachmentsCommand is the payload for type="set_attachments": the WHOLE list of paths staged
// beside the draft, every time, since a per-file delta would need an ordering the wire lacks. Empty
// is a value; a no-op like set_draft.
type SetAttachmentsCommand struct {
	Paths []string `json:"paths"`
}

// SetModeCommand is the payload for type="set_mode". ModeID names an entry in
// the workspace mode catalog — bundled workflow modes and workspace custom agents
// alike. Applied to a live session in place; for a chat whose bridge has not
// started the mode is persisted and applied at session/new (StartOpts.Mode).
type SetModeCommand struct {
	ModeID string `json:"mode_id"`
}

// SetThinkingCommand is the payload for type="set_thinking": the effort slider's
// Off stop sends false; picking any effort tier turns thinking back on through
// set_effort itself.
type SetThinkingCommand struct {
	Enabled bool `json:"enabled"`
}

// RenameChatCommand is the payload for type="rename_chat": the user's own name for
// the chat, at most MaxUserChatNameUnits UTF-16 code units after sanitizing.
type RenameChatCommand struct {
	Name string `json:"name"`
}

// MaxUserChatNameUnits caps a user rename in UTF-16 code units, the unit an HTML
// input's maxlength counts.
const MaxUserChatNameUnits = 128

// kasTitleCap is KAS's own title cap, in JS string length (UTF-16 code units).
const kasTitleCap = 80

// KASStoredTitle is the title KAS keeps for a rename to name: trimmed, and past 80
// UTF-16 code units cut to 77 plus "...". A reconcile compares against this, never
// against name, or a long name would re-rename on every load.
func KASStoredTitle(name string) string {
	t := strings.TrimSpace(name)
	units := utf16.Encode([]rune(t))
	if len(units) <= kasTitleCap {
		return t
	}
	return string(utf16.Decode(units[:kasTitleCap-3])) + "..."
}

// EffortLevel is a typed enum for reasoning effort levels.
type EffortLevel string

// Well-known effort levels, used where marotte itself picks a tier. Not the
// valid set: the per-model catalog owns which tiers exist.
const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"
)

// Valid reports whether e is plausibly an effort-level id: a short lowercase token (letter first;
// letters, digits, hyphens; at most 32 bytes). A shape check, not a closed set: the vocabulary is
// per model and upstream-owned.
func (e EffortLevel) Valid() bool {
	if len(e) == 0 || len(e) > 32 {
		return false
	}
	for i := range len(e) {
		c := e[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c == '-' || (c >= '0' && c <= '9')):
		default:
			return false
		}
	}
	return true
}

// SetSupervisedModeCommand is the payload for type="set_supervised_mode".
// Records the chat's supervised choice and, on a running session, sets KAS's
// `autopilot` config option. It gates nothing itself: KAS holds the turn's writes
// and asks for one approval, so there is no marotte-side queue to drain.
type SetSupervisedModeCommand struct {
	Enabled bool `json:"enabled"`
}

// SteerCommand is the payload for type="steer": a message that joins the RUNNING turn, merged by
// KAS at the next node boundary. Text is required, trimmed and capped at maxSteerBytes.
type SteerCommand struct {
	Text      string `json:"text"`
	MessageID string `json:"message_id"`
}

// SteerRemoveCommand is the payload for type="steer_remove". SteerID is the key of
// a dock row the user sent; the rows they kept are resent together, in order.
type SteerRemoveCommand struct {
	SteerID string `json:"steer_id"`
}

// CancelCommand is the payload for type="cancel". Lead is the key of the dock row
// the stop is for: the unread steers the stop resends go out with it first.
type CancelCommand struct {
	Lead string `json:"lead,omitempty"`
}

// QueuePromptCommand is the payload for type="queue_prompt": a message held for
// the end of the running turn. Text and MessageID follow PromptCommand's rules;
// Attachments are capped at MaxAttachments and read when the row is sent.
type QueuePromptCommand struct {
	Text        string       `json:"text"`
	MessageID   string       `json:"message_id"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// UnqueuePromptCommand is the payload for type="unqueue_prompt". An id no row
// carries is success: two devices can discard one row.
type UnqueuePromptCommand struct {
	MessageID string `json:"message_id"`
}

// SetInterruptModeCommand is the payload for type="set_interrupt_mode".
type SetInterruptModeCommand struct {
	Mode InterruptMode `json:"mode"`
}

// OpenTabCommand is the payload for type="open_tab": open a tab for something that already exists.
// It never mints, so (Kind, Ref) is a key; an already-open (Kind, Ref) mutates nothing and emits no
// event.
type OpenTabCommand struct {
	// Kind must be one of the nine (TabKind.Valid).
	Kind TabKind `json:"kind"`
	// Ref is required for every kind but a singleton, where it must be empty. A
	// chat ref is checked against the chat store, which is what makes an open
	// racing a delete a refusal rather than a tab pointing at nothing.
	Ref string `json:"ref,omitempty"`
	// Parent names an already-open tab to hang this one under; one that is not
	// open promotes the new tab to top level rather than refusing it.
	Parent string `json:"parent,omitempty"`
	// OpID correlates the frame this open produces with the dispatch that asked
	// for it. See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
	// Owns means closing this tab tears down what it shows. The client decides,
	// because only the caller knows whether it launched the thing.
	Owns bool `json:"owns,omitempty"`
}

// CloseTabCommand is the payload for type="close_tab". Closing an id that is not open is not an
// error. For a CHAT tab it also runs the chat teardown and keeps the record under retention.
type CloseTabCommand struct {
	ID   string `json:"id"`
	OpID string `json:"op_id,omitempty"`
}

// ReorderTabsCommand is the payload for type="reorder_tabs": the whole expanded order, every open
// tab once. No base-version precondition: the exact-set check is sufficient, and a version one
// would discard a valid drag.
type ReorderTabsCommand struct {
	OpID  string   `json:"op_id,omitempty"`
	Order []string `json:"order"`
}

// PinTabCommand is the payload for type="pin_tab". Idempotent in both
// directions: a tab already in that state bumps no version and emits nothing.
type PinTabCommand struct {
	ID     string `json:"id"`
	OpID   string `json:"op_id,omitempty"`
	Pinned bool   `json:"pinned"`
}

// ReparentTabCommand is the payload for type="reparent_tab": hang an open tab
// under an open chat tab. Idempotent when the parent is unchanged; 404 for an
// id that is not open, 409 when Parent is not an open chat tab.
type ReparentTabCommand struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	OpID   string `json:"op_id,omitempty"`
}

// ApproveSpecPhaseCommand is the payload for type="approve_spec_phase": a human approved Phase of
// the spec at Dir, as it was at Hash. Hash is a compare-and-swap precondition the handler
// re-checks, not a value it trusts.
type ApproveSpecPhaseCommand struct {
	Dir   string `json:"dir"`
	Phase string `json:"phase"`
	Hash  string `json:"hash"`
}
