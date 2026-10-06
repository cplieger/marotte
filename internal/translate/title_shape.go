package translate

// The door treatment for a KAS string that wants to become a chat name, shared by the focus
// channel and the session/load title (agent.adoptKASTitle). A chat name only moves UP the
// precedence, so the first string through is LATCHED until an agent declares a title; the
// text is an unbounded model reply. Both doors gate, because a poisoned title KAS stored
// is re-offered on every resume. Nothing marotte sends can stop the producer.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/sanitize"
)

// KASDefaultSessionTitle is KAS's placeholder (DEFAULT_SESSION_TITLE). Adopting it makes the
// chat non-default-named, which locks out the real title arriving later.
const KASDefaultSessionTitle = "New Session"

// derivedTitleEllipsis is the suffix KAS's Ete appends when it caps at 80 runes:
// `t.length<=80 ? t : t.slice(0,77)+"..."`.
const derivedTitleEllipsis = "..."

// maxTitleRunes caps an adopted title; a guard against a malformed frame (KAS caps at 80).
const maxTitleRunes = 80

// maxTitleWords bounds an adopted title. An agent's update_session_information title is
// bound by no prompt and live ones run to 7, so 8 is too tight.
const maxTitleWords = 12

// Refusal reasons, so a door's log line names WHICH rule fired.
const (
	refusalKASPlaceholder = "kas placeholder"
	refusalTooLong        = "over the rune cap"
	refusalTruncated      = "truncated"
	refusalMultiSentence  = "multi-sentence"
	refusalTooManyWords   = "too many words"
)

// SanitizeTitle prepares one upstream title for a chat name: ANSI and hidden runes
// out, then the single-line display policy, then trimmed. Empty means there is
// nothing to adopt.
func SanitizeTitle(raw string) string {
	return strings.TrimSpace(displayText(sanitize.Output(raw)))
}

// TitleRefusal names the shape rule a sanitized title breaks, or "" when it breaks none.
// No rule asserts content. Residuals: a one-clause refusal under the rune cap reads as a
// title, and an unspaced script (CJK) is one word to the word cap.
func TitleRefusal(title string) string {
	switch {
	// Well-SHAPED; what disqualifies it is whose placeholder it is.
	case title == KASDefaultSessionTitle:
		return refusalKASPlaceholder
	case utf8.RuneCountInString(title) > maxTitleRunes:
		return refusalTooLong
	// Only Ete produces this suffix and an agent's own title is never Ete-capped, so a
	// truncated title is a derivation or an over-long model reply.
	case strings.HasSuffix(title, derivedTitleEllipsis):
		return refusalTruncated
	case hasInternalSentenceBreak(title):
		return refusalMultiSentence
	case len(strings.Fields(title)) > maxTitleWords:
		return refusalTooManyWords
	}
	return ""
}

// sentenceTerminators can close a sentence mid-string: the Latin three plus their
// fullwidth and ideographic forms, so the rule is not Latin-only.
const sentenceTerminators = ".?!。？！"

// hasInternalSentenceBreak reports whether title carries a terminator, then whitespace,
// then a letter: a title's trailing punctuation is stripped upstream and a mid-word period
// (Node.js) has no whitespace after it. Refusing wrongly still lets an agent title win,
// while adopting wrongly latches the refusal as the name, so this door errs to refuse. A
// producer-side rule inverts that asymmetry; do not align the two.
func hasInternalSentenceBreak(title string) bool {
	runes := []rune(title)
	// len-2 because the triplet needs a rune after the whitespace.
	for i := range len(runes) - 2 {
		if !strings.ContainsRune(sentenceTerminators, runes[i]) {
			continue
		}
		if letterFollowsSpace(runes[i+1:]) {
			return true
		}
	}
	return false
}

// letterFollowsSpace reports whether rest opens with whitespace and then a letter.
func letterFollowsSpace(rest []rune) bool {
	if len(rest) == 0 || !unicode.IsSpace(rest[0]) {
		return false
	}
	for _, r := range rest {
		if unicode.IsSpace(r) {
			continue
		}
		return unicode.IsLetter(r)
	}
	return false
}
