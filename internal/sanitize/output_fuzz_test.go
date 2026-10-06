package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzSanitizeOutput(f *testing.F) {
	f.Add("")
	f.Add("hello")
	f.Add("\x1b[31m\u200Bred\x1b[0m")
	f.Add("a\u200B\x1b(\u200C0b")
	f.Add("\x1b[\u200B31m")
	f.Add("\x1b]\u2060title\x07")
	f.Add("AB\x1b[")
	f.Add("AB\x1b]0;titl")
	f.Add("AB\x1b(")

	f.Fuzz(func(t *testing.T, s string) {
		out := Output(s)
		if ansiRe.MatchString(out) {
			t.Errorf("Output(%q) still contains ANSI", s)
		}
		if strings.ContainsRune(out, '\x1b') {
			t.Errorf("Output(%q) still contains U+001B", s)
		}
		for _, r := range out {
			if isHidden(r) {
				t.Errorf("Output(%q) contains hidden U+%04X", s, r)
			}
		}
		if out2 := Output(out); out2 != out {
			t.Errorf("not idempotent: %q → %q → %q", s, out, out2)
		}
		// Output must be valid UTF-8: strings.Map turns any invalid byte into U+FFFD.
		if !utf8.ValidString(out) {
			t.Errorf("Output(%q) = %q is not valid UTF-8", s, out)
		}
		// The rune count never grows (byte length can: an invalid byte becomes 3-byte U+FFFD).
		if utf8.RuneCountInString(out) > utf8.RuneCountInString(s) {
			t.Errorf("rune count grew: %d > %d (in=%q out=%q)",
				utf8.RuneCountInString(out), utf8.RuneCountInString(s), s, out)
		}
	})
}
