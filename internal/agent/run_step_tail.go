package agent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// tailMaxRunes is longer than any dock row shows; the client's CSS ellipsizes the rest.
const tailMaxRunes = 200

// tailStructureOnly is markdown that says nothing on its own line: a code fence, a table's rule, a thematic break
// (one alternative per mark, since RE2 has no backreference).
var tailStructureOnly = regexp.MustCompile("^(?:(?:`{3,}|~{3,})[^`]*|\\|?(?:\\s*:?-+:?\\s*\\|)*\\s*:?-+:?\\s*\\|?|-(?:\\s*-){2,}|\\*(?:\\s*\\*){2,}|_(?:\\s*_){2,})$")

// tailBlockMarkers are leading block markers: heading hashes, quote marks, list bullets and numbers.
var tailBlockMarkers = regexp.MustCompile(`^(?:#{1,6}\s+|>\s?|[-*+]\s+|\d{1,9}[.)]\s+)+`)

// printedLine is the line a step's dock row shows for one sealed entry of its own lane, "" when the entry prints
// nothing there. The client's twin, which also reads a streaming entry, is run-step-tail.ts lastPrintedLine: both
// must cut a line the same way.
func printedLine(e *marotte.Entry) string {
	if e.Lane != "" {
		return ""
	}
	switch e.Kind {
	case marotte.EntryKindText:
		var t marotte.EntryText
		if json.Unmarshal(e.Payload, &t) != nil {
			return ""
		}
		return lastProseLine(t.Text)
	case marotte.EntryKindThinking:
		var t marotte.EntryThinking
		if json.Unmarshal(e.Payload, &t) != nil {
			return ""
		}
		return lastProseLine(t.Text)
	case marotte.EntryKindToolCall:
		var c marotte.EntryToolCall
		if json.Unmarshal(e.Payload, &c) != nil {
			return ""
		}
		return lastProseLine(c.Title)
	default:
		return ""
	}
}

// dockText is the end of a streaming text that lastProseLine reads the same as the whole, whatever is appended to it:
// its unfinished last line and every line back to the newest finished line that prints.
func dockText(text string) string {
	end := strings.LastIndexByte(text, '\n')
	for end > 0 {
		start := strings.LastIndexByte(text[:end], '\n') + 1
		if lastProseLine(text[start:end]) != "" {
			return text[start:]
		}
		end = start - 1
	}
	return text
}

// lastProseLine is the last line of text that reads as prose, its markdown block markers dropped and cut to
// tailMaxRunes.
func lastProseLine(text string) string {
	for _, l := range slices.Backward(strings.Split(text, "\n")) {
		raw := strings.TrimSpace(l)
		if raw == "" || tailStructureOnly.MatchString(raw) {
			continue
		}
		line := strings.TrimSpace(tailBlockMarkers.ReplaceAllString(raw, ""))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > tailMaxRunes {
			return string(r[:tailMaxRunes])
		}
		return line
	}
	return ""
}
