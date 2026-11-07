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

// The index never rejects a chat the scan matches, in either hook view. Logs cover every span kind, from an alphabet
// whose runes change case, accent and byte length under folding, with whitespace and quotes so queries hold several
// words and phrases. A message is split mid-word over two entries, with another lane's text or a hook the hidden
// view drops between them; queries are mostly case-flipped slices of the item texts the scan matches. The oracle is
// the scan plus titleAll.
func TestChatFilter_NeverRejectsAChatTheScanMatches(t *testing.T) {
	alphabet := []rune("abcdeKkİiΣσßẞéÉ✓ x\n\"")
	text := func(rt *rapid.T, label string, max int) string {
		return string(rapid.SliceOfN(rapid.SampledFrom(alphabet), 3, max).Draw(rt, label))
	}
	hooksHidden := &Store{hookStatusShown: func() bool { return false }}
	rapid.Check(t, func(rt *rapid.T) {
		name := text(rt, "name", 20)
		n := rapid.IntRange(1, 3).Draw(rt, "turns")
		turns := make([]*turnFixture, 0, n)
		for i := range n {
			turns = append(turns, drawTurn(rt, i, text))
		}
		entries, drawn := chatOf(turns...)
		views := [][]marotte.Entry{entries, hooksHidden.searchView(entries)}

		spans := []string{name}
		for _, view := range views {
			for _, it := range searchItems(view, indexSearchTurns(view, drawn)) {
				for _, seg := range it.segs {
					spans = append(spans, seg.text)
				}
			}
		}
		var query string
		if rapid.IntRange(0, 3).Draw(rt, "unrelated") == 0 {
			query = text(rt, "query", 6)
		} else {
			query = flipCase(rt, sliceRunes(rt, rapid.SampledFrom(spans).Draw(rt, "span"), 6))
		}

		matched := titleAll(name, query) > 0
		for _, view := range views {
			matched = matched || searchEntries(view, drawn, query, false).res.Matched > 0
		}
		if !matched {
			return
		}
		want := queryTrigrams(parseSearchQuery(query, false).terms)
		if !buildChatFilter(name, entries).holdsAll(want) {
			rt.Fatalf("filter rejected query %q that the scan matched (title %d) in chat %q with %d entries",
				query, titleAll(name, query), name, len(entries))
		}
		// The appender's filter: built over the log so far, then extended entry by entry. Neither half sees the scan's drawn
		// set, which is the strictest the build can be held to.
		builtOver := rapid.IntRange(0, len(entries)).Draw(rt, "built_over")
		extended := buildChatFilter(name, entries[:builtOver])
		for i := builtOver; i < len(entries); i++ {
			extended.addEntry(&entries[i])
		}
		if !extended.holdsAll(want) {
			rt.Fatalf("filter built over %d entries and extended with the other %d rejected query %q that the scan matched (title %d) in chat %q",
				builtOver, len(entries)-builtOver, query, titleAll(name, query), name)
		}
	})
}

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
		return splitMessage(rt, openTurn(id, uint64(i+1), nil), id, text).
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

// splitMessage writes one message as two entries of one say, cut at a drawn rune, with nothing, another lane's
// text or a quiet hook (one the hidden view drops) between the halves.
func splitMessage(rt *rapid.T, tf *turnFixture, id string, text func(*rapid.T, string, int) string) *turnFixture {
	content := []rune(text(rt, id+"_content", 40))
	cut := rapid.IntRange(0, len(content)).Draw(rt, id+"_cut")
	tf.text(id+"-a1#0", string(content[:cut]))
	switch rapid.IntRange(0, 2).Draw(rt, id+"_between") {
	case 1:
		tf.laneText("sub-1", id+"-d1", text(rt, id+"_lane", 20))
	case 2:
		tf.add("", id+"-h1", marotte.EntryKindToolCall, marotte.EntryToolCall{
			ID: id + "-h1", Title: "Hook fired: guard", Kind: marotte.ToolKindHook, Status: marotte.ToolCompleted,
			HookTrigger: "SessionStart",
		})
	}
	return tf.text(id+"-a1#1", string(content[cut:]))
}

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
