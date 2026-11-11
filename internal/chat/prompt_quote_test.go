package chat

import "testing"

// A quote's source draws each CommonMark 6.1 code span as its code, as static-src/quote-band.ts sourceEl does.
func TestUnwrapCodeSpans(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no span", "From Kiro's reply", "From Kiro's reply"},
		{"one span", "From the output of `go test ./...`", "From the output of go test ./..."},
		{"two spans", "`a` and `b`", "a and b"},
		{"a longer fence holds a backtick", "From ``a`b``", "From a`b"},
		{"one space each side is dropped", "`` `x` ``", "`x`"},
		{"code of spaces alone keeps them", "`  `", "  "},
		{"an unclosed fence is text", "a `b", "a `b"},
		{"a closer of another length is text", "`a``", "`a``"},
		{"a span never crosses a line terminator", "`a\u2028b`", "`a\u2028b`"},
		{"a fence may open inside a longer run", "```a`` b`", "`a b`"},
		{"the shortest code closes", "x``y`z``", "xy`z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unwrapCodeSpans(tt.in); got != tt.want {
				t.Errorf("unwrapCodeSpans(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
