package chat

// The quote blocks the client's Quote in reply writes into a prompt, cut as static-src/message-quote.ts splitQuotes
// cuts them, so a turn's preview skips them as its header does. A port: testdata/first_line.json holds both to one
// reading through firstLineOf.

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// quoteAttribution ends every quote block; static-src/message-quote.ts QUOTE_ATTRIBUTION owns the sentence.
const (
	quoteAttribution     = "Quoting your earlier reply."
	quoteAttributionLine = "> " + quoteAttribution
)

// promptPart is one stretch of a prompt: a quote block, or the trimmed text around one.
type promptPart struct {
	source *string // a quote's named source; nil for text and for a quote that names none
	text   string  // the quoted passage, or the text
	quote  bool
}

var escapedContext = regexp.MustCompile(`#\\(\\*)\[\[`)

func unescapeContext(s string) string {
	return escapedContext.ReplaceAllString(s, "#${1}[[")
}

// unescapeAttribution is a passage line without the one backslash the client's escapeAttribution gives a line
// reading as the attribution.
func unescapeAttribution(line string) string {
	if strings.HasPrefix(line, `\`) && strings.TrimLeft(line, `\`) == quoteAttribution {
		return line[1:]
	}
	return line
}

// jsSpace is what the client's String.prototype.trim strips: ECMA-262 WhiteSpace and LineTerminator. Not
// unicode.IsSpace, which takes U+0085 and leaves U+FEFF.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', '\n', '\r', '\ufeff', '\u2028', '\u2029':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// unquote is a `>` line without its marker.
func unquote(line string) string {
	if rest, ok := strings.CutPrefix(line, "> "); ok {
		return rest
	}
	return line[1:]
}

// opensNamed reports whether run[i] opens a named quote: a `From …:` line, then a bare `>`.
func opensNamed(run []string, i int) bool {
	if i+1 >= len(run) {
		return false
	}
	head := unquote(run[i])
	return strings.HasPrefix(head, "From ") && strings.HasSuffix(head, ":") && unquote(run[i+1]) == ""
}

// readQuote is the quote block holds, its lines ending on the attribution line, or false when nothing is quoted.
func readQuote(block []string) (promptPart, bool) {
	named := opensNamed(block, 0)
	from := 0
	if named {
		from = 2
	}
	inner := block[from : len(block)-1]
	lines := make([]string, len(inner))
	for i, l := range inner {
		lines[i] = unescapeAttribution(unquote(l))
	}
	quoted := unescapeContext(strings.TrimRightFunc(strings.Join(lines, "\n"), jsSpace))
	if quoted == "" {
		return promptPart{}, false
	}
	p := promptPart{quote: true, text: quoted}
	if named {
		source := unescapeContext(strings.TrimSuffix(unquote(block[0]), ":"))
		p.source = &source
	}
	return p, true
}

// readRun is a run of `>` lines as its quotes and the typed lines around them, in order, cut by the rule
// message-quote.ts readRun states.
func readRun(run []string) (pieces []runPiece) {
	start := 0
	for i, line := range run {
		if line != quoteAttributionLine {
			continue
		}
		if read, ok := readBlock(run[start : i+1]); ok {
			pieces = append(pieces, read...)
		} else {
			pieces = append(pieces, runPiece{typed: run[start : i+1]})
		}
		start = i + 1
	}
	return append(pieces, runPiece{typed: run[start:]})
}

// readBlock is the typed lines above block's first named opener and the quote from there, or false when it quotes
// nothing.
func readBlock(block []string) ([]runPiece, bool) {
	at := 0
	for i := range block {
		if opensNamed(block, i) {
			at = i
			break
		}
	}
	p, ok := readQuote(block[at:])
	if !ok {
		return nil, false
	}
	return []runPiece{{typed: block[:at]}, {quote: &p}}, true
}

// runPiece is one piece of a `>` run: a quote, or the typed lines no quote takes.
type runPiece struct {
	quote *promptPart
	typed []string
}

// drawnWords is prompt as a message draws it, one string per find run: each quote block its band's words, each
// stretch around one its trimmed text, in order. A prompt holding no quote is itself.
func drawnWords(prompt string) []string {
	parts := splitQuotes(prompt)
	if !slices.ContainsFunc(parts, func(p promptPart) bool { return p.quote }) {
		return []string{prompt}
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.text
		if p.source != nil {
			out[i] = unwrapCodeSpans(*p.source) + ": " + p.text
		}
	}
	return out
}

// unwrapCodeSpans is a quote's source as static-src/quote-band.ts sourceEl draws it, each code span as its code. A
// span is a backtick run, then text neither starting nor ending on a backtick nor holding a line terminator, then the
// same run with no backtick after it, the shortest such text; code padded by a space at each end, and not all space,
// loses both (CommonMark 6.1).
func unwrapCodeSpans(s string) string {
	var b strings.Builder
	at := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			continue
		}
		from := i
		for from < len(s) && s[from] == '`' {
			from++
		}
		end, ok := codeSpanEnd(s, from, s[i:from])
		if !ok {
			continue
		}
		code := s[from:end]
		if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimFunc(code, jsSpace) != "" {
			code = code[1 : len(code)-1]
		}
		b.WriteString(s[at:i])
		b.WriteString(code)
		at = end + from - i
		i = at - 1
	}
	b.WriteString(s[at:])
	return b.String()
}

// codeSpanEnd is where the code starting at s[from] ends before a closing fence, or false.
func codeSpanEnd(s string, from int, fence string) (int, bool) {
	for end := from + 1; end <= len(s); end++ {
		last := s[end-1:]
		if last[0] == '\n' || last[0] == '\r' || strings.HasPrefix(last, "\u2028") || strings.HasPrefix(last, "\u2029") {
			return 0, false
		}
		rest := s[end:]
		if last[0] != '`' && strings.HasPrefix(rest, fence) && (len(rest) == len(fence) || rest[len(fence)] != '`') {
			return end, true
		}
	}
	return 0, false
}

// splitQuotes is prompt cut into its quote blocks, wherever each sits, and the trimmed text between them, blank
// text left out.
func splitQuotes(prompt string) []promptPart {
	lines := strings.Split(prompt, "\n")
	var parts []promptPart
	var text []string
	flush := func() {
		if words := strings.TrimFunc(strings.Join(text, "\n"), jsSpace); words != "" {
			parts = append(parts, promptPart{text: words})
		}
		text = nil
	}
	for i := 0; i < len(lines); {
		end := i
		for end < len(lines) && strings.HasPrefix(lines[end], ">") {
			end++
		}
		if end == i {
			text = append(text, lines[i])
			i++
			continue
		}
		for _, piece := range readRun(lines[i:end]) {
			if piece.quote == nil {
				text = append(text, piece.typed...)
				continue
			}
			flush()
			parts = append(parts, *piece.quote)
		}
		i = end
	}
	flush()
	return parts
}
