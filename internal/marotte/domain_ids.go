package marotte

import (
	"crypto/rand"
	"encoding/hex"
)

// SecretMask is the placeholder value returned for every secret on
// public reads. Clients send this unchanged on update to keep the
// stored value; any other value replaces it.
const SecretMask = "***"

// DefaultChatName is the fallback name for newly created chats when the
// client does not supply one.
const DefaultChatName = "New conversation"

// MaxDraftBytes caps a chat's persisted composer draft, far below a prompt's 512 KiB because a
// draft is re-saved every 600ms of typing and read on every chat open.
const MaxDraftBytes = 16 * 1024

// MaxAttachments caps how many files one chat may stage beside its draft: a product limit for a
// one-line pill row, not a decode bound.
const MaxAttachments = 32

// MaxAttachmentPathBytes caps one staged path. Deliberately MaxChatNameBytes
// rather than a new number: both are a single filesystem-shaped string the
// client sends, and PATH_MAX is 4096 on Linux while every path this app can
// produce comes from its own file browser under the workspace root.
const MaxAttachmentPathBytes = MaxChatNameBytes

// MaxChatNameBytes caps the byte length of chat names at creation and
// rename boundaries. All code paths that set Chat.Name should enforce
// this limit.
const MaxChatNameBytes = 512

// NewChatID mints a chat identifier: "c-" plus the hex of 16 bytes from crypto/rand, since the id
// addresses a conversation (URL segment, file name, session-chain key) and must not be guessable.
// The shape satisfies ids.ValidChatID, as the older c-<ts>-<rand> does.
func NewChatID() ChatID {
	var b [16]byte
	rand.Read(b[:])
	return ChatID("c-" + hex.EncodeToString(b[:]))
}

// ErrMsgUtilityUnavailable is the canonical error message returned when
// the utility bridge (LLM prompt function) is not wired. Used by both
// the git and server packages to keep the error string in one place.
const ErrMsgUtilityUnavailable = "utility bridge not available"

// ChatID is a typed wrapper for chat identifiers. The underlying string
// marshals identically to JSON, preserving the wire contract.
type ChatID string

// String implements fmt.Stringer for logging convenience.
func (c ChatID) String() string { return string(c) }

// SessionID is a typed wrapper for ACP session identifiers. Values are
// validated via ids.ValidSessionID before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type SessionID string

// String implements fmt.Stringer for logging convenience.
func (s SessionID) String() string { return string(s) }

// ModelID is a typed wrapper for model identifiers. Values are
// validated via ids.ValidIdent before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type ModelID string

// String implements fmt.Stringer for logging convenience.
func (m ModelID) String() string { return string(m) }
