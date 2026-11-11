package chat

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// quoteBlock is the quote block the client's quoteSelection writes for passage, unescaped context tokens aside.
func quoteBlock(passage string) string {
	body := strings.Split(passage, "\n")
	for i, l := range body {
		if strings.TrimLeft(l, `\`) == quoteAttribution {
			body[i] = `\` + l
		}
	}
	lines := strings.Split("From Kiro's reply:\n\n"+strings.Join(body, "\n")+"\n\n"+quoteAttribution, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// A turn's first line comes from prompt text, arbitrary and untrusted, through the quote cut and a rune cap: it stays
// one bounded, collapsed line, and a prompt opening on a quote previews the reader's words after it, whatever the
// passage holds.
func FuzzFirstLineBoundedAndWellFormed(f *testing.F) {
	f.Add("")
	f.Add("plain request")
	f.Add("  leading and trailing  ")
	f.Add("tabs\tand\nnewlines\r\nmixed")
	f.Add(strings.Repeat("\u00e9", 500))
	f.Add(strings.Repeat("\U0001f600", 200))
	f.Add("\u0085\ufeff\u3000")
	f.Add("Quoting your earlier reply.")
	f.Add("a\nQuoting your earlier reply.\nFrom the docs:")
	f.Add("intro\n\nQuoting your earlier reply.\nFrom the docs:\n\nline b")
	f.Add("\\Quoting your earlier reply.")
	f.Add("> typed\n\n> From x:\n>\n> Quoting your earlier reply.")
	f.Add("#\\[[file:a]]\n\nFrom the docs:\n")
	f.Fuzz(func(t *testing.T, in string) {
		for _, prompt := range []string{in, quoteBlock(in) + "\n\nwhy?", in + "\n" + quoteBlock(in)} {
			got := firstLineOf(prompt)
			if utf8.ValidString(prompt) && !utf8.ValidString(got) {
				t.Fatalf("firstLineOf(%q) = %q, not valid UTF-8", prompt, got)
			}
			if n := utf8.RuneCountInString(got); n > firstLineMax+1 {
				t.Errorf("firstLineOf(%q) has %d runes, cap is %d plus the ellipsis", prompt, n, firstLineMax)
			}
			if strings.ContainsFunc(got, func(r rune) bool { return unicode.IsSpace(r) && r != ' ' }) ||
				strings.Contains(got, "  ") || strings.TrimSpace(got) != got {
				t.Errorf("firstLineOf(%q) = %q, not one collapsed line", prompt, got)
			}
		}
		if strings.TrimFunc(in, jsSpace) == "" {
			return
		}
		if got := firstLineOf(quoteBlock(in) + "\n\nwhy?"); got != "why?" {
			t.Errorf("a prompt opening on a quote of %q previews %q, want the words after it", in, got)
		}
	})
}
