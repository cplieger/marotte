package translate

import "github.com/cplieger/runesafe/v2"

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
