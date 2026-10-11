package ids

import (
	"regexp"
	"strings"
)

const maxMessageIDBytes = 128

var validMessageIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]+$`)

// ValidMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message. This is the single source of
// truth; every boundary that accepts a message id delegates here.
func ValidMessageID(id string) bool {
	if id == "" || len(id) > maxMessageIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}

// ErrMsgInvalidChatID is the single source of truth for the HTTP error
// message returned when a chat ID fails validation.
const ErrMsgInvalidChatID = "invalid chat_id"

// ValidChatID reports whether id is a valid chat identifier: alphanumerics, hyphens and
// underscores, at most 128 chars (ULIDs, UUIDs, legacy "chat-<ms>"). It is a filesystem gate, since
// a chat id names the chat's own file, and every boundary delegates here.
func ValidChatID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// ValidIdent reports whether s is safe to use as an agent or model
// identifier. Empty strings pass (the field is optional); non-empty
// strings must match identRe AND must not start with '.' or '-' AND
// must not be all-dot strings. This is the single source of truth;
// every boundary that accepts an agent or model id delegates here.
func ValidIdent(s string) bool {
	if s == "" {
		return true
	}
	if !identRe.MatchString(s) {
		return false
	}
	if s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if r != '.' {
			return true
		}
	}
	return false
}

// ValidSessionID reports whether s is safe to use as an ACP session id.
// Rejects empty strings, strings over 128 bytes, path separators, NUL,
// and parent-dir references. Session ids (v3: `sess_`-prefixed) are
// concatenated into filesystem paths under $KIRO_HOME/sessions/; this
// function is the single source of truth for that safety gate.
func ValidSessionID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	if strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	if s == "." || s == ".." || strings.Contains(s, "..") {
		return false
	}
	return true
}
