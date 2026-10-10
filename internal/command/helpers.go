package command

import "unicode/utf8"

// truncateRunes truncates s to at most n runes, returning a valid
// byte-prefix of s (preserving original bytes, not re-encoding).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	pos := 0
	for i := 0; i < n && pos < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[pos:])
		pos += size
	}
	return s[:pos]
}
