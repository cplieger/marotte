package translate

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// feedSay streams chunks as text deltas under one say, the shape KAS sends: every delta of an
// assistant message carries its messageId. An empty sayID sends id-less deltas, a tangent's shape.
func feedSay(t *testing.T, tr *Translator, chatID marotte.ChatID, sayID string, chunks ...string) {
	t.Helper()
	for _, c := range chunks {
		frame := map[string]any{"content": map[string]any{"type": "text", "text": c}}
		if sayID != "" {
			frame["_meta"] = map[string]any{"kiro": map[string]any{"messageId": sayID}}
		}
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, frame), false, FrameAttribution{})
	}
}

// queueSteers puts steers in KAS's buffer, as steering_queued does before the agent reads them:
// an ack of a queued steer files its read ahead of the ack.
func queueSteers(t *testing.T, tr *Translator, chatID marotte.ChatID, steerIDs ...string) {
	t.Helper()
	for _, id := range steerIDs {
		tr.HandleSessionInfoUpdate(t.Context(), chatID,
			steerFrame(t, "steering_queued", map[string]any{"messageId": id, "content": "do " + id}), FrameAttribution{})
	}
}

// proseAndAcks is the chat's record in order, sealed entries then the open ones, reduced to its
// text and steer_ack entries: `text <id> "<text>"` and `ack <steer id> "<statement>"`.
func proseAndAcks(t *testing.T, deps *baseDeps, chatID marotte.ChatID) []string {
	t.Helper()
	var out []string
	for _, e := range deps.chatEntries(chatID) {
		switch e.Kind {
		case marotte.EntryKindText:
			out = append(out, fmt.Sprintf("text %s %q", e.ID, textOf(t, e)))
		case marotte.EntryKindSteerAck:
			a := steerAcksOf(t, []marotte.Entry{e})[0]
			out = append(out, fmt.Sprintf("ack %s %q", a.SteerID, a.Text))
		}
	}
	for _, o := range deps.turns.chats[chatID].OpenEntries() {
		if o.Kind == marotte.EntryKindText {
			out = append(out, fmt.Sprintf("text %s %q", o.ID, o.Text))
		}
	}
	return out
}

// A chunk that closes one marker and opens the next files the text around each ack on the side
// of it the stream put it. The first two cases are chunk sequences recorded in live chats, where
// the second marker's opening `[STEER` was released before the first ack and the rest leaked.
func TestHandleAssistantChunk_SteerAcksKeepStreamOrder(t *testing.T) {
	const (
		idA = "steer-m-mv6w11yd-5l1r1x1z1l535ub2c38"
		idB = "steer-m-mv6w1po3-2l2j1g4x4w1z305q4542"
		idX = "steer-m-mv6xli1s-1fo5c7354b506h3b3z"
		idY = "steer-m-mv6xm66k-3c5y5j6j1n6u3u4p4g1k"
		idZ = "steer-m-mv6xmdmz-p51t3m6o5x6b18373n"
	)
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{{
		name: "the closing chunk ends inside the next marker's prefix",
		chunks: []string{
			"The question is now with the langtag investigation once you decide.\n\n[STEER",
			"ING " + idA + ": I sent it to the langtag investigation.]\n\n[STEER",
			"ING " + idB + ": I sent the in-app level setting.]",
		},
		want: []string{
			`text m1-say "Opening. The question is now with the langtag investigation once you decide.\n\n"`,
			`ack ` + idA + ` "I sent it to the langtag investigation."`,
			`text m1-say#2 "\n\n"`,
			`ack ` + idB + ` "I sent the in-app level setting."`,
		},
	}, {
		name: "three markers where the first closing chunk opens the second",
		chunks: []string{
			"- **Recommendation:** (a).\n\n[STEER",
			"ING " + idX + ": I confirmed the UI rule.]\n\n[STEER",
			"ING " + idY + ": I recorded the clean-rebuild rule as DECISIONS.md item 14.]\n\n",
			"[STEERING " + idZ + ": I applied the from-scratch rule.]",
		},
		want: []string{
			`text m1-say "Opening. - **Recommendation:** (a).\n\n"`,
			`ack ` + idX + ` "I confirmed the UI rule."`,
			`text m1-say#2 "\n\n"`,
			`ack ` + idY + ` "I recorded the clean-rebuild rule as DECISIONS.md item 14."`,
			`text m1-say#3 "\n\n"`,
			`ack ` + idZ + ` "I applied the from-scratch rule."`,
		},
	}, {
		name: "the closing chunk ends past the next marker's prefix",
		chunks: []string{
			"decide.\n\n",
			"[STEERING " + idA + ": body A]\n\n[STEERING " + idB,
			": body B]",
		},
		want: []string{
			`text m1-say "Opening. decide.\n\n"`,
			`ack ` + idA + ` "body A"`,
			`text m1-say#2 "\n\n"`,
			`ack ` + idB + ` "body B"`,
		},
	}, {
		name: "prose before a marker in the same chunk",
		chunks: []string{
			"decide. ",
			"[STEERING " + idA + ": body A]",
			" after [STEERING " + idB + ": body B] tail",
		},
		want: []string{
			`text m1-say "Opening. decide. "`,
			`ack ` + idA + ` "body A"`,
			`text m1-say#2 " after "`,
			`ack ` + idB + ` "body B"`,
			`text m1-say#3 " tail"`,
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, queued := range []bool{false, true} {
				deps, _ := newEventCaptureDeps()
				tr := New(rolesOf(deps))
				chatID := marotte.ChatID("c1")
				if queued {
					queueSteers(t, tr, chatID, idA, idB, idX, idY, idZ)
				}
				// The say is already open when the markers arrive, as after a reply's prose.
				feedSay(t, tr, chatID, "m1-say", append([]string{"Opening. "}, tt.chunks...)...)
				if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, tt.want) {
					t.Errorf("record with steers queued %t:\n got  %q\n want %q", queued, got, tt.want)
				}
			}
		})
	}
}

// Bytes withheld at the end of one say stay in it when the next say begins, and bytes withheld
// by the next say never land in the say before it.
func TestHandleAssistantChunk_SteerCarryStaysInItsSay(t *testing.T) {
	t.Run("a held tail settles into its own say", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		feedSay(t, tr, chatID, "m1-say", "Look at [")
		feedSay(t, tr, chatID, "m2-say", "the docs.")
		want := []string{`text m1-say "Look at ["`, `text m2-say "the docs."`}
		if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, want) {
			t.Errorf("record:\n got  %q\n want %q", got, want)
		}
	})
	t.Run("a held head stays out of the previous say", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		feedSay(t, tr, chatID, "m1-say", "First. ")
		feedSay(t, tr, chatID, "m2-say", "Second [ST", "OP] here.")
		want := []string{`text m1-say "First. "`, `text m2-say "Second [STOP] here."`}
		if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, want) {
			t.Errorf("record:\n got  %q\n want %q", got, want)
		}
	})
	// An id-less delta extends the open say, so a marker it opens is completed by the say's next
	// id'd chunk instead of settling as if another say had begun.
	t.Run("an id-less chunk's held prefix belongs to the say it extends", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		feedSay(t, tr, chatID, "m1-say", "Opening. ")
		feedSay(t, tr, chatID, "", "decide.\n\n[STEER")
		feedSay(t, tr, chatID, "m1-say", "ING steer-A: body A]")
		want := []string{`text m1-say "Opening. decide.\n\n"`, `ack steer-A "body A"`}
		if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, want) {
			t.Errorf("record:\n got  %q\n want %q", got, want)
		}
	})
	t.Run("an id-less chunk's held tail settles into the say it extends", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		feedSay(t, tr, chatID, "m1-say", "Look at ")
		feedSay(t, tr, chatID, "", "the [")
		feedSay(t, tr, chatID, "m2-say", "Next.")
		want := []string{`text m1-say "Look at the ["`, `text m2-say "Next."`}
		if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, want) {
			t.Errorf("record:\n got  %q\n want %q", got, want)
		}
	})
	// The ack, and the read of a queued steer filed ahead of it, seal the say's entry, not the say:
	// id-less prose after it continues the say, so the prefix it holds is completed by the say's
	// next id'd chunk.
	for name, queued := range map[string]bool{"": false, " of a queued steer": true} {
		t.Run("an id-less chunk after an ack"+name+" continues the say", func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			chatID := marotte.ChatID("c1")
			if queued {
				queueSteers(t, tr, chatID, "steer-A", "steer-B")
			}
			feedSay(t, tr, chatID, "m1-say", "Opening. ", "[STEERING steer-A: body A]")
			feedSay(t, tr, chatID, "", "x [ST")
			feedSay(t, tr, chatID, "m1-say", "EERING steer-B: body B]")
			want := []string{`text m1-say "Opening. "`, `ack steer-A "body A"`, `text m1-say#2 "x "`, `ack steer-B "body B"`}
			if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, want) {
				t.Errorf("record:\n got  %q\n want %q", got, want)
			}
		})
		t.Run("a prefix held with nothing open after an ack"+name+" joins the next chunk", func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			chatID := marotte.ChatID("c1")
			if queued {
				queueSteers(t, tr, chatID, "steer-A", "steer-B")
			}
			feedSay(t, tr, chatID, "m1-say", "Opening. ", "[STEERING steer-A: body A]")
			feedSay(t, tr, chatID, "", "[ST")
			feedSay(t, tr, chatID, "m1-say", "EERING steer-B: body B]")
			want := []string{`text m1-say "Opening. "`, `ack steer-A "body A"`, `ack steer-B "body B"`}
			if got := proseAndAcks(t, deps, chatID); !slices.Equal(got, want) {
				t.Errorf("record:\n got  %q\n want %q", got, want)
			}
		})
	}
}
