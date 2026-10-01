package agent

// decision_settled is what closes a card on the surfaces that did NOT answer.
// Two things are pinned: the winning claim announces itself with the kind and
// the attribution intact, and a losing claim announces nothing — an event per
// rejected attempt would retire cards for a decision that attempt never settled.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/sse"
	"github.com/cplieger/marotte/internal/marotte"
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

// TestTakePendingPerm_LosingClaimAnnouncesNothing: the second tab's attempt
// settles nothing, so it must not tell every surface that the card is closed —
// and an unanswered request is exactly the one that has to stay on screen.
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
