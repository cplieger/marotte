package turnlog

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// resultSeal is the one seal of a tool_result among sealed, failing when there is none or several.
func resultSeal(t *testing.T, sealed []Sealed) Sealed {
	t.Helper()
	var out []Sealed
	for _, s := range sealed {
		if s.Entry != nil && s.Entry.Kind == marotte.EntryKindToolResult {
			out = append(out, s)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d tool_result seals, want 1", len(out))
	}
	return out[0]
}

var storedShell = marotte.EntryToolCall{
	ID: "c1", Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolPending, Input: json.RawMessage(`{}`),
}

// A tool_result's seal carries the call it settles as its tool_call entry stored it, so a carrier
// titles the result from the same pair a reload joins.
func TestToolResult_TheSealCarriesTheStoredCall(t *testing.T) {
	turn, rec := open(t)
	for _, s := range rec.seal(turn.ToolCall(t.Context(), "", &storedShell)) {
		if s.Call != nil {
			t.Errorf("the tool_call's seal carries a call %+v, want none", s.Call)
		}
	}
	folded := marotte.ToolCallOfEntry(&storedShell)
	folded.Input = json.RawMessage(`{"command":"go vet ./..."}`)
	turn.SetOpenCall("c1", &folded)

	s := resultSeal(t, rec.seal(turn.ToolResult(t.Context(), "", "c1", &marotte.EntryToolResult{Status: marotte.ToolCompleted})))
	if s.Call == nil || !reflect.DeepEqual(*s.Call, storedShell) {
		t.Errorf("the result's seal carries call %+v, want the stored %+v", s.Call, storedShell)
	}
}

// A close's abort keeps what the card drew of the fold, the fields its title reads, so a reload titles the
// aborted card as the live one was, and its seal carries the stored call.
func TestClose_AnAbortKeepsTheFoldsTitleFields(t *testing.T) {
	disclosed := &marotte.ToolDisclosed{Type: "skill", DisplayName: "land"}
	tests := []struct {
		name string
		fold func(*marotte.ToolCall)
		want marotte.EntryToolResult
	}{
		{
			name: "a revised input, retitle and disclosure",
			fold: func(c *marotte.ToolCall) {
				c.Title, c.Kind, c.Input, c.Disclosed = "Reading file", marotte.ToolKindRead, json.RawMessage(`{"path":"a.go"}`), disclosed
			},
			want: marotte.EntryToolResult{
				Title: "Reading file", Kind: marotte.ToolKindRead, Status: marotte.ToolAborted,
				Input: json.RawMessage(`{"path":"a.go"}`), Disclosed: disclosed,
			},
		},
		{
			name: "an input the create already sent",
			fold: func(*marotte.ToolCall) {},
			want: marotte.EntryToolResult{Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolAborted},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn, rec := open(t)
			rec.seal(turn.ToolCall(t.Context(), "", &storedShell))
			folded := marotte.ToolCallOfEntry(&storedShell)
			tt.fold(&folded)
			turn.SetOpenCall("c1", &folded)

			s := resultSeal(t, rec.seal(turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCancelled})))
			var got marotte.EntryToolResult
			if err := json.Unmarshal(s.Entry.Payload, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("aborted result = %+v, want %+v", got, tt.want)
			}
			if s.Call == nil || !reflect.DeepEqual(*s.Call, storedShell) {
				t.Errorf("the abort's seal carries call %+v, want the stored %+v", s.Call, storedShell)
			}
		})
	}
}
