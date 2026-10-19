package translate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// feedSayAs streams one chunk of say sayID, reasoning or reply, as KAS frames it.
func feedSayAs(t *testing.T, tr *Translator, sayID, text string, reasoning bool, attr FrameAttribution) {
	t.Helper()
	frame := map[string]any{
		"content": map[string]any{"type": "text", "text": text},
		"_meta":   map[string]any{"kiro": map[string]any{"messageId": sayID}},
	}
	tr.HandleAssistantChunk(t.Context(), testChat, mustJSON(t, frame), reasoning, attr)
}

// streamShape is a log in order, sealed entries then the open ones: `<kind> <id> "<text>"` for
// a say, `<kind>` for the rest.
func streamShape(t *testing.T, sealed, open []marotte.Entry) []string {
	t.Helper()
	var out []string
	for _, e := range append(sealed, open...) {
		switch e.Kind {
		case marotte.EntryKindText, marotte.EntryKindThinking:
			out = append(out, fmt.Sprintf("%s %s %q", e.Kind, e.ID, textOf(t, e)))
		default:
			out = append(out, string(e.Kind))
		}
	}
	return out
}

func asEntries(t *testing.T, open []marotte.OpenEntry) []marotte.Entry {
	t.Helper()
	out := make([]marotte.Entry, 0, len(open))
	for _, o := range open {
		raw := mustJSON(t, marotte.EntryText{Text: o.Text})
		out = append(out, marotte.Entry{ID: o.ID, Kind: o.Kind, Lane: o.Lane, Payload: raw})
	}
	return out
}

// A workflow message reaching the chat between two chunks of one say, mid-word, leaves the say
// whole: the message's row and its take-up sit at the say's boundary, before it.
func TestWorkflowMessage_MidStreamLeavesTheSayWhole(t *testing.T) {
	for _, reasoning := range []bool{true, false} {
		kind := marotte.EntryKindText
		if reasoning {
			kind = marotte.EntryKindThinking
		}
		t.Run(string(kind), func(t *testing.T) {
			deps, tr := messageDeps(t)
			deps.turns.chatTurn(testChat)
			const say = "f4d96109-say"
			feedSayAs(t, tr, say, "I need to locate the arrapi agent on Ag", reasoning, FrameAttribution{})
			tr.HandleWorkflowMessage(t.Context(), testChat,
				workflowNotify("step", testStep, testParent, "success", "Task 12: APPROVE"))
			feedSayAs(t, tr, say, "ent Mail, reg", reasoning, FrameAttribution{})
			tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
				"messageId": "notify-1", "content": "[notification/success] Task 12: APPROVE",
				"notificationSeverity": "success",
			}), FrameAttribution{})
			feedSayAs(t, tr, say, "ister", reasoning, FrameAttribution{})

			got := streamShape(t, deps.chatEntries(testChat), asEntries(t, deps.turns.chats[testChat].OpenEntries()))
			want := []string{
				"steer", "steer_delivered",
				fmt.Sprintf("%s %s %q", kind, say, "I need to locate the arrapi agent on Agent Mail, register"),
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("the chat's log is\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// A parent's message reaching a busy step between two chunks of the step's say leaves that say
// whole in the run's log.
func TestWorkflowMessage_MidStreamLeavesAStepsSayWhole(t *testing.T) {
	for _, reasoning := range []bool{true, false} {
		kind := marotte.EntryKindText
		if reasoning {
			kind = marotte.EntryKindThinking
		}
		t.Run(string(kind), func(t *testing.T) {
			deps, tr := messageDeps(t)
			path := workflow.PathKey([]string{testWfRun, "review"})
			attr := FrameAttribution{Step: true, SessionID: testStep, RunID: testWfRun, NodePath: path}
			const say = "b2c1-say"
			feedSayAs(t, tr, say, "merged land", reasoning, attr)
			tr.HandleWorkflowMessage(t.Context(), testChat,
				workflowNotify("parent", testParent, testStep, "info", "also check the tests"))
			feedSayAs(t, tr, say, "ings look right", reasoning, attr)

			run := deps.turns.runs[runPathKey(testWfRun, path)]
			got := streamShape(t, deps.runEntries(testWfRun, path), asEntries(t, run.OpenEntries()))
			want := []string{"steer", fmt.Sprintf("%s %s %q", kind, say, "merged landings look right")}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("the step's log is\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
