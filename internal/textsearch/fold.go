package textsearch

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Fold lowercases for a case-insensitive scan with Unicode's simple, per-rune
// mapping (no Final_Sigma, no locale). Rune-count-preserving by construction:
// strings.Map with a rune-to-rune function, an invalid byte becoming one
// U+FFFD. So a rune index into Fold(s) is a rune index into s. Byte offsets
// are NOT preserved; Occurrences reports the original's.
func Fold(s string) string {
	return strings.Map(unicode.ToLower, s)
}

// FoldText is Fold plus accent folding: a lowered rune whose canonical decomposition is one base and nonspacing
// marks becomes that base (`é` to `e`), through the generated table (internal/foldgen). Runes with no such
// decomposition (`ø`, `ß`) stay. Rune-count-preserving like Fold, and length-preserving in UTF-16.
func FoldText(s string) string {
	return strings.Map(foldTextRune, s)
}

func foldTextRune(r rune) rune {
	r = unicode.ToLower(r)
	if r < utf8.RuneSelf {
		return r
	}
	if i, ok := slices.BinarySearch(accentFrom[:], r); ok {
		return accentTo[i]
	}
	return r
}
