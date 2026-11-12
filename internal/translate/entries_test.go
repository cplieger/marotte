package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// TestEntryFrames_PublishOnTheLogsTopic pins that a run's log frames ride the run's topic, "run:<workflowID>",
// so only a connection scoped to that run receives them, and a chat's ride the broadcast topic "".
func TestEntryFrames_PublishOnTheLogsTopic(t *testing.T) {
	path := []string{"wf_1", "build"}
	cases := []struct {
		name      string
		attr      FrameAttribution
		wantTopic string
	}{
		{name: "a run step's frames", attr: FrameAttribution{Step: true, RunID: "wf_1", NodePath: workflow.PathKey(path)}, wantTopic: "run:wf_1"},
		{name: "a chat's frames", attr: FrameAttribution{}, wantTopic: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			if tc.attr.Step {
				deps.RunNodeStart(t.Context(), &RunStep{RunID: tc.attr.RunID, NodePath: tc.attr.NodePath}, "c1")
			}
			tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
				"content": map[string]any{"type": "text", "text": "hi"},
			}), false, tc.attr)
			tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
				"content": map[string]any{"type": "text", "text": " there"},
			}), false, tc.attr)
			tr.HandleToolCall(t.Context(), "c1", mustJSON(t, map[string]any{
				"toolCallId": "tc-1", "title": "readFile", "kind": "read", "status": "pending",
			}), tc.attr)

			logFrames := 0
			for _, e := range *events {
				switch e.Type {
				case marotte.EventTurnOpened, marotte.EventEntryOpened, marotte.EventEntryDelta,
					marotte.EventEntrySealed, marotte.EventEntryAppended, marotte.EventTurnClosed:
					logFrames++
					if e.Topic != tc.wantTopic {
						t.Errorf("%s topic = %q, want %q", e.Type, e.Topic, tc.wantTopic)
					}
				}
			}
			if logFrames < 3 {
				t.Fatalf("captured %d log frames, want the opened, delta and sealed frames at least", logFrames)
			}
		})
	}
}
