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
// nothing there. calls holds the lane's tool_calls a result can still settle: a call appended unsettled joins it,
// its result settles it. The client's twin, which also reads a streaming entry, is run-step-tail.ts lastPrintedLine: both must cut a line the
// same way.
func printedLine(e *marotte.Entry, calls stepCalls) string {
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
		if !c.Status.Terminal() {
			calls[stepCallKey{e.Turn, e.ID}] = &c
		}
		tc := marotte.ToolCallOfEntry(&c)
		title, _ := marotte.ToolCard(&tc)
		return titleLine(title)
	case marotte.EntryKindToolResult:
		return calls.settle(e)
	default:
		return ""
	}
}

// stepCalls is a step's root-lane tool_calls a result can still settle, by turn and call id: each was appended
// unsettled, and no result but an abort has settled it.
type stepCalls map[stepCallKey]*marotte.EntryToolCall

type stepCallKey struct{ turn, id string }

// titles is the held calls' card titles by call id, nil when none: one turn's sink holds only that turn's calls.
func (c stepCalls) titles() map[string]string {
	var out map[string]string
	for k, call := range c {
		if out == nil {
			out = make(map[string]string, len(c))
		}
		tc := marotte.ToolCallOfEntry(call)
		out[k.id], _ = marotte.ToolCard(&tc)
	}
	return out
}

// settle answers the line a result prints, its card's settled title where that differs from the call's own (as when
// the command arrived only on the result), else "", and releases the call. An aborted result keeps it: KAS can still
// say how the call ended in a late result (appendAfterClosed).
func (c stepCalls) settle(e *marotte.Entry) string {
	k := stepCallKey{e.Turn, marotte.ToolCallIDOfResult(e.ID)}
	call, ok := c[k]
	if !ok {
		return ""
	}
	var res marotte.EntryToolResult
	if json.Unmarshal(e.Payload, &res) != nil {
		delete(c, k)
		return ""
	}
	if res.Status != marotte.ToolAborted {
		delete(c, k)
	}
	own, settled := marotte.ToolCallOfEntry(call), marotte.SettledToolCall(call, &res)
	title, _ := marotte.ToolCard(&settled)
	if was, _ := marotte.ToolCard(&own); title != was {
		return titleLine(title)
	}
	return ""
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
		if line := strings.TrimSpace(tailBlockMarkers.ReplaceAllString(raw, "")); line != "" {
			return capRunes(line)
		}
	}
	return ""
}

// titleLine is a tool card title as the dock prints it: the card draws the title as given, a command's shell marks
// included, so it is only trimmed and cut to tailMaxRunes.
func titleLine(title string) string {
	return capRunes(strings.TrimSpace(title))
}

func capRunes(line string) string {
	if r := []rune(line); len(r) > tailMaxRunes {
		return string(r[:tailMaxRunes])
	}
	return line
}
