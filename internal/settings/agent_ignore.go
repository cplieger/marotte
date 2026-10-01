package settings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// AgentIgnoreFloor is sent to KAS whatever the user's list holds, so a
// `.kiroignore` at the workspace root is always enforced. It is not a removable
// entry: the panel renders it as a fixed row above the user's chips, and
// AgentIgnoreList puts it first in the list that goes on the wire.
const AgentIgnoreFloor = ".kiroignore"

// ErrAgentIgnoreEntry is the class of every refusal below. Each refusal wraps it
// with the reason a user reads, because the reasons are KAS's own and a caller
// that only reports the class cannot say which rule fired.
var ErrAgentIgnoreEntry = errors.New("invalid agent ignore file entry")

// jsTrimmed trims the runes ECMAScript's String.prototype.trim removes, which is
// the set KAS's own entry validator compares against. It is deliberately NOT
// strings.TrimSpace: Go's unicode.IsSpace and ECMAScript's WhiteSpace plus
// LineTerminator differ on two runes, and one differs in the ACCEPTING
// direction — U+FEFF is trimmed by JavaScript and not by Go, so TrimSpace lets
// through an entry KAS then skips. U+0085 is the mirror image and is omitted
// here for the same reason: Go trims it, JavaScript does not.
func jsTrimmed(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0xFEFF, 0x2028, 0x2029:
			return true
		}
		return unicode.Is(unicode.Zs, r)
	})
}

// ValidAgentIgnoreEntry mirrors KAS's `Bvt`, rule for rule: non-empty, already
// trimmed, not ".", no path separator, no "..", no glob metacharacter. Arm order
// cannot matter — empty and untrimmed are disjoint, and no other pair overlaps on
// a verdict. An entry KAS refuses is one it SKIPS with a load error, so accepting
// one persists a name the agent never enforces while the panel claims it does.
// The basename rule is KAS's mechanism: the list names ignore FILES at the
// workspace root, so a pattern belongs inside one of them.
func ValidAgentIgnoreEntry(entry string) error {
	switch {
	case entry == "":
		return fmt.Errorf("%w: an entry cannot be empty", ErrAgentIgnoreEntry)
	case entry != jsTrimmed(entry):
		return fmt.Errorf("%w %q: leading or trailing whitespace", ErrAgentIgnoreEntry, entry)
	case entry == ".":
		return fmt.Errorf("%w: %q is not a filename", ErrAgentIgnoreEntry, entry)
	case strings.ContainsAny(entry, `/\`):
		return fmt.Errorf("%w %q: name a file at the workspace root, not a path", ErrAgentIgnoreEntry, entry)
	case strings.Contains(entry, ".."):
		return fmt.Errorf("%w %q: name a file at the workspace root, not a path", ErrAgentIgnoreEntry, entry)
	case strings.ContainsAny(entry, "*?[]{}"):
		return fmt.Errorf("%w %q: put a glob pattern inside an ignore file, not in this list", ErrAgentIgnoreEntry, entry)
	}
	return nil
}

// AgentIgnoreList is the list marotte sends KAS: AgentIgnoreFloor first, then the
// user's entries in their own order, deduped, with anything ValidAgentIgnoreEntry
// refuses dropped.
//
// It filters as well as dedupes so that what marotte sends equals what KAS
// enforces: KAS skips an invalid entry itself, and config.json is a file the
// operator edits by hand, so a name that never reached the PATCH validation can
// still be in the document.
func AgentIgnoreList(entries []string) []string {
	out := make([]string, 0, len(entries)+1)
	out = append(out, AgentIgnoreFloor)
	for _, entry := range entries {
		if ValidAgentIgnoreEntry(entry) != nil || slices.Contains(out, entry) {
			continue
		}
		out = append(out, entry)
	}
	return out
}
