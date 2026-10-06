package agent

// The winning claim announces decision_settled with kind and attribution; a losing claim announces nothing.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/sse"
)

// settledEvents decodes the decision_settled payloads emitted after sinceID.
func settledEvents(t *testing.T, events []sse.ReplayEvent) []marotte.DecisionSettledPayload {
	t.Helper()
	var out []marotte.DecisionSettledPayload
	for _, e := range events {
		var envelope struct {
			Type    marotte.EventType              `json:"type"`
			ChatID  marotte.ChatID                 `json:"chat_id"`
			Payload marotte.DecisionSettledPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &envelope); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if envelope.Type != marotte.EventDecisionSettled {
			continue
		}
		if envelope.ChatID != "c1" {
			t.Errorf("event chat_id = %q, want c1 (the chat the ask was raised on)", envelope.ChatID)
		}
		out = append(out, envelope.Payload)
	}
	return out
}

func TestTakePendingPerm_AnnouncesTheSettledDecision(t *testing.T) {
	cases := []struct {
		name      string
		event     marotte.EventType
		wantKind  marotte.DecisionKind
		settledBy marotte.SettledBy
	}{
		{
			name:      "permission answered by a person",
			event:     marotte.EventPermissionNeeded,
			wantKind:  marotte.DecisionKindPermission,
			settledBy: marotte.SettledByUser,
		},
		{
			name:      "elicitation answered by a person",
			event:     marotte.EventElicitationNeeded,
			wantKind:  marotte.DecisionKindElicitation,
			settledBy: marotte.SettledByUser,
		},
		{
			// The kind travels from the TRACKED event, so a question and a
			// permission cannot be reported as each other.
			name:      "question answered by the unattended floor",
			event:     marotte.EventUserInputNeeded,
			wantKind:  marotte.DecisionKindUserInput,
			settledBy: marotte.SettledByUnattended,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			head := h.bus.fanout.Position().Head
			h.bus.pendingPerms.Add(9, marotte.NewEvent(tc.event, "c1", marotte.PermissionNeededPayload{RequestID: 9}))

			if !h.bus.TakePendingPerm("c1", 9, tc.settledBy) {
				t.Fatal("TakePendingPerm refused a pending request")
			}

			got := settledEvents(t, bufferedSince(h, head))
			if len(got) != 1 {
				t.Fatalf("emitted %d decision_settled events, want 1", len(got))
			}
			want := marotte.DecisionSettledPayload{RequestID: 9, Kind: tc.wantKind, SettledBy: tc.settledBy}
			if got[0] != want {
				t.Errorf("payload = %+v, want %+v", got[0], want)
			}
		})
	}
}

// TestTakePendingPerm_LosingClaimAnnouncesNothing pins that an unanswered request must stay on screen.
func TestTakePendingPerm_LosingClaimAnnouncesNothing(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.pendingPerms.Add(9, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 9}))
	if !h.bus.TakePendingPerm("c1", 9, marotte.SettledByUser) {
		t.Fatal("first claim refused")
	}

	head := h.bus.fanout.Position().Head
	if h.bus.TakePendingPerm("c1", 9, marotte.SettledByUser) {
		t.Error("second claim on one request id succeeded, want refused")
	}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 0 {
		t.Errorf("a losing claim emitted %d events, want 0: %+v", len(got), got)
	}
}

// A withdrawn ask retires as moot, newest round only, in its own chat.
func TestPendingPermsWithdraw_RetiresTheChatsAskAsMoot(t *testing.T) {
	h, _, _ := newTestHub()
	head := h.bus.fanout.Position().Head
	h.bus.pendingPerms.Add(3, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 3, ToolCallID: "tc"}))
	h.bus.pendingPerms.Add(5, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 5, ToolCallID: "tc"}))
	h.bus.pendingPerms.Add(5, marotte.NewEvent(marotte.EventPermissionNeeded, "c2",
		marotte.PermissionNeededPayload{RequestID: 5, ToolCallID: "tc"}))

	if !h.bus.PendingPermsWithdraw("c1", "tc") {
		t.Fatal("PendingPermsWithdraw(c1, tc) = false, want true")
	}
	got := settledEvents(t, bufferedSince(h, head))
	if len(got) != 1 || got[0].RequestID != 5 || got[0].SettledBy != marotte.SettledByMoot ||
		got[0].Kind != marotte.DecisionKindPermission {
		t.Fatalf("decision_settled = %+v, want one {permission, request 5, moot}", got)
	}
	var left []int64
	for _, e := range h.bus.pendingPerms.List("") {
		left = append(left, e.Payload.(marotte.PermissionNeededPayload).RequestID)
	}
	if len(left) != 2 {
		t.Errorf("pending after withdraw = %v, want c1's round 3 and c2's 5", left)
	}
	if h.bus.PendingPermsWithdraw("c1", "other") {
		t.Error("PendingPermsWithdraw(c1, other) = true for a tool call nothing asked about")
	}
}
