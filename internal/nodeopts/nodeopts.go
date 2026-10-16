// Package nodeopts rewrites a NODE_OPTIONS value so it sets the JavaScript heap limit, lexing it the way Node does.
package nodeopts

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The lexer's errors carry Node's own wording (src/node_options.cc, ParseNodeOptionsEnvVar), so a refusal reads the
// same as Node's would.
var (
	errInvalidEscape      = errors.New("invalid escape")
	errUnterminatedString = errors.New("unterminated string")
	errHeapFlagValue      = errors.New("a heap size flag needs =<digits>")
)

const heapFlag = "--max-old-space-size"

// heapFlagNames are V8's two heap-limit flags. The percentage one beats the size one whatever their order, so it
// must go too, or it silently defeats the limit.
var heapFlagNames = map[string]struct{}{
	heapFlag:                 {},
	heapFlag + "-percentage": {},
}

// token is one lexed NODE_OPTIONS argument: its decoded text and the byte span it came from.
type token struct {
	text       string
	start, end int
}

// WithHeapLimit returns inherited with every heap-limit flag removed and `--max-old-space-size=<mib>` appended. mib 0
// returns inherited unchanged and unparsed. Every other option keeps its exact bytes. A value Node itself would
// refuse is an error, never repaired.
func WithHeapLimit(inherited string, mib int) (string, error) {
	if mib == 0 {
		return inherited, nil
	}
	if mib < 0 {
		return "", fmt.Errorf("heap limit %d MiB is negative", mib)
	}
	tokens, err := lex(inherited)
	if err != nil {
		return "", err
	}
	kept := make([]string, 0, len(tokens)+1)
	for _, tk := range tokens {
		heap, err := isHeapToken(tk.text)
		if err != nil {
			return "", err
		}
		if !heap {
			kept = append(kept, inherited[tk.start:tk.end])
		}
	}
	kept = append(kept, heapFlag+"="+strconv.Itoa(mib))
	return strings.Join(kept, " "), nil
}

// lex splits s as Node does: only an ASCII space outside double quotes separates; a quote toggles quoting and is not
// part of the token; inside quotes a backslash takes the next byte literally.
func lex(s string) ([]token, error) {
	var l lexer
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inString:
			if i+1 == len(s) {
				return nil, errInvalidEscape
			}
			l.begin(i)
			i++
			l.cur.WriteByte(s[i])
		case c == ' ' && !inString:
			l.flush(i)
		case c == '"':
			l.begin(i)
			inString = !inString
		default:
			l.begin(i)
			l.cur.WriteByte(c)
		}
	}
	if inString {
		return nil, errUnterminatedString
	}
	l.flush(len(s))
	return l.tokens, nil
}

// lexer is lex's token under construction: open while one has begun, start its first byte's offset.
type lexer struct {
	tokens []token
	cur    strings.Builder
	start  int
	open   bool
}

func (l *lexer) begin(i int) {
	if !l.open {
		l.open, l.start = true, i
	}
}

func (l *lexer) flush(end int) {
	if l.open {
		l.tokens = append(l.tokens, token{text: l.cur.String(), start: l.start, end: end})
	}
	l.cur.Reset()
	l.open = false
}

// isHeapToken reports whether text names a heap-limit flag, reading every `_` as `-` as V8 does. A heap flag without
// `=<digits>` is an error: Node refuses the separated and the non-numeric forms itself.
func isHeapToken(text string) (bool, error) {
	name, value, hasValue := strings.Cut(text, "=")
	if _, ok := heapFlagNames[strings.ReplaceAll(name, "_", "-")]; !ok {
		return false, nil
	}
	if !hasValue || value == "" || strings.Trim(value, "0123456789") != "" {
		return false, fmt.Errorf("%w: %q", errHeapFlagValue, text)
	}
	return true, nil
}
