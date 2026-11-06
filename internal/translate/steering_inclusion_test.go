package translate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

var steeringDocs = []string{
	"file:///w/.kiro/steering/git-commits.md",
	"file:///w/.kiro/steering/testing.md",
}

const steeringTitleWant = "Steering loaded: git-commits.md, testing.md"

func steeringFields(subtask string) map[string]any {
	f := map[string]any{"kind": infoKindSteeringInclusion, "steeringDocuments": steeringDocs}
	if subtask != "" {
		f["agentSubtaskId"] = subtask
	}
	return f
}

func liveText(t *testing.T, id, text string) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta":         map[string]any{"kiro": map[string]any{"messageId": id}},
	})
}

// contentKinds is the entries' kinds with turn_open dropped, so a case reads as its content order.
func contentKinds(entries []marotte.Entry) []marotte.EntryKind {
	var out []marotte.EntryKind
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnOpen {
			out = append(out, entries[i].Kind)
		}
	}
	return out
}

func steeringCallsOf(t *testing.T, entries []marotte.Entry) []marotte.Entry {
	t.Helper()
	var out []marotte.Entry
	for _, e := range entries {
		if e.Kind == marotte.EntryKindToolCall && marotte.IsSteeringCallID(e.ID) {
			out = append(out, e)
		}
	}
	return out
}

func wantSteeringBox(t *testing.T, e marotte.Entry) marotte.EntryToolCall {
	t.Helper()
	var call marotte.EntryToolCall
	if err := json.Unmarshal(e.Payload, &call); err != nil {
		t.Fatalf("steering payload: %v", err)
	}
	if call.Kind != marotte.ToolKindSteering || call.Status != marotte.ToolCompleted ||
		call.Title != steeringTitleWant || !slices.Equal(call.SteeringDocs, steeringDocs) || call.ID != e.ID {
		t.Errorf("steering box = %+v (entry id %q), want kind steering, completed, %q, docs %v, the entry's id",
			call, e.ID, steeringTitleWant, steeringDocs)
	}
	return call
}

func TestSteeringDocName(t *testing.T) {
	cases := map[string]string{
		"file:///w/.kiro/steering/go.md":         "go.md",
		"file:///w/.kiro/steering/my%20notes.md": "my notes.md",
		"kiro-default-steering":                  "kiro-default-steering",
		"file:///":                               "file:///",
		"file://elsewhere/w/go.md":               "file://elsewhere/w/go.md",
		"file://localhost/w/go.md":               "go.md",
		"file:/w/go.md":                          "file:/w/go.md",
		"file:///w/.kiro/steering/":              "steering",
	}
	for id, want := range cases {
		if got := steeringDocName(id); got != want {
			t.Errorf("steeringDocName(%q) = %q, want %q", id, got, want)
		}
	}
}

// A set too long for one display line names the whole documents that fit and counts the rest, so
// the client can still link each name it shows.
func TestSteeringToolCall_TitleKeepsWholeNamesUnderTheCap(t *testing.T) {
	names := make([]string, 60)
	docs := make([]string, len(names))
	for i := range names {
		names[i] = fmt.Sprintf("document-number-%02d.md", i)
		docs[i] = "file:///w/.kiro/steering/" + names[i]
	}
	titleOf := func(shown int) string {
		return steeringTitlePrefix + strings.Join(names[:shown], ", ") + fmt.Sprintf(" +%d more", len(names)-shown)
	}
	call := steeringToolCall(docs, 0)
	shown := -1
	for k := 1; k < len(names); k++ {
		if call.Title == titleOf(k) {
			shown = k
		}
	}
	if shown < 0 {
		t.Fatalf("title = %q, want the prefix, the first names whole, then \" +N more\"", call.Title)
	}
	if len(call.Title) > maxDisplayTextBytes || len(titleOf(shown+1)) <= maxDisplayTextBytes {
		t.Errorf("title shows %d names in %d bytes, want the most that fit in %d", shown, len(call.Title), maxDisplayTextBytes)
	}
	if !slices.Equal(call.SteeringDocs, docs) {
		t.Errorf("steering_docs = %d ids, want all %d", len(call.SteeringDocs), len(docs))
	}

	huge := strings.Repeat("x", maxDisplayTextBytes)
	if got, want := steeringToolCall([]string{huge}, 0).Title, displayText(steeringTitlePrefix+huge); got != want {
		t.Errorf("a first name past the cap: title = %q, want the cut %q", got, want)
	}
}

func TestHandleSessionInfoUpdate_SteeringInclusionBox(t *testing.T) {
	t.Run("AtItsStreamPosition", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base))
		ctx := t.Context()
		tr.HandleAssistantChunk(ctx, "c1", liveText(t, "s1", "before"), false, FrameAttribution{})
		tr.HandleSessionInfoUpdate(ctx, "c1", liveInfoFrame(t, steeringFields("")), FrameAttribution{})
		tr.HandleAssistantChunk(ctx, "c1", liveText(t, "s2", "after"), false, FrameAttribution{})

		entries := base.chatEntries("c1")
		want := []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindToolCall}
		if got := contentKinds(entries); !slices.Equal(got, want) {
			t.Fatalf("sealed kinds = %v, want %v (the open say after it stays open)", got, want)
		}
		boxes := steeringCallsOf(t, entries)
		if len(boxes) != 1 {
			t.Fatalf("steering boxes = %d, want 1", len(boxes))
		}
		wantSteeringBox(t, boxes[0])
		if boxes[0].Lane != "" {
			t.Errorf("lane = %q, want the main lane", boxes[0].Lane)
		}
		if !hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("no entry_appended{tool_call} broadcast for the steering box")
		}
	})
	t.Run("SubagentLane", func(t *testing.T) {
		base, _ := newEventCaptureDeps()
		tr := New(rolesOf(base))
		tr.HandleSessionInfoUpdate(t.Context(), "c1", liveInfoFrame(t, steeringFields("sub-1")), FrameAttribution{})
		boxes := steeringCallsOf(t, base.chatEntries("c1"))
		if len(boxes) != 1 || boxes[0].Lane != "sub-1" {
			t.Fatalf("steering boxes = %+v, want one in lane sub-1", boxes)
		}
	})
	t.Run("AfterAReadInItsLane", func(t *testing.T) {
		base, _ := newEventCaptureDeps()
		tr := New(rolesOf(base))
		ctx := t.Context()
		tr.HandleToolCall(ctx, "c1", mustJSON(t, map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": "r1", "title": "Read File", "kind": "read",
			"status": "pending", "locations": []map[string]any{{"path": "/w/a.go"}},
		}), FrameAttribution{})
		tr.HandleToolCallUpdate(ctx, "c1", mustJSON(t, map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": "r1", "status": "completed",
		}), FrameAttribution{})
		tr.HandleSessionInfoUpdate(ctx, "c1", liveInfoFrame(t, steeringFields("")), FrameAttribution{})

		entries := base.chatEntries("c1")
		want := []marotte.EntryKind{marotte.EntryKindToolCall, marotte.EntryKindToolResult, marotte.EntryKindToolCall}
		if got := contentKinds(entries); !slices.Equal(got, want) {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
		if last := entries[len(entries)-1]; !marotte.IsSteeringCallID(last.ID) || last.Lane != "" {
			t.Errorf("last entry = %s lane %q, want the steering box in the read's lane", last.ID, last.Lane)
		}
	})
	t.Run("StepFrameInTheRunTurn", func(t *testing.T) {
		doors := map[string]func(tr *Translator, t *testing.T, raw []byte, attr FrameAttribution){
			"chat_hosted": func(tr *Translator, t *testing.T, raw []byte, attr FrameAttribution) {
				tr.HandleSessionInfoUpdate(t.Context(), "c1", raw, attr)
			},
			"run_hosted": func(tr *Translator, t *testing.T, raw []byte, attr FrameAttribution) {
				tr.HandleStepInfoUpdate(t.Context(), "run:wf_1", raw, attr)
			},
		}
		for name, feed := range doors {
			t.Run(name, func(t *testing.T) {
				base, _ := newEventCaptureDeps()
				tr := New(rolesOf(base))
				attr := FrameAttribution{Step: true, RunID: "wf_1", SessionID: "sess_step", NodePath: workflow.PathKey([]string{"wf_1", "build"})}
				feed(tr, t, liveInfoFrame(t, steeringFields("")), attr)
				boxes := steeringCallsOf(t, base.runEntries("wf_1", attr.NodePath))
				if len(boxes) != 1 {
					t.Fatalf("run turn steering boxes = %d, want 1", len(boxes))
				}
				wantSteeringBox(t, boxes[0])
				if got := steeringCallsOf(t, base.chatEntries("c1")); len(got) != 0 {
					t.Errorf("chat steering boxes after a step's inclusion = %d, want 0", len(got))
				}
				run := base.turns.runs[runPathKey("wf_1", attr.NodePath)]
				if _, err := run.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
					t.Fatalf("close the run turn: %v", err)
				}
				closes := entriesOfKind(base.runEntries("wf_1", attr.NodePath), marotte.EntryKindTurnClose)
				if len(closes) != 1 {
					t.Fatalf("run turn closes = %d, want 1", len(closes))
				}
				var footer marotte.EntryTurnClose
				payloadOf(t, closes[0], &footer)
				if !slices.Equal(footer.Steering, steeringDocs) {
					t.Errorf("run turn footer steering = %v, want %v", footer.Steering, steeringDocs)
				}
				if chat := base.turns.chats["c1"]; chat != nil {
					t.Errorf("a step's inclusion opened a chat turn %s", chat.ID())
				}
			})
		}
	})
}

// The footer's steering list reads the same documents once, beside the box.
func TestHandleSessionInfoUpdate_SteeringInclusionKeepsTheFooter(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	ctx := t.Context()
	turn := startedTurn(deps, "c1")
	tr.HandleSessionInfoUpdate(ctx, "c1", liveInfoFrame(t, steeringFields("")), FrameAttribution{})
	if _, err := turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
		t.Fatalf("close: %v", err)
	}
	entries := deps.turns.entriesOf(turn)
	if n := len(steeringCallsOf(t, entries)); n != 1 {
		t.Errorf("steering boxes = %d, want 1", n)
	}
	if n := len(entriesOfKind(entries, marotte.EntryKindToolResult)); n != 0 {
		t.Errorf("tool_result entries = %d, want 0: the close aborts no steering box", n)
	}
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(entriesOfKind(entries, marotte.EntryKindTurnClose)[0].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	if !slices.Equal(footer.Steering, steeringDocs) {
		t.Errorf("footer steering = %v, want %v", footer.Steering, steeringDocs)
	}
}

func replaySteering(t *testing.T, subtask string) [2]any {
	t.Helper()
	extra := map[string]any{"steeringDocuments": steeringDocs}
	if subtask != "" {
		extra["agentSubtaskId"] = subtask
	}
	return pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindSteeringInclusion, extra))
}

func TestEntryProjection_SteeringInclusionBox(t *testing.T) {
	t.Run("SamePayloadAsLive", func(t *testing.T) {
		turns := entryProject([][2]any{
			turnStartFrame(t),
			agentChunkFrame(t, "before"),
			replaySteering(t, ""),
			agentChunkFrame(t, "after"),
			turnEndFrame(t, "end_turn"),
		})
		if len(turns) != 1 {
			t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
		}
		want := []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindToolCall, marotte.EntryKindText, marotte.EntryKindTurnClose}
		if got := contentKinds(turns[0].Entries); !slices.Equal(got, want) {
			t.Fatalf("kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
		}
		boxes := steeringCallsOf(t, turns[0].Entries)
		if len(boxes) != 1 {
			t.Fatalf("steering boxes = %d, want 1", len(boxes))
		}
		got := wantSteeringBox(t, boxes[0])

		base, _ := newEventCaptureDeps()
		tr := New(rolesOf(base))
		tr.HandleSessionInfoUpdate(t.Context(), "c1", liveInfoFrame(t, steeringFields("")), FrameAttribution{})
		live := wantSteeringBox(t, steeringCallsOf(t, base.chatEntries("c1"))[0])
		got.ID, got.Ts, live.ID, live.Ts = "", 0, "", 0
		if !reflect.DeepEqual(got, live) {
			t.Errorf("replayed box %+v, want the live box %+v", got, live)
		}
		var footer marotte.EntryTurnClose
		payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &footer)
		if !slices.Equal(footer.Steering, steeringDocs) {
			t.Errorf("footer steering = %v, want %v", footer.Steering, steeringDocs)
		}
		if n := len(entriesOfKind(turns[0].Entries, marotte.EntryKindToolResult)); n != 0 {
			t.Errorf("tool_result entries = %d, want 0: the close aborts no steering box", n)
		}
	})
	t.Run("SubagentLane", func(t *testing.T) {
		turns := entryProject([][2]any{turnStartFrame(t), replaySteering(t, "sub-1"), turnEndFrame(t, "end_turn")})
		boxes := steeringCallsOf(t, turns[0].Entries)
		if len(boxes) != 1 || boxes[0].Lane != "sub-1" {
			t.Fatalf("steering boxes = %+v, want one in lane sub-1", boxes)
		}
	})
	// KAS persists the session-start set and a turn's new documents ahead of the message they
	// steer, so a replay may name them before that prompt's row: they open the prompt's turn.
	t.Run("AheadOfThePromptTopsItsTurn", func(t *testing.T) {
		prompt := replayUserRow{id: "u1", ts: "2026-09-15T12:00:00.000Z", text: "hi"}
		for name, frames := range map[string][][2]any{
			"first_turn":           {replaySteering(t, ""), prompt.frame(t), turnStartFrame(t), agentChunkFrame(t, "ok"), turnEndFrame(t, "end_turn")},
			"after_the_prompt_row": {prompt.frame(t), replaySteering(t, ""), turnStartFrame(t), agentChunkFrame(t, "ok"), turnEndFrame(t, "end_turn")},
			"after_a_closed_turn": {
				turnStartFrame(t), agentChunkFrame(t, "first"), turnEndFrame(t, "end_turn"),
				replaySteering(t, ""), prompt.frame(t), turnStartFrame(t), agentChunkFrame(t, "ok"), turnEndFrame(t, "end_turn"),
			},
		} {
			t.Run(name, func(t *testing.T) {
				turns := entryProject(frames)
				last := turns[len(turns)-1]
				var open marotte.EntryTurnOpen
				payloadOf(t, last.Entries[0], &open)
				if open.Prompt == nil || open.Prompt.Text != "hi" {
					t.Fatalf("last turn opens with %+v, want the prompt's turn:\n%s", open, dumpTurns(turns))
				}
				if len(last.Entries) < 2 || !marotte.IsSteeringCallID(last.Entries[1].ID) {
					t.Fatalf("entry 1 of the prompt's turn is not the steering box:\n%s", dumpTurns(turns))
				}
				total := 0
				for i := range turns {
					total += len(steeringCallsOf(t, turns[i].Entries))
				}
				if total != 1 {
					t.Errorf("steering boxes across turns = %d, want 1:\n%s", total, dumpTurns(turns))
				}
				var footer marotte.EntryTurnClose
				payloadOf(t, entryOfKind(t, last, marotte.EntryKindTurnClose), &footer)
				if !slices.Equal(footer.Steering, steeringDocs) {
					t.Errorf("prompt turn's footer steering = %v, want %v", footer.Steering, steeringDocs)
				}
			})
		}
	})
}
