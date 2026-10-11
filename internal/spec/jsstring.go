package spec

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// isJSSpace is JavaScript's \s and String.prototype.trim set. Go's regexp \s stays ASCII in the
// line regex itself, the one stated divergence.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func isBlank(line string) bool {
	return strings.TrimFunc(line, isJSSpace) == ""
}

func leadingSpace(line string) int {
	n := 0
	for _, r := range line {
		if !isJSSpace(r) {
			break
		}
		n++
	}
	return n
}

func dropLeading(line string, n int) string {
	off := 0
	for range n {
		_, size := utf8.DecodeRuneInString(line[off:])
		off += size
	}
	return line[off:]
}

// sortNumbers is the Llc comparator: dot depth first, then
// localeCompare(numeric) segment by segment, stable like Array.prototype.sort.
func sortNumbers(ts []*task) {
	slices.SortStableFunc(ts, func(a, b *task) int {
		if d := depth(a.number) - depth(b.number); d != 0 {
			return d
		}
		return compareDotted(a.number, b.number)
	})
}

func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		if c := compareDigits(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}

// compareDigits orders two digit runs by value whatever their length, with
// leading zeros ignored as numeric collation ignores them.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
