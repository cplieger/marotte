package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// chunkDeltas returns every streamed text delta in a capture, in order, so a test can assert
// the sentinel still REACHED the user.
func chunkDeltas(events []marotte.ServerEvent) []string {
	var out []string
	for _, e := range events {
		switch p := e.Payload.(type) {
		case marotte.EntryOpenedPayload:
			if e.Type == marotte.EventEntryOpened && p.Open.Kind == marotte.EntryKindText {
				out = append(out, p.Open.Text)
			}
		case marotte.EntryDeltaPayload:
			if e.Type == marotte.EventEntryDelta {
				out = append(out, p.Delta)
			}
		}
	}
	return out
}

func feedChunkAs(t *testing.T, tr *Translator, chatID marotte.ChatID, text string, isReasoning bool, attr FrameAttribution) {
	t.Helper()
	frame := map[string]any{"content": map[string]any{"type": "text", "text": text}}
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, frame), isReasoning, attr)
}

// TestHandleAssistantChunk_SentinelEndsTheTurn pins that the turn ends, the sentence is STILL
// broadcast, and the reason travels.
func TestHandleAssistantChunk_SentinelEndsTheTurn(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	feedChunk(t, tr, chatID, interruptSentinel)

	if len(deps.turnInterrupts) != 1 {
		t.Fatalf("got %d interrupts, want 1: the turn is wedged until the tab closes",
			len(deps.turnInterrupts))
	}
	if got := deps.turnInterrupts[0].chatID; got != chatID {
		t.Errorf("interrupted chat %q, want %q", got, chatID)
	}
	if got := deps.turnInterrupts[0].reason; got != interruptReason {
		t.Errorf("reason = %q, want %q — the divider has no other source for it", got, interruptReason)
	}

	// The sentence must reach the transcript before the interrupt's teardown takes the buffer.
	deltas := chunkDeltas(*events)
	if len(deltas) != 1 || deltas[0] != interruptSentinel {
		t.Errorf("streamed deltas = %q, want the sentinel shown to the user", deltas)
	}
}

// TestHandleAssistantChunk_SentinelIsExactMatchOnly pins rows that must NOT end the turn,
// quoted prose above all.
func TestHandleAssistantChunk_SentinelIsExactMatchOnly(t *testing.T) {
	cases := map[string]struct {
		text        string
		isReasoning bool
		attr        FrameAttribution
	}{
		"quoted inside prose": {
			text: `kiro-cli prints "` + interruptSentinel + `" when its filter trips.`,
		},
		"sentinel with a trailing sentence": {
			text: interruptSentinel + " Let me try a different approach.",
		},
		"a leading fragment of it": {
			text: "Tool uses were",
		},
		// The accepted miss: a prefix matcher would fire on any turn opening with these words.
		"split across deltas is deliberately missed": {
			text: "Tool uses were interrupted, waiting for",
		},
		// A sentinel-shaped THOUGHT is not the sentinel; KAS reads markers from text entries only.
		"as a reasoning chunk": {
			text:        interruptSentinel,
			isReasoning: true,
		},
		// The TUI's user-cancel sentinel: CmdCancel already ends that turn.
		"the user-cancel sentinel": {
			text: "Response was interrupted by the user",
		},
		// A step frame must not cancel the parent chat's live turn.
		"a workflow step frame": {
			text: interruptSentinel,
			attr: FrameAttribution{Step: true, RunID: "wf-1", NodePath: "wf-1/n-1"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))

			feedChunkAs(t, tr, "c1", tc.text, tc.isReasoning, tc.attr)

			if len(deps.turnInterrupts) != 0 {
				t.Errorf("the turn was ended by %q; only a delta that IS the sentinel may end one",
					tc.text)
			}
		})
	}
}

// TestHandleAssistantChunk_SentinelToleratesSurroundingWhitespace: the rule is
// exact equality on the TRIMMED delta, because a trailing newline is framing
// rather than content and dropping the trim would miss the real frame.
func TestHandleAssistantChunk_SentinelToleratesSurroundingWhitespace(t *testing.T) {
	for name, text := range map[string]string{
		"trailing newline": interruptSentinel + "\n",
		"leading newline":  "\n" + interruptSentinel,
		"padded":           "  " + interruptSentinel + "  ",
	} {
		t.Run(name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))

			feedChunk(t, tr, "c1", text)

			if len(deps.turnInterrupts) != 1 {
				t.Errorf("%q did not end the turn; whitespace around the sentinel is framing", text)
			}
		})
	}
}

// TestHandleAssistantChunk_SentinelEndsTheTurnOnce pins one interrupt call per matching delta;
// the HOST's per-turn latch makes a second a no-op, so keep the latch there.
func TestHandleAssistantChunk_SentinelEndsTheTurnOnce(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	feedChunk(t, tr, "c1", interruptSentinel)
	feedChunk(t, tr, "c1", interruptSentinel)

	if len(deps.turnInterrupts) != 2 {
		t.Errorf("got %d interrupts for 2 sentinel deltas, want 2; the one-shot lives on "+
			"the host (sharedBridge.interruptTurn), not here", len(deps.turnInterrupts))
	}
}
