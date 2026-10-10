// Package sanitize defuses text marotte did not write before it is persisted, echoed to a
// client, or written to a log: ANSI escape sequences and the invisible Unicode codepoints
// (TAG characters, zero-width joiners, bidi overrides) that carry prompt injections.
//
// Output is for MULTI-LINE transcript content (tool output, a shell capture, an export):
// hidden runes are deleted. A single-line surface takes runesafe's
// SanitizeSingleLineBounded instead, which replaces them with a visible space.
// internal/ansitext parses the same sequences to KEEP them as style spans.
package sanitize

import (
	"regexp"
	"strings"

	"github.com/cplieger/runesafe/v2"
)

// ansiRe matches the ANSI sequences kiro-cli and its subprocesses produce: CSI, OSC,
// charset-select and single-character controls. 8-bit C1 controls (0x9b..0x9f) are invalid
// UTF-8 to Go's regexp and are not matched; kiro-cli uses the 7-bit ESC forms.
var ansiRe = regexp.MustCompile(
	`\x1b\[[0-9;?]*[a-zA-Z]` + // CSI
		`|\x1b\][\s\S]*?(?:\x07|\x1b\\)` + // OSC
		`|\x1b[()][A-Za-z0-9]` + // charset select
		`|\x1b[NOPX^_78=>c]`, // SS2/SS3/DCS/SOS/PM/APC/save/restore/RIS/keypad
)

// StripANSI removes ANSI escape sequences from a string.
func StripANSI(s string) string {
	for {
		out := ansiRe.ReplaceAllString(s, "")
		if out == s {
			return out
		}
		s = out
	}
}

// Unicode strips hidden codepoints used for prompt injection via tool output (TAG
// characters, zero-width spaces/joiners, format controls), as Q Developer CLI's
// ExecuteCmd does.
func Unicode(s string) string {
	return strings.Map(func(r rune) rune {
		if isHidden(r) {
			return -1
		}
		return r
	}, s)
}

// The bidi set is runesafe.IsBidiControl (the exact unicode.Bidi_Control set).
func isHidden(r rune) bool {
	if r >= 0xE0000 && r <= 0xE007F {
		return true // TAG characters
	}
	if runesafe.IsBidiControl(r) {
		return true // bidi marks/embeddings/overrides/isolates (Trojan Source)
	}
	switch r {
	case 0x00AD, // soft hyphen
		0x200B, 0x200C, 0x200D, // zero-width space/non-joiner/joiner
		0xFEFF,                                 // BOM / zero-width no-break space
		0x2060, 0x2061, 0x2062, 0x2063, 0x2064: // word joiner + invisible math
		return true
	}
	return false
}

// Output applies ANSI stripping and Unicode sanitization to a fixed point (removing a hidden
// rune can complete an escape sequence), then replaces any unmatched U+001B with U+FFFD,
// matching internal/ansitext's escape-free contract. Idempotent; the result is valid UTF-8.
// It terminates: after the first pass every pass only removes runes.
func Output(s string) string {
	for {
		out := Unicode(StripANSI(s))
		if out == s {
			return strings.ReplaceAll(out, "\x1b", "\uFFFD")
		}
		s = out
	}
}
