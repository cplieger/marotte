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

// A call kiro-cli moved into a background job returned to the model at the move, so the live close seals it
// completed with its whole fold and the job's mark, never aborted: the job outlives the turn.
func TestClose_ABackgroundJobsCardIsSealedRunning(t *testing.T) {
	turn, rec := open(t)
	rec.seal(turn.ToolCall(t.Context(), "", &storedShell))
	folded := marotte.ToolCallOfEntry(&storedShell)
	folded.Status = marotte.ToolInProgress
	folded.Output = "Still running after 30s, so it moved to the background as job 7.\n"
	folded.Job = &marotte.BackgroundJob{ID: "7"}
	turn.SetOpenCall("c1", &folded)

	s := resultSeal(t, rec.seal(turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted})))
	var got marotte.EntryToolResult
	if err := json.Unmarshal(s.Entry.Payload, &got); err != nil {
		t.Fatal(err)
	}
	want := marotte.EntryToolResult{
		Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolCompleted,
		Output: folded.Output, Job: &marotte.BackgroundJob{ID: "7"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closed job card = %+v, want %+v", got, want)
	}
}

// A job's end files as an interjection: it seals no lane, so the reply streaming beside it keeps its entry.
func TestJobEnded_InterjectsWithoutSealing(t *testing.T) {
	turn, rec := open(t)
	rec.seal(turn.TextDelta(t.Context(), "", "say-1", "still "))
	sealed := rec.seal(turn.JobEnded(t.Context(), &marotte.EntryJobEnded{
		JobID: "7", End: marotte.BackgroundJobEnd{Status: marotte.BackgroundJobFailed, DurationMs: 5},
	}))
	if len(sealed) != 1 || sealed[0].Entry.Kind != marotte.EntryKindJobEnded || sealed[0].Entry.ID != marotte.JobEndedID("7") {
		t.Fatalf("seals = %+v, want only the job_ended", sealed)
	}
	if open := turn.OpenEntries(); len(open) != 1 || open[0].Text != "still " {
		t.Errorf("open entries = %+v, want the reply still open", open)
	}
}
