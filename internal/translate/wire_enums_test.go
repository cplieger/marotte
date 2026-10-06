package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// Values KAS does not send today, so a passing case proves the door's gate.
const (
	unknownToolKind   = "quantum_tunnel"
	unknownToolStatus = "queued"
	unknownPlanStatus = "skipped"
)

// TestHandleToolCall_AnUnknownKindOrStatusIsNormalisedAtTheDoor pins that a create frame's
// kind and status reach the entry log inside marotte's own sets.
func TestHandleToolCall_AnUnknownKindOrStatusIsNormalisedAtTheDoor(t *testing.T) {
	cases := []struct {
		name       string
		frame      map[string]any
		wantKind   marotte.ToolKind
		wantStatus marotte.ToolStatus
	}{
		{
			name:       "UnknownValuesAreMappedIn",
			frame:      map[string]any{"toolCallId": "tc-1", "title": "readFile", "kind": unknownToolKind, "status": unknownToolStatus},
			wantKind:   marotte.ToolKindOther,
			wantStatus: marotte.ToolInProgress,
		},
		{
			name:       "AbsentValuesTakeACPsDefaults",
			frame:      map[string]any{"toolCallId": "tc-1", "title": "readFile"},
			wantKind:   marotte.ToolKindOther,
			wantStatus: marotte.ToolPending,
		},
		{
			name:       "KnownValuesPassThrough",
			frame:      map[string]any{"toolCallId": "tc-1", "title": "readFile", "kind": "read", "status": "in_progress"},
			wantKind:   marotte.ToolKindRead,
			wantStatus: marotte.ToolInProgress,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			const chatID marotte.ChatID = "c1"
			tr.HandleToolCall(t.Context(), chatID, mustJSON(t, tc.frame), FrameAttribution{})
			calls := toolCallsOf(t, deps.chatEntries(chatID))
			if len(calls) != 1 {
				t.Fatalf("sealed tool_call entries = %d, want 1", len(calls))
			}
			if calls[0].Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", calls[0].Kind, tc.wantKind)
			}
			if calls[0].Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", calls[0].Status, tc.wantStatus)
			}
		})
	}
}

// TestToolCallUpdate_AnUnknownStatusKeepsTheCallOpen pins that an unrecognized update
// status is in_progress, left for the close's abort rule.
func TestToolCallUpdate_AnUnknownStatusKeepsTheCallOpen(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"kind":       unknownToolKind,
		"status":     unknownToolStatus,
	}), FrameAttribution{})
	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if tc.Status != marotte.ToolInProgress {
		t.Errorf("status = %q, want %q", tc.Status, marotte.ToolInProgress)
	}
	if tc.Kind != marotte.ToolKindOther {
		t.Errorf("kind = %q, want %q", tc.Kind, marotte.ToolKindOther)
	}
}

// TestToolCallUpdate_AnAbsentStatusStillMeansUnchanged falsifies the gate's empty clause:
// a substituted default would write pending over an in-flight call.
func TestToolCallUpdate_AnAbsentStatusStillMeansUnchanged(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile (retitled)",
	}), FrameAttribution{})
	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if tc.Status != marotte.ToolPending {
		t.Errorf("status = %q, want %q (the create's status, unchanged)", tc.Status, marotte.ToolPending)
	}
	if tc.Kind != marotte.ToolKindRead {
		t.Errorf("kind = %q, want %q (the create's kind, unchanged)", tc.Kind, marotte.ToolKindRead)
	}
}

// TestHandlePlan_AnUnknownEntryStatusIsPending pins that an unknown plan row status
// under-claims as pending.
func TestHandlePlan_AnUnknownEntryStatusIsPending(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	const chatID marotte.ChatID = "c1"
	tr.HandlePlan(t.Context(), chatID, mustJSON(t, map[string]any{
		"entries": []map[string]any{
			{"content": "step one", "priority": "high", "status": unknownPlanStatus},
			{"content": "step two", "priority": "low", "status": "completed"},
		},
	}), FrameAttribution{})
	plans := plansOf(t, deps.chatEntries(chatID))
	if len(plans) != 1 || len(plans[0].Entries) != 2 {
		t.Fatalf("projected plans = %+v, want one plan of two entries", plans)
	}
	if got := plans[0].Entries[0].Status; got != marotte.PlanPending {
		t.Errorf("unknown status = %q, want %q", got, marotte.PlanPending)
	}
	if got := plans[0].Entries[1].Status; got != marotte.PlanCompleted {
		t.Errorf("known status = %q, want %q", got, marotte.PlanCompleted)
	}
}

// TestHandlePermissionRequest_AnUnknownToolKindIsOther pins the permission door's kind.
func TestHandlePermissionRequest_AnUnknownToolKindIsOther(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(7)
	tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall":  map[string]any{"toolCallId": "tc-9", "title": "Write config.tf", "kind": unknownToolKind},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		}),
	})
	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.Kind != marotte.ToolKindOther {
		t.Errorf("kind = %q, want %q", got.Kind, marotte.ToolKindOther)
	}
}

// TestEntryProjection_AnUnknownKindOrStatusIsNormalisedAtTheDoor pins the replay door: a
// log holding a kind this build does not know must still open.
func TestEntryProjection_AnUnknownKindOrStatusIsNormalisedAtTheDoor(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		pair(marotte.ACPUpdateToolCall, mustJSON(t, map[string]any{
			"sessionUpdate": string(marotte.ACPUpdateToolCall),
			"toolCallId":    "call-1",
			"title":         "Run Command",
			"kind":          unknownToolKind,
			"status":        unknownToolStatus,
			"_meta": map[string]any{"kiro": map[string]any{
				"replay":    true,
				"messageId": "call-1-call",
				"timestamp": "2026-09-15T12:00:02.000Z",
			}},
		})),
		turnEndFrame(t, "end_turn"),
	})
	call := entryOfKind(t, turns[0], marotte.EntryKindToolCall)
	var payload marotte.EntryToolCall
	payloadOf(t, call, &payload)
	if payload.Kind != marotte.ToolKindOther {
		t.Errorf("kind = %q, want %q", payload.Kind, marotte.ToolKindOther)
	}
	if payload.Status != marotte.ToolInProgress {
		t.Errorf("status = %q, want %q", payload.Status, marotte.ToolInProgress)
	}
}
