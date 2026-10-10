package settings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// AgentIgnoreFloor is sent to KAS whatever the user's list holds, so a root `.kiroignore` is
// always enforced; AgentIgnoreList puts it first.
const AgentIgnoreFloor = ".kiroignore"

// errAgentIgnoreEntry is the class of every refusal below. Each refusal wraps it
// with the reason a user reads, because the reasons are KAS's own and a caller
// that only reports the class cannot say which rule fired.
var errAgentIgnoreEntry = errors.New("invalid agent ignore file entry")

// jsTrimmed trims the runes ECMAScript's String.prototype.trim removes, the set KAS's entry
// validator compares against. NOT strings.TrimSpace: U+FEFF is trimmed by JavaScript and not
// by Go (TrimSpace would admit an entry KAS skips); U+0085 is the reverse and is omitted.
func jsTrimmed(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0xFEFF, 0x2028, 0x2029:
			return true
		}
		return unicode.Is(unicode.Zs, r)
	})
}

// ValidAgentIgnoreEntry mirrors KAS's `Bvt` rule for rule: non-empty, already trimmed, not
// ".", no path separator, no "..", no glob metacharacter. An entry KAS refuses is one it
// SKIPS, so accepting it would show an unenforced name as enforced.
func ValidAgentIgnoreEntry(entry string) error {
	switch {
	case entry == "":
		return fmt.Errorf("%w: an entry cannot be empty", errAgentIgnoreEntry)
	case entry != jsTrimmed(entry):
		return fmt.Errorf("%w %q: leading or trailing whitespace", errAgentIgnoreEntry, entry)
	case entry == ".":
		return fmt.Errorf("%w: %q is not a filename", errAgentIgnoreEntry, entry)
	case strings.ContainsAny(entry, `/\`):
		return fmt.Errorf("%w %q: name a file at the workspace root, not a path", errAgentIgnoreEntry, entry)
	case strings.Contains(entry, ".."):
		return fmt.Errorf("%w %q: name a file at the workspace root, not a path", errAgentIgnoreEntry, entry)
	case strings.ContainsAny(entry, "*?[]{}"):
		return fmt.Errorf("%w %q: put a glob pattern inside an ignore file, not in this list", errAgentIgnoreEntry, entry)
	}
	return nil
}

// AgentIgnoreList is the list marotte sends KAS: AgentIgnoreFloor first, then the user's
// entries in order, deduped, with refused entries dropped (config.json is hand-editable).
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
