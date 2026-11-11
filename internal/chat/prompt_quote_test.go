package chat

import (
	"slices"
	"testing"
)

// A header's search segments are one per band and per stretch of words between, the find runs
// static-src/fundamentals/turn-header.test.ts reads off the same prompt.
func TestDrawnWords_OneRunPerBandAndStretch(t *testing.T) {
	const (
		first  = "Activity notifications: a separate stream, after the platform is built."
		second = "Caddy healthcheck order: waits for you to schedule it"
	)
	prompt := "> From Kiro's reply:\n>\n> " + first + "\n>\n> Quoting your earlier reply.\n\n" +
		"remind me what this is about?\n\n" +
		"> From Kiro's reply:\n>\n> " + second + "\n>\n> Quoting your earlier reply.\n\n" +
		"and is it urgent?"
	want := []string{
		"From Kiro's reply: " + first,
		"remind me what this is about?",
		"From Kiro's reply: " + second,
		"and is it urgent?",
	}
	if got := drawnWords(prompt); !slices.Equal(got, want) {
		t.Errorf("drawnWords = %q, want %q", got, want)
	}
}

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
