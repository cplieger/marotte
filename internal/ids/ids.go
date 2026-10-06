// Package ids is marotte's identifier vocabulary: it mints the ids the app owns and validates every
// id the app accepts, one rule read in two directions (NewMessageID and ValidMessageID), so a
// minted id cannot become one its own boundary rejects.
// Three validators are filesystem gates: a chat id names the chat's own file, and a session id is
// joined into a path under $KIRO_HOME/sessions/. Nothing returns an error: crypto/rand's Read never
// fails since Go 1.24.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"uuid"
)

// Encoding selects the base32 variant used for the output string.
type Encoding int

const (
	// HexUpper uses RFC 4648 base32hex (0-9 A-V), uppercase, no padding.
	HexUpper Encoding = iota
	// StdLower uses RFC 4648 base32 standard (a-z 2-7), lowercase, no padding.
	StdLower
)

// New returns a random identifier; byteLen controls entropy (output is ceil(byteLen*8/5)
// characters). It panics on an unknown Encoding: the argument is a compile-time constant, so that
// is the program contradicting itself.
func New(byteLen int, enc Encoding) string {
	b := make([]byte, byteLen)
	rand.Read(b)
	switch enc {
	case HexUpper:
		return base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	case StdLower:
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		return strings.ToLower(s)
	default:
		panic(fmt.Sprintf("ids: unknown encoding %d", enc))
	}
}

// NewMessageID returns a UUIDv7 (RFC 9562): time-ordered and sortable, sub-millisecond calls
// included, since the stdlib packs a 12-bit millisecond fraction beside the timestamp. The mint
// half of ValidMessageID's rule.
func NewMessageID() string {
	return uuid.NewV7().String()
}
