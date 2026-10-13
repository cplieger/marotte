package translate

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// steerChunkingSamples are assistant replies holding steer ack markers, one per shape a live
// chat or a review found broken, with short ids: the filter's thresholds are the marker prefix's
// length, not the id's. want is the record reduced by proseAndAckSpans.
var steerChunkingSamples = []struct {
	name, text string
	want       []string
}{{
	name: "two adjacent markers after prose",
	text: "decide.\n\n[STEERING steer-A: body A]\n\n[STEERING steer-B: body B]",
	want: []string{`prose "decide.\n\n"`, `ack steer-A "body A"`, `prose "\n\n"`, `ack steer-B "body B"`},
}, {
	name: "three adjacent markers after prose",
	text: "(a).\n\n[STEERING steer-X: X]\n\n[STEERING steer-Y: Y]\n\n[STEERING steer-Z: Z]",
	want: []string{
		`prose "(a).\n\n"`, `ack steer-X "X"`, `prose "\n\n"`, `ack steer-Y "Y"`,
		`prose "\n\n"`, `ack steer-Z "Z"`,
	},
}, {
	name: "a reply that starts with a marker",
	text: "[STEERING steer-A: body A]\n\n[STEERING steer-B: body B]\n\nThen prose.",
	want: []string{`ack steer-A "body A"`, `prose "\n\n"`, `ack steer-B "body B"`, `prose "\n\nThen prose."`},
}, {
	name: "prose between and after markers",
	text: "decide. [STEERING steer-A: body A]x [STEERING steer-B: body B] tail",
	want: []string{`prose "decide. "`, `ack steer-A "body A"`, `prose "x "`, `ack steer-B "body B"`, `prose " tail"`},
}, {
	name: "brackets that never become a marker",
	text: "See [docs], [STEERING no colon] and [STEERING steer-A: A].",
	want: []string{`prose "See [docs], [STEERING no colon] and "`, `ack steer-A "A"`, `prose "."`},
}}

// steerChunk is one text delta: its text and whether it carries the say's messageId.
type steerChunk struct {
	text string
	id   bool
}

// steerChunkings cuts text in two at every point, each half with or without the messageId, so
// every id'd, id-less and mixed two-chunk stream of the say is produced. Longer cuts are the fuzz
// target's.
func steerChunkings(text string) [][]steerChunk {
	var out [][]steerChunk
	for i := 1; i < len(text); i++ {
		for mask := range 4 {
			out = append(out, []steerChunk{
				{text: text[:i], id: mask&1 != 0},
				{text: text[i:], id: mask&2 != 0},
			})
		}
	}
	return out
}

// proseAndAckSpans closes the turn, so a withheld tail settles, and reduces the live record to
// what a reader sees: the prose between acks, however many entries it took, and each ack.
func proseAndAckSpans(t *testing.T, deps *baseDeps, chatID marotte.ChatID) []string {
	t.Helper()
	if turn := deps.turns.chats[chatID]; turn != nil {
		if _, err := turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
			t.Fatalf("close the turn: %v", err)
		}
	}
	var out []string
	var prose strings.Builder
	flush := func() {
		if prose.Len() > 0 {
			out = append(out, fmt.Sprintf("prose %q", prose.String()))
			prose.Reset()
		}
	}
	for _, e := range deps.chatEntries(chatID) {
		switch e.Kind {
		case marotte.EntryKindText:
			prose.WriteString(textOf(t, e))
		case marotte.EntryKindSteerAck:
			flush()
			a := steerAcksOf(t, []marotte.Entry{e})[0]
			out = append(out, fmt.Sprintf("ack %s %q", a.SteerID, a.Text))
		}
	}
	flush()
	return out
}

// steerChunkingIDs are the steers the samples acknowledge, queued by liveSteerRecord when asked.
var steerChunkingIDs = []string{"steer-A", "steer-B", "steer-X", "steer-Y", "steer-Z"}

// liveSteerRecord streams chunks into a fresh chat, with every steer the samples acknowledge
// queued first when queued is set, so each ack also files the steer's read.
func liveSteerRecord(t *testing.T, chunks []steerChunk, queued bool) []string {
	t.Helper()
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	if queued {
		queueSteers(t, tr, chatID, steerChunkingIDs...)
	}
	for _, c := range chunks {
		say := ""
		if c.id {
			say = "m1-say"
		}
		feedSay(t, tr, chatID, say, c.text)
	}
	return proseAndAckSpans(t, deps, chatID)
}

// steerOpenings are what the say holds before a sample: nothing, or prose with or without the
// messageId, so the sample's first chunk opens, extends or changes the say.
var steerOpenings = [][]steerChunk{nil, {{text: "Opening. ", id: true}}, {{text: "Opening. "}}}

func describeChunks(chunks []steerChunk) string {
	parts := make([]string, len(chunks))
	for k, c := range chunks {
		id := "id-less"
		if c.id {
			id = "m1-say"
		}
		parts[k] = fmt.Sprintf("%s %q", id, c.text)
	}
	return strings.Join(parts, " | ")
}

// The live record of a say does not depend on where the stream was cut, which chunks carried
// the messageId, or whether the acknowledged steers were queued: every two-chunk stream files the
// prose and acks of the reply sent in one piece.
func TestHandleAssistantChunk_SteerAcksIgnoreChunking(t *testing.T) {
	for _, s := range steerChunkingSamples {
		t.Run(s.name, func(t *testing.T) {
			for _, queued := range []bool{false, true} {
				for _, opening := range steerOpenings {
					want := s.want
					if len(opening) > 0 {
						want = withOpening(s.want, opening[0].text)
					}
					whole := append(slices.Clone(opening), steerChunk{text: s.text, id: true})
					if got := liveSteerRecord(t, whole, queued); !slices.Equal(got, want) {
						t.Fatalf("whole reply %s, queued %t:\n got  %q\n want %q", describeChunks(whole), queued, got, want)
					}
					for _, chunks := range steerChunkings(s.text) {
						chunks = append(slices.Clone(opening), chunks...)
						if got := liveSteerRecord(t, chunks, queued); !slices.Equal(got, want) {
							t.Fatalf("chunks %s, queued %t:\n got  %q\n want %q", describeChunks(chunks), queued, got, want)
						}
					}
				}
			}
		})
	}
}

// withOpening is want with opening prose ahead of it, merged into the first prose span.
func withOpening(want []string, opening string) []string {
	out := slices.Clone(want)
	if len(out) > 0 && strings.HasPrefix(out[0], "prose ") {
		var first string
		if _, err := fmt.Sscanf(out[0], "prose %q", &first); err == nil {
			out[0] = fmt.Sprintf("prose %q", opening+first)
			return out
		}
	}
	return append([]string{fmt.Sprintf("prose %q", opening)}, out...)
}

// The replay projection discards acks; its prose is as independent of the chunking as the live
// record's.
func TestEntryProjection_SteerAcksIgnoreChunking(t *testing.T) {
	for _, s := range steerChunkingSamples {
		t.Run(s.name, func(t *testing.T) {
			for _, opening := range steerOpenings {
				want := proseOf(s.want)
				if len(opening) > 0 {
					want = opening[0].text + want
				}
				for _, chunks := range steerChunkings(s.text) {
					chunks = append(slices.Clone(opening), chunks...)
					if got := projectedProse(t, chunks); got != want {
						t.Fatalf("chunks %s:\n got  %q\n want %q", describeChunks(chunks), got, want)
					}
				}
			}
		})
	}
}

func projectedProse(t *testing.T, chunks []steerChunk) string {
	t.Helper()
	frames := [][2]any{turnStartFrame(t)}
	for _, c := range chunks {
		var extra map[string]any
		if c.id {
			extra = map[string]any{"messageId": "m1-say"}
		}
		frames = append(frames, pair(replayFrame(t, marotte.ACPUpdateAgentChunk, c.text, "", extra)))
	}
	frames = append(frames, turnEndFrame(t, "end_turn"))
	var b strings.Builder
	for _, turn := range entryProject(frames) {
		for _, e := range turn.Entries {
			if e.Kind == marotte.EntryKindText {
				b.WriteString(textOf(t, e))
			}
		}
	}
	return b.String()
}

// proseOf joins the prose spans of a reduced record, the text a reader sees with the acks gone.
func proseOf(spans []string) string {
	var b strings.Builder
	for _, span := range spans {
		var text string
		if _, err := fmt.Sscanf(span, "prose %q", &text); err == nil {
			b.WriteString(text)
		}
	}
	return b.String()
}
