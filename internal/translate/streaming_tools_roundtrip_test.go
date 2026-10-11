package translate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

// applyDelta is the wire contract as a SPECIFICATION: omitted means unchanged, output
// appends unless replaced, diffs append, everything else is assigned. It mirrors the
// client fold in static-src/store.ts, the only consumer a delta has.
func applyDelta(before marotte.ToolCall, d *marotte.ToolProgressPayload) marotte.ToolCall {
	out := before
	if d.Title != "" {
		out.Title = d.Title
	}
	if d.Kind != "" {
		out.Kind = d.Kind
	}
	if d.Status != "" {
		out.Status = d.Status
	}
	switch {
	case d.OutputReplace:
		out.Output = d.OutputDelta
	case d.OutputDelta != "":
		out.Output = before.Output + d.OutputDelta
	}
	if len(d.OutputSpans) > 0 {
		out.OutputSpans = d.OutputSpans
	}
	if len(d.DiffsAppended) > 0 {
		out.Diffs = append(append([]marotte.ToolDiff(nil), before.Diffs...), d.DiffsAppended...)
	}
	if len(d.Locations) > 0 {
		out.Locations = d.Locations
	}
	if d.DurationMs != 0 {
		out.DurationMs = d.DurationMs
	}
	if d.TerminalID != "" {
		out.TerminalID = d.TerminalID
	}
	if d.AgentSubtaskID != "" {
		out.AgentSubtaskID = d.AgentSubtaskID
	}
	if d.WorkflowID != "" {
		out.WorkflowID = d.WorkflowID
	}
	if d.Checkpoint != nil {
		out.Checkpoint = d.Checkpoint
	}
	if d.Disclosed != nil {
		out.Disclosed = d.Disclosed
	}
	if d.Denial != nil {
		out.Denial = d.Denial
	}
	if d.Offload != nil {
		out.Offload = d.Offload
	}
	if d.Declined {
		out.Declined = true
	}
	return out
}

// Each step's frame must reconstruct that step's result. Steps are only folds
// applyToolCallUpdate performs: `omitempty` cannot express a reset to zero, so a
// generator that blanks a field would falsify the property spuriously.
func TestToolCallDelta_RoundTripsOverAFoldSequence(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		tc := marotte.ToolCall{
			ID:     "call-1",
			Title:  "Reading",
			Kind:   marotte.ToolKindRead,
			Status: marotte.ToolPending,
		}
		for range rapid.IntRange(1, 20).Draw(rt, "steps") {
			before := tc
			foldStep(rt, &tc)
			d := toolProgress("t-1", &before, &tc)
			got := applyDelta(before, &d)
			if !sameToolCall(&got, &tc) {
				t.Fatalf("round trip lost a change\nbefore = %+v\ndelta  = %+v\nfolded = %+v\nwant   = %+v",
					before, d, got, tc)
			}
			// A frame that names no call gives the client nothing to apply it to.
			if d.ToolCallID != tc.ID || d.Turn != "t-1" {
				t.Fatalf("delta address = (%q, %q), want (%q, %q)",
					d.Turn, d.ToolCallID, "t-1", tc.ID)
			}
		}
	})
}

func foldStep(rt *rapid.T, tc *marotte.ToolCall) {
	switch rapid.IntRange(0, 9).Draw(rt, "step") {
	case 0:
		// The commonest frame: a status transition and nothing else.
		tc.Status = rapid.SampledFrom([]marotte.ToolStatus{
			marotte.ToolPending, marotte.ToolInProgress,
			marotte.ToolCompleted, marotte.ToolFailed,
		}).Draw(rt, "status")
	case 1:
		tc.Output += rapid.StringN(1, 40, 40).Draw(rt, "chunk")
	case 2:
		// adoptTerminalOutput replaces the fragments wholesale: the one non-append fold.
		tc.Output = rapid.StringN(0, 40, 40).Draw(rt, "terminalOutput")
	case 3:
		tc.Diffs = append(tc.Diffs, marotte.ToolDiff{
			Path:    rapid.StringMatching(`[a-z]{1,8}\.go`).Draw(rt, "diffPath"),
			NewText: rapid.StringN(0, 20, 20).Draw(rt, "newText"),
		})
	case 4:
		tc.Locations = []marotte.ToolLocation{{
			Path: rapid.StringMatching(`[a-z]{1,8}\.go`).Draw(rt, "locPath"),
			Line: rapid.IntRange(1, 500).Draw(rt, "locLine"),
		}}
	case 5:
		tc.OutputSpans = []marotte.TextSpan{{
			Start: 0,
			End:   rapid.IntRange(1, 20).Draw(rt, "spanEnd"),
			Attrs: uint16(rapid.IntRange(1, 8).Draw(rt, "spanAttrs")),
		}}
	case 6:
		// Adopted once, so a step finding one already set changes nothing; worth generating.
		id := rapid.StringMatching(`[a-z]{4,8}`).Draw(rt, "attachID")
		switch rapid.IntRange(0, 2).Draw(rt, "which") {
		case 0:
			if tc.TerminalID == "" {
				tc.TerminalID = id
			}
		case 1:
			if tc.AgentSubtaskID == "" {
				tc.AgentSubtaskID = id
			}
		case 2:
			if tc.WorkflowID == "" {
				tc.WorkflowID = id
			}
		}
	case 7:
		if tc.Checkpoint == nil {
			tc.Checkpoint = &marotte.ToolCheckpoint{
				Original: rapid.StringMatching(`[a-z]{1,8}`).Draw(rt, "cpOriginal"),
			}
		}
	case 8:
		if tc.DurationMs == 0 {
			tc.DurationMs = rapid.IntRange(1, 100_000).Draw(rt, "durationMs")
		}
	case 9:
		// One-way: a step finding it already set proves the fold never carries a clear.
		tc.Declined = true
	}
}

// sameToolCall compares by JSON, treating nil and an empty slice as equal like the wire.
func sameToolCall(a, b *marotte.ToolCall) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// deltaFixture is one cross-language case: the value the fold started from, the
// frame the server sends, and the value a client must end up holding.
type deltaFixture struct {
	Name   string                      `json:"name"`
	Before marotte.ToolCall            `json:"before"`
	Delta  marotte.ToolProgressPayload `json:"delta"`
	After  marotte.ToolCall            `json:"after"`
}

// The builder is pinned against the cases static-src/tool-call-delta.node.test.ts drives
// the client fold with, so neither language owns a private table.
func TestToolCallDelta_SharedFixture(t *testing.T) {
	path := filepath.Join("testdata", "tool_call_delta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	var fx struct {
		Cases []deltaFixture `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("Setup: parse %s: %v", path, err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture carries no cases; an empty table would pass forever")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := toolProgress(c.Delta.Turn, &c.Before, &c.After)
			wantJSON, _ := json.Marshal(c.Delta)
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Setup: marshal delta: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("toolProgress(%q, before, after) = %s, want %s",
					c.Delta.Turn, gotJSON, wantJSON)
			}
			// A fixture whose `after` does not follow from `before` + `delta`
			// fails here rather than teaching the client half a wrong expectation.
			if folded := applyDelta(c.Before, &c.Delta); !sameToolCall(&folded, &c.After) {
				t.Errorf("applyDelta(before, delta) = %+v, want %+v", folded, c.After)
			}
		})
	}
}
