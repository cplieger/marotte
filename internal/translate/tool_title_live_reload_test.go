package translate

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// drawn is what a tool card draws of its served fields: its title and loaded list.
type drawn struct {
	title  string
	loaded []marotte.LoadedResource
}

// fold is the client's fold of a served card field pair: an absent one keeps the card's.
func (d *drawn) fold(title string, loaded []marotte.LoadedResource) {
	if title != "" {
		d.title = title
	}
	if loaded != nil {
		d.loaded = loaded
	}
}

// liveCards folds the frames a client receives into each card by call id.
func liveCards(t *testing.T, events []marotte.ServerEvent) map[string]drawn {
	t.Helper()
	cards := map[string]drawn{}
	for _, evt := range events {
		switch p := evt.Payload.(type) {
		case marotte.EntryAppendedPayload:
			foldEntry(t, cards, &p.Entry)
		case marotte.ToolProgressPayload:
			c := cards[p.ToolCallID]
			c.fold(p.CardTitle, p.Loaded)
			cards[p.ToolCallID] = c
		}
	}
	return cards
}

// reloadedCards folds a reload of the log, served as every reader serves it, into each card by call id.
func reloadedCards(t *testing.T, stored []marotte.Entry) map[string]drawn {
	t.Helper()
	served := slices.Clone(stored)
	marotte.ServeToolEntries(served, nil)
	cards := map[string]drawn{}
	for i := range served {
		foldEntry(t, cards, &served[i])
	}
	return cards
}

func foldEntry(t *testing.T, cards map[string]drawn, e *marotte.Entry) {
	t.Helper()
	switch e.Kind {
	case marotte.EntryKindToolCall:
		c := decodePayload[marotte.EntryToolCall](t, e)
		cards[e.ID] = drawn{title: c.CardTitle, loaded: c.Loaded}
	case marotte.EntryKindToolResult:
		r := decodePayload[marotte.EntryToolResult](t, e)
		id := marotte.ToolCallIDOfResult(e.ID)
		c := cards[id]
		c.fold(r.CardTitle, r.Loaded)
		cards[id] = c
	}
}

// For every card kind, the card a client folds from the live frames is the card a reload of the log draws.
func TestToolCard_TheLiveFramesFoldToTheReloadedCard(t *testing.T) {
	const chatID = marotte.ChatID("c1")
	notes := []marotte.LoadedResource{{ID: "skill::release-notes", Type: marotte.LoadedSkill, Name: "release-notes"}}
	tests := []struct {
		name string
		// frames are the tool_call then tool_call_update frames; close ends the turn after them.
		frames []map[string]any
		close  bool
		want   drawn
	}{
		{
			name:   "a shell call whose command the create sends",
			frames: []map[string]any{shellCreate(map[string]any{"command": "go test ./..."}), update("completed", nil)},
			want:   drawn{title: "go test ./..."},
		},
		{
			name: "a placeholder shell call whose command an update sends",
			frames: []map[string]any{
				shellCreate(map[string]any{}),
				update("in_progress", map[string]any{"rawInput": map[string]any{"command": "go vet ./..."}}),
				update("completed", nil),
			},
			want: drawn{title: "go vet ./..."},
		},
		{
			name: "a placeholder shell call whose command only the settle sends",
			frames: []map[string]any{
				shellCreate(map[string]any{}),
				update("completed", map[string]any{"rawInput": map[string]any{"command": "go vet ./..."}}),
			},
			want: drawn{title: "go vet ./..."},
		},
		{
			name: "a load call",
			frames: []map[string]any{
				{"toolCallId": "tc-1", "title": "Tool Load", "kind": "other", "status": "pending", "rawInput": map[string]any{"id": "skill::land"}},
				update("completed", nil),
			},
			want: drawn{title: "Loaded skill: land", loaded: landLoaded},
		},
		{
			name: "a disclose_context the settle names",
			frames: []map[string]any{
				{
					"toolCallId": "tc-1", "title": "Disclose Context", "kind": "other", "status": "pending",
					"_meta": map[string]any{"kiro": map[string]any{"toolId": "disclose_context"}},
				},
				update("completed", map[string]any{"_meta": map[string]any{"kiro": map[string]any{
					"disclosedContext": map[string]any{"type": "skill", "displayName": "release-notes"},
				}}}),
			},
			want: drawn{title: "Loaded skill: release-notes", loaded: notes},
		},
		{
			name: "an MCP call",
			frames: []map[string]any{
				{
					"toolCallId": "tc-1", "title": "@github/list_pull_requests", "kind": "other", "status": "pending",
					"_meta": map[string]any{"kiro": map[string]any{"serverName": "github"}},
				},
				update("completed", nil),
			},
			want: drawn{title: "list pull requests"},
		},
		{
			name: "a retitle on an update",
			frames: []map[string]any{
				{"toolCallId": "tc-1", "title": "readFile", "kind": "read", "status": "pending"},
				update("in_progress", map[string]any{"title": "Read main.go"}),
				update("completed", nil),
			},
			want: drawn{title: "Read main.go"},
		},
		{
			name: "a call the turn's close aborts",
			frames: []map[string]any{
				shellCreate(map[string]any{}),
				update("in_progress", map[string]any{"rawInput": map[string]any{"command": "go vet ./..."}}),
			},
			close: true,
			want:  drawn{title: "go vet ./..."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			tr.HandleToolCall(t.Context(), chatID, mustJSON(t, tt.frames[0]), FrameAttribution{})
			for _, f := range tt.frames[1:] {
				tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, f), FrameAttribution{})
			}
			turn := deps.turns.chats[chatID]
			if tt.close {
				sealed, err := turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCancelled})
				if err != nil {
					t.Fatal(err)
				}
				PublishSealed(t.Context(), deps, chatID, "", sealed)
			}

			live, reloaded := liveCards(t, *events), reloadedCards(t, deps.turns.entriesOf(turn))
			if !reflect.DeepEqual(live, reloaded) {
				t.Errorf("live cards %+v, reloaded %+v", live, reloaded)
			}
			if got := live["tc-1"]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("the card draws %+v, want %+v", got, tt.want)
			}
		})
	}
}

func shellCreate(input map[string]any) map[string]any {
	return map[string]any{"toolCallId": "tc-1", "title": "Run Command", "kind": "execute", "status": "pending", "rawInput": input}
}

// update is tc-1's tool_call_update frame with status and fields.
func update(status string, fields map[string]any) map[string]any {
	out := map[string]any{"toolCallId": "tc-1", "status": status}
	maps.Copy(out, fields)
	return out
}
