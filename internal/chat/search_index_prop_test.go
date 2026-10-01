package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

// The one invariant the index keeps: it never rejects a chat the scan matches.
// Logs are built over every span kind the scan reads (title, prompt, attachment
// names, prose in two lanes, reasoning, tool title/input/output/diff, a steer, plan
// entries, a failure reason) from an alphabet whose runes change case and byte
// length under folding; a query is usually a case-flipped slice of one of those
// spans, so most draws match. The oracle is the scan itself plus titleHits, never
// a second trigram walk.
func TestChatFilter_NeverRejectsAChatTheScanMatches(t *testing.T) {
	alphabet := []rune("abcdeKkİiΣσßẞé✓ x\n")
	text := func(rt *rapid.T, label string, max int) string {
		return string(rapid.SliceOfN(rapid.SampledFrom(alphabet), 3, max).Draw(rt, label))
	}
	rapid.Check(t, func(rt *rapid.T) {
		name := text(rt, "name", 20)
		n := rapid.IntRange(1, 3).Draw(rt, "turns")
		turns := make([]*turnFixture, 0, n)
		for i := range n {
			turns = append(turns, drawTurn(rt, i, text))
		}
		entries, drawn := chatOf(turns...)

		spans := []string{name}
		index := indexSearchTurns(entries, drawn)
		for i := range entries {
			for _, seg := range entrySegments(&entries[i], index[entries[i].Turn]) {
				spans = append(spans, seg.text)
			}
		}
		var query string
		if rapid.IntRange(0, 3).Draw(rt, "unrelated") == 0 {
			query = text(rt, "query", 6)
		} else {
			query = flipCase(rt, sliceRunes(rt, rapid.SampledFrom(spans).Draw(rt, "span"), 6))
		}

		res, _ := searchEntries(entries, drawn, query, false)
		matched := res.Matched > 0 || titleHits(name, query) > 0
		if !matched {
			return
		}
		want := queryTrigrams(parseSearchQuery(query, false).text)
		if !buildChatFilter(name, entries).holdsAll(want) {
			rt.Fatalf("filter rejected query %q that the scan matched (%d body hits, %d title hits) in chat %q with %d entries",
				query, res.Matched, titleHits(name, query), name, len(entries))
		}
		// The appender's filter: built by a query over the log as it stood, then
		// extended one entry at a time, each read alone. It holds whatever the
		// query-time build holds, and a tool_call's whole input besides. Neither
		// half sees the drawn set the scan read with: drawn is a bit the appender
		// flips, and a filter built while a turn was undrawn outlives the append
		// that draws it, so the scan's drawn set is the strictest the build can
		// be held to and the build must hold to every one.
		builtOver := rapid.IntRange(0, len(entries)).Draw(rt, "built_over")
		extended := buildChatFilter(name, entries[:builtOver])
		for i := builtOver; i < len(entries); i++ {
			extended.addEntry(&entries[i])
		}
		if !extended.holdsAll(want) {
			rt.Fatalf("filter built over %d entries and extended with the other %d rejected query %q that the scan matched with every turn drawn (%d body hits, %d title hits) in chat %q",
				builtOver, len(entries)-builtOver, query, res.Matched, titleHits(name, query), name)
		}
	})
}

// drawTurn is one turn of a random shape, each shape feeding a different set of
// segment kinds.
func drawTurn(rt *rapid.T, i int, text func(*rapid.T, string, int) string) *turnFixture {
	id := fmt.Sprintf("t-%d", i)
	switch rapid.IntRange(0, 3).Draw(rt, id+"_shape") {
	case 0:
		return openTurn(id, uint64(i+1), &marotte.EntryPrompt{
			ID: "m-" + id, Text: text(rt, id+"_prompt", 40),
			Attachments: []marotte.Attachment{{Path: "x", Name: text(rt, id+"_attachment", 12)}},
		}).
			add("", id+"-steer", marotte.EntryKindSteer, marotte.EntrySteer{
				Text:   text(rt, id+"_steer", 20),
				Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
			})
	case 1:
		return openTurn(id, uint64(i+1), nil).
			text(id+"-a1", text(rt, id+"_content", 40)).
			add("", id+"-plan", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: text(rt, id+"_plan", 20)}}}).
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: text(rt, id+"_failure", 20)})
	case 2:
		return openTurn(id, uint64(i+1), nil).
			thinking(id+"-th1", text(rt, id+"_thinking", 40)).
			laneText("sub-1", id+"-a1", text(rt, id+"_text", 40))
	default:
		input, err := json.Marshal(map[string]string{"command": text(rt, id+"_input", 20)})
		if err != nil {
			rt.Fatalf("marshal input: %v", err)
		}
		return openTurn(id, uint64(i+1), nil).
			tool(marotte.EntryToolCall{ID: id + "-tc1", Title: text(rt, id+"_title", 20), Input: input},
				&marotte.EntryToolResult{
					Status: marotte.ToolCompleted, Output: text(rt, id+"_output", 40),
					Diffs: []marotte.ToolDiff{{Path: "p", NewText: text(rt, id+"_diff", 40)}},
				})
	}
}

// sliceRunes is a random rune window of s, at most max runes long and at least
// three where s allows, so most windows are long enough to carry a trigram.
func sliceRunes(rt *rapid.T, s string, max int) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	start := rapid.IntRange(0, len(runes)-1).Draw(rt, "start")
	longest := min(max, len(runes)-start)
	end := start + rapid.IntRange(min(3, longest), longest).Draw(rt, "length")
	return string(runes[start:end])
}

// flipCase upper-cases a random subset of s's runes, so the query differs from the
// span it was cut from by case alone.
func flipCase(rt *rapid.T, s string) string {
	var b strings.Builder
	for i, r := range s {
		if rapid.Bool().Draw(rt, fmt.Sprintf("flip_%d", i)) {
			r = unicode.ToUpper(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
