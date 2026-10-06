package command

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultTerminalLines = 200
	maxTerminalLines     = 5000
	// terminalHeader is the Kiro IDE's own header for its terminal provider.
	terminalHeader   = "Current terminal contents:\n"
	terminalAltNote  = "[A full-screen program is open in the shell; this is the shell output beneath it.]\n"
	terminalOmitted  = "[earlier output omitted]\n"
	terminalExitNote = "\n[The shell has exited.]"
)

var (
	errTerminalLines    = errors.New("the line count must be a whole number of at least 1")
	errShellUnavailable = errors.New("the shell is not available")
	errShellNotStarted  = errors.New("the shell has not started yet")
	errTerminalEmpty    = errors.New("the terminal is empty")
)

// mentionTerminal answers a terminal reference: the built-in shell's newest lines,
// verbatim. The query is the line count, default defaultTerminalLines. Output
// over MaxMentionChars is cut from the OLDEST end, because the newest output is
// what the reader is pointing at; renderMention would keep the head instead, so
// the whole item is sized here and handed over fixed.
func mentionTerminal(query string, sh ShellReader) ([]mentionPart, error) {
	n, err := terminalLines(query)
	if err != nil {
		return nil, err
	}
	if sh == nil {
		return nil, errShellUnavailable
	}
	out, ok := sh.ReadShell(n)
	if !ok {
		return nil, errShellNotStarted
	}
	if out.Text == "" {
		return nil, errTerminalEmpty
	}
	head := terminalHeader
	if out.AltScreen {
		head += terminalAltNote
	}
	tail := ""
	if out.Exited {
		tail = terminalExitNote
	}
	body := out.Text
	room := MaxMentionChars - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if utf8.RuneCountInString(body) > room {
		body = keepNewest(body, room-utf8.RuneCountInString(terminalOmitted))
		head += terminalOmitted
	}
	return []mentionPart{fixedText(head + body + tail)}, nil
}

// terminalLines parses a terminal query: empty is the default, a count above
// maxTerminalLines is lowered to it.
func terminalLines(query string) (int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return defaultTerminalLines, nil
	}
	n, err := strconv.Atoi(query)
	if err != nil || n < 1 {
		return 0, errTerminalLines
	}
	return min(n, maxTerminalLines), nil
}

// keepNewest returns at most n runes from the end of s, starting at a line
// boundary unless the newest line alone is longer than n.
func keepNewest(s string, n int) string {
	if n <= 0 {
		return ""
	}
	start := len(s)
	for range n {
		if start == 0 {
			return s
		}
		_, size := utf8.DecodeLastRuneInString(s[:start])
		start -= size
	}
	tail := s[start:]
	if start > 0 && s[start-1] != '\n' {
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		}
	}
	return tail
}
