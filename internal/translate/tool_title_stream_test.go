package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

type broadcastLog []marotte.ServerEvent

func (b *broadcastLog) Broadcast(_ context.Context, evt marotte.ServerEvent) { *b = append(*b, evt) }

var landLoaded = []marotte.LoadedResource{{ID: "skill::land", Type: marotte.LoadedSkill, Name: "land"}}

// appendedOf is the one entry_appended frame's entry of kind, failing when there is none or several.
func appendedOf(t *testing.T, events []marotte.ServerEvent, kind marotte.EntryKind) marotte.Entry {
	t.Helper()
	var out []marotte.Entry
	for _, e := range eventsOf(events, marotte.EventEntryAppended) {
		if p, ok := e.Payload.(marotte.EntryAppendedPayload); ok && p.Entry.Kind == kind {
			out = append(out, p.Entry)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d entry_appended frames carry a %s, want 1", len(out), kind)
	}
	return out[0]
}

func TestPublishAppended_StampsAToolCallCopy(t *testing.T) {
	e := marotte.Entry{Kind: marotte.EntryKindToolCall, ID: "tc-1", Turn: "t-1", Seq: 3}
	setPayload(&e, marotte.EntryToolCall{
		ID: "tc-1", Title: "Tool Load", Kind: marotte.ToolKindOther, Status: marotte.ToolPending,
		ToolName: "tool_load", Input: json.RawMessage(`{"id":"skill::land"}`),
	})
	stored := slices.Clone(e.Payload)
	var bus broadcastLog

	PublishAppended(t.Context(), &bus, "c1", "", &e)

	served := appendedOf(t, bus, marotte.EntryKindToolCall)
	got := decodePayload[marotte.EntryToolCall](t, &served)
	if got.CardTitle != "Loaded skill: land" || !reflect.DeepEqual(got.Loaded, landLoaded) {
		t.Errorf("broadcast card_title %q loaded %+v, want %q %+v", got.CardTitle, got.Loaded, "Loaded skill: land", landLoaded)
	}
	if !bytes.Equal(e.Payload, stored) {
		t.Errorf("the entry passed in was rewritten to %s, want the stored payload %s", e.Payload, stored)
	}
}

// A result's seal carries the call it settles, so its frame is titled from the pair a reload joins: the load
// call's tool name is only on the call.
func TestPublishSealed_StampsAResultFromTheCallItSettles(t *testing.T) {
	e := marotte.Entry{Kind: marotte.EntryKindToolResult, ID: marotte.ToolResultID("tc-1"), Turn: "t-1", Seq: 4}
	setPayload(&e, marotte.EntryToolResult{Title: "Tool Load", Status: marotte.ToolCompleted})
	stored := slices.Clone(e.Payload)
	call := &marotte.EntryToolCall{
		ID: "tc-1", Title: "Tool Load", Kind: marotte.ToolKindOther, ToolName: "tool_load", Input: json.RawMessage(`{"id":"skill::land"}`),
	}
	var bus broadcastLog

	PublishSealed(t.Context(), &bus, "c1", "", []turnlog.Sealed{{Entry: &e, Call: call}})

	served := appendedOf(t, bus, marotte.EntryKindToolResult)
	got := decodePayload[marotte.EntryToolResult](t, &served)
	if got.CardTitle != "Loaded skill: land" || !reflect.DeepEqual(got.Loaded, landLoaded) {
		t.Errorf("broadcast card_title %q loaded %+v, want %q %+v", got.CardTitle, got.Loaded, "Loaded skill: land", landLoaded)
	}
	if !bytes.Equal(e.Payload, stored) {
		t.Errorf("the entry passed in was rewritten to %s, want the stored payload %s", e.Payload, stored)
	}
}

// A close aborts a call still in flight; its frame is titled from the fold the live card drew, as a reload is.
func TestPublishSealed_TitlesACloseAbortAsTheLiveCardWas(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "title": "Run Command", "kind": "execute", "status": "pending", "rawInput": map[string]any{},
	}), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress", "rawInput": map[string]any{"command": "go vet ./..."},
	}), FrameAttribution{})
	turn := deps.turns.chats[chatID]
	sealed, err := turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCancelled})
	if err != nil {
		t.Fatal(err)
	}

	PublishSealed(t.Context(), deps, chatID, "", sealed)

	served := appendedOf(t, *events, marotte.EntryKindToolResult)
	if got := decodePayload[marotte.EntryToolResult](t, &served); got.CardTitle != "go vet ./..." {
		t.Errorf("abort frame card_title = %q, want the command the live card drew", got.CardTitle)
	}
	reloaded := slices.Clone(deps.turns.entriesOf(turn))
	marotte.ServeToolEntries(reloaded, nil)
	for i := range reloaded {
		if reloaded[i].Kind == marotte.EntryKindToolResult {
			if got := decodePayload[marotte.EntryToolResult](t, &reloaded[i]); got.CardTitle != "go vet ./..." {
				t.Errorf("reloaded abort card_title = %q, want the command the live card drew", got.CardTitle)
			}
		}
	}
}

// The run appender files a late step result and announces it, titled from the call it joins in the closed
// turn; the translator announces nothing more, or the result would reach the client twice.
func TestLateStepResult_TheRunAppenderAnnouncesIt(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	path := []string{"wf_1", "build"}
	attr := FrameAttribution{Step: true, RunID: "wf_1", NodePath: workflow.PathKey(path)}
	deps.RunNodeStart(t.Context(), &RunStep{RunID: attr.RunID, NodePath: attr.NodePath}, "c1")
	deps.RunNodeComplete(t.Context(), attr.RunID, path, "completed", "")

	tr.HandleToolCallUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"toolCallId": "tc-late", "status": "completed"}), attr)

	if len(deps.runBetween[runPathKey(attr.RunID, attr.NodePath)]) != 1 {
		t.Fatalf("Setup: filed after the close: %+v, want one tool_result", deps.runBetween)
	}
	for _, e := range eventsOf(*events, marotte.EventEntryAppended) {
		if p, ok := e.Payload.(marotte.EntryAppendedPayload); ok && p.Entry.Kind == marotte.EntryKindToolResult {
			t.Errorf("the translator announced the late result %s itself", p.Entry.Payload)
		}
	}
}

func TestToolProgress_CarriesTheCardTitleWhenItChanges(t *testing.T) {
	shell := marotte.ToolCall{ID: "tc-1", Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolInProgress}
	disclose := marotte.ToolCall{
		ID: "tc-1", Title: "Disclose Context", Kind: marotte.ToolKindOther, Status: marotte.ToolInProgress,
		ToolName: "disclose_context", Input: json.RawMessage(`{"name":"release-notes"}`),
	}
	notesLoaded := []marotte.LoadedResource{{ID: "skill::release-notes", Type: marotte.LoadedSkill, Name: "release-notes"}}
	tests := []struct {
		name          string
		before, after func() marotte.ToolCall
		wantTitle     string
		wantLoaded    []marotte.LoadedResource
	}{
		{
			name:   "a placeholder shell call's command arrives on an update",
			before: func() marotte.ToolCall { return shell },
			after: func() marotte.ToolCall {
				c := shell
				c.Input = json.RawMessage(`{"command":"go test ./..."}`)
				return c
			},
			wantTitle: "go test ./...",
		},
		{
			name:   "disclosed arrives on progress",
			before: func() marotte.ToolCall { return disclose },
			after: func() marotte.ToolCall {
				c := disclose
				c.Disclosed = &marotte.ToolDisclosed{Type: "skill", DisplayName: "release-notes"}
				return c
			},
			wantTitle:  "Loaded skill: release-notes",
			wantLoaded: notesLoaded,
		},
		{
			name:      "a retitle",
			before:    func() marotte.ToolCall { c := shell; c.Kind = marotte.ToolKindRead; c.Title = "readFile"; return c },
			after:     func() marotte.ToolCall { c := shell; c.Kind = marotte.ToolKindRead; c.Title = "Read main.go"; return c },
			wantTitle: "Read main.go",
		},
		{
			name:   "a status-only frame",
			before: func() marotte.ToolCall { return disclose },
			after:  func() marotte.ToolCall { c := disclose; c.Status = marotte.ToolCompleted; return c },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := tt.before(), tt.after()
			d := toolProgress("t-1", &before, &after)
			if d.CardTitle != tt.wantTitle || !reflect.DeepEqual(d.Loaded, tt.wantLoaded) {
				t.Errorf("toolProgress card_title %q loaded %+v, want %q %+v", d.CardTitle, d.Loaded, tt.wantTitle, tt.wantLoaded)
			}
		})
	}
}

// The settle frame is titled from the folded call, which knows the tool name the result line does not store.
func TestHandleToolCallUpdate_SettleFrameCarriesTheFoldedTitle(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "title": "Tool Load", "kind": "other", "status": "pending",
		"rawInput": map[string]any{"id": "skill::land"},
	}), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	served := appendedOf(t, *events, marotte.EntryKindToolResult)
	got := decodePayload[marotte.EntryToolResult](t, &served)
	if got.CardTitle != "Loaded skill: land" || !reflect.DeepEqual(got.Loaded, landLoaded) {
		t.Errorf("settle frame card_title %q loaded %+v, want %q %+v", got.CardTitle, got.Loaded, "Loaded skill: land", landLoaded)
	}
	for _, e := range deps.turns.entriesOf(deps.turns.chats[chatID]) {
		if e.Kind == marotte.EntryKindToolCall || e.Kind == marotte.EntryKindToolResult {
			if bytes.Contains(e.Payload, []byte(`"card_title"`)) || bytes.Contains(e.Payload, []byte(`"loaded"`)) {
				t.Errorf("the log's %s line %s holds a served field", e.Kind, e.Payload)
			}
		}
	}
}

func TestHandleToolCall_TheWorkingLabelNamesTheCommand(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleToolCall(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "title": "Run Command", "kind": "execute", "status": "pending",
		"rawInput": map[string]any{"command": "go test ./..."},
	}), FrameAttribution{})

	labels := eventsOf(*events, marotte.EventWorkingLabel)
	if len(labels) != 1 {
		t.Fatalf("%d working_label frames, want 1", len(labels))
	}
	if got := labels[0].Payload.(marotte.WorkingLabelPayload).Label; got != "Running go test ./..." {
		t.Errorf("working label = %q, want %q", got, "Running go test ./...")
	}
}
