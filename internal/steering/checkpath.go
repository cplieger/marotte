package steering

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/sanitize"
)

// CheckPath reports why dir cannot be written verbatim into a generated document, or nil. The
// generator writes every directory New takes inside a Markdown code span, unchanged, so each must
// pass: a line break could forge a line kiro-cli treats as authoritative, a backtick close the
// span, and a hidden rune say something the reader cannot see.
func CheckPath(dir string) error {
	if !utf8.ValidString(dir) {
		return errors.New("is not valid UTF-8")
	}
	for _, r := range dir {
		if r == '`' || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) ||
			sanitize.Unicode(string(r)) == "" {
			return fmt.Errorf("holds %U, which the generated steering documents cannot carry", r)
		}
	}
	return nil
}
