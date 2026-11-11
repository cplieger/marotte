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

// quoteAttributionLine ends every quote block; static-src/message-quote.ts QUOTE_ATTRIBUTION owns the sentence.
const quoteAttributionLine = "> Quoting your earlier reply."

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
	inner := block[min(from, len(block)-1) : len(block)-1]
	lines := make([]string, len(inner))
	for i, l := range inner {
		lines[i] = unquote(l)
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
	start, prevFrom := 0, -1
	for _, end := range quoteEnds(run) {
		if read, ok := readBlock(run[start : end+1]); ok {
			prevFrom = start
			pieces = append(pieces, read...)
			start = end + 1
		} else if prevFrom >= 0 {
			read, _ = readBlock(run[prevFrom : end+1])
			pieces = append(pieces[:len(pieces)-2], read...)
			start = end + 1
		}
	}
	return append(pieces, runPiece{typed: run[start:]})
}

// quoteEnds is the indexes of the attribution lines in run that may close a quote, in order.
func quoteEnds(run []string) []int {
	var ends []int
	for i, line := range run {
		if line == quoteAttributionLine && (i == len(run)-1 || opensNamed(run, i+1)) {
			ends = append(ends, i)
		}
	}
	for i, line := range slices.Backward(run) {
		if line == quoteAttributionLine {
			if len(ends) == 0 || ends[len(ends)-1] != i {
				ends = append(ends, i)
			}
			break
		}
	}
	return ends
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
