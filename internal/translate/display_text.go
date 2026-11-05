package translate

import (
	"strings"

	"github.com/cplieger/runesafe/v3"
)

// maxDisplayTextBytes bounds one upstream string on its way to a banner, card or tooltip:
// nothing on the wire bounds them. Above the widest legitimate value measured (303 bytes),
// below the point where one card pushes the dock off screen.
const maxDisplayTextBytes = 512

// displayText prepares one upstream string for a single-line human-read surface: runesafe's
// single-line preset, capped on a rune boundary. The preset REPLACES controls, Bidi_Control
// runes and separators with a space, so a deception becomes visible rather than vanishing;
// sanitize.Output (which deletes) is wrong here. The one cost: mixed-script labels relying on
// explicit direction marks lose them. Sibling: auth.identityText.
func displayText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxDisplayTextBytes)
}

// displayLines is displayText's multi-line twin for a decision surface that shows a command:
// LF survives because a shell runs each line as its own command, so folding it would show
// two commands as one. CR does not (the shell reads it as an ordinary character), so CRLF
// becomes LF and every other unsafe rune a space. Uncapped: the caller bounds it.
func displayLines(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && runesafe.IsUnsafeSingleLine(r) {
			return ' '
		}
		return r
	}, strings.ReplaceAll(s, "\r\n", "\n"))
}
