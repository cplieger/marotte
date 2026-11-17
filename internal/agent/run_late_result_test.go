package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

type frameLog struct {
	mu     sync.Mutex
	events []marotte.ServerEvent
}

func (f *frameLog) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
}

// appendedResults is every tool_result the frames carried as entry_appended.
func (f *frameLog) appendedResults(t *testing.T) []marotte.EntryToolResult {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []marotte.EntryToolResult
	for _, evt := range f.events {
		p, ok := evt.Payload.(marotte.EntryAppendedPayload)
		if !ok || p.Entry.Kind != marotte.EntryKindToolResult {
			continue
		}
		var r marotte.EntryToolResult
		if err := json.Unmarshal(p.Entry.Payload, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// A step's result KAS sends after its turn closed is announced titled from the call the close aborted, as a
// reload of the run log titles it.
func TestRunAppendAfterClosed_TitlesALateResultFromItsCall(t *testing.T) {
	step := workflow.PathKey([]string{"wf1", "build"})
	tests := []struct {
		name       string
		call       marotte.EntryToolCall
		res        marotte.EntryToolResult
		wantTitle  string
		wantLoaded []marotte.LoadedResource
	}{
		{
			name: "a placeholder shell call whose command only the result holds",
			call: marotte.EntryToolCall{ID: "call-1", Title: "Run Command", Kind: marotte.ToolKindExecute, Input: json.RawMessage(`{}`)},
			res: marotte.EntryToolResult{
				Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolCompleted,
				Input: json.RawMessage(`{"command":"go vet ./..."}`),
			},
			wantTitle: "go vet ./...",
		},
		{
			name: "a load call whose tool name only the call holds",
			call: marotte.EntryToolCall{
				ID: "call-1", Title: "Tool Load", Kind: marotte.ToolKindOther, ToolName: "tool_load",
				Input: json.RawMessage(`{"id":"skill::land"}`),
			},
			res:        marotte.EntryToolResult{Title: "Tool Load", Kind: marotte.ToolKindOther, Status: marotte.ToolCompleted},
			wantTitle:  "Loaded skill: land",
			wantLoaded: []marotte.LoadedResource{{ID: "skill::land", Type: marotte.LoadedSkill, Name: "land"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunLog(t.TempDir())
			turn, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: step}, "c-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := turn.ToolCall(t.Context(), "", &tt.call); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.closeNode(t.Context(), "wf1", step, "cancelled", "", nil); err != nil {
				t.Fatal(err)
			}
			frames := &frameLog{}
			rs := &Runs{log: r, bus: frames}
			late := marotte.Entry{Kind: marotte.EntryKindToolResult, ID: marotte.ToolResultID("call-1")}
			raw, err := json.Marshal(tt.res)
			if err != nil {
				t.Fatal(err)
			}
			late.Payload = raw

			if err := rs.RunAppendAfterClosed(t.Context(), "wf1", step, &late); err != nil {
				t.Fatal(err)
			}

			got := frames.appendedResults(t)
			if len(got) != 1 {
				t.Fatalf("%d tool_result frames, want the late result's one", len(got))
			}
			if got[0].CardTitle != tt.wantTitle || !reflect.DeepEqual(got[0].Loaded, tt.wantLoaded) {
				t.Errorf("late frame card_title %q loaded %+v, want %q %+v", got[0].CardTitle, got[0].Loaded, tt.wantTitle, tt.wantLoaded)
			}
			reloaded := runLogEntries(t, r, "wf1")
			marotte.ServeToolEntries(reloaded, nil)
			last := reloaded[len(reloaded)-1]
			var res marotte.EntryToolResult
			if err := json.Unmarshal(last.Payload, &res); err != nil {
				t.Fatal(err)
			}
			if res.CardTitle != got[0].CardTitle {
				t.Errorf("reloaded late result card_title %q, live %q", res.CardTitle, got[0].CardTitle)
			}
		})
	}
}
