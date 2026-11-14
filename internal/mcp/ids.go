package mcp

import (
	"errors"
	"fmt"

	"github.com/cplieger/marotte/internal/ids"
)

// serverID is a validated identifier for an MCP server record. Values
// are generated at Create time via newID() and are base32-encoded random
// values. The type prevents accidental Name-as-ID confusion at compile
// time, mirroring marotte.ChatID and marotte.SessionID.
type serverID string

// idMaxLen is the byte bound on a server id. Generated ids are 10 chars; the
// headroom is for an id minted by an older or a future encoding.
const idMaxLen = 32

// parseServerID validates a raw string as a server ID: the shared charset (mcp.nameAllowedRune),
// but bounded at IDMaxLen (32) rather than nameMaxLen, and with no leading-letter rule since
// newID() can open with a digit. TestNameDoorsAgree pins both.
func parseServerID(raw string) (serverID, error) {
	if raw == "" {
		return "", errors.New("empty server id")
	}
	if len(raw) > idMaxLen {
		return "", fmt.Errorf("server id too long: %d chars (max %d)", len(raw), idMaxLen)
	}
	for _, r := range raw {
		if !nameAllowedRune(r) {
			return "", fmt.Errorf("server id has an illegal character: %q", raw)
		}
	}
	return serverID(raw), nil
}

// String implements fmt.Stringer.
func (id serverID) String() string { return string(id) }

// newID returns a short random identifier for a server record. 10 chars
// of base32 lowercase is ~48 bits of entropy (6 bytes of randomness;
// ample for a single user's configured set).
func newID() serverID {
	return serverID(ids.New(6, ids.StdLower))
}
