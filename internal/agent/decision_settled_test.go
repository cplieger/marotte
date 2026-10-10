package agent

// A claim's delivered answer announces decision_settled with kind and attribution; a losing claim announces nothing.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/sse"
)

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

func TestTakePendingPerm_AnnouncesTheDeliveredDecision(t *testing.T) {
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
			askID := requestIDOf(t, h.bus.pendingPerms.add(9, marotte.NewEvent(tc.event, "c1", marotte.PermissionNeededPayload{}), newFakeBridge()))

			reply, ok := h.bus.TakePendingPerm("c1", askID, tc.settledBy)
			if !ok {
				t.Fatal("TakePendingPerm refused a pending request")
			}
			if got := settledEvents(t, bufferedSince(h, head)); len(got) != 0 {
				t.Fatalf("a claim announced %+v before its answer was written", got)
			}
			if outcome, err := reply.Respond(t.Context(), nil); outcome != command.AnswerDelivered || err != nil {
				t.Fatalf("Respond = (%v, %v), want (AnswerDelivered, nil)", outcome, err)
			}

			got := settledEvents(t, bufferedSince(h, head))
			if len(got) != 1 {
				t.Fatalf("emitted %d decision_settled events, want 1", len(got))
			}
			want := marotte.DecisionSettledPayload{RequestID: askID, Kind: tc.wantKind, SettledBy: tc.settledBy}
			if got[0] != want {
				t.Errorf("payload = %+v, want %+v", got[0], want)
			}
		})
	}
}

// TestTakePendingPerm_LosingClaimAnnouncesNothing pins that an unanswered request must stay on screen.
func TestTakePendingPerm_LosingClaimAnnouncesNothing(t *testing.T) {
	h, _, _ := newTestHub()
	askID := requestIDOf(t, h.bus.pendingPerms.add(9, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{}), newFakeBridge()))
	reply, ok := h.bus.TakePendingPerm("c1", askID, marotte.SettledByUser)
	if !ok {
		t.Fatal("first claim refused")
	}
	if outcome, err := reply.Respond(t.Context(), nil); outcome != command.AnswerDelivered || err != nil {
		t.Fatalf("Respond = (%v, %v), want (AnswerDelivered, nil)", outcome, err)
	}

	head := h.bus.fanout.Position().Head
	if _, ok := h.bus.TakePendingPerm("c1", askID, marotte.SettledByUser); ok {
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
	h.bus.pendingPerms.add(3, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{ToolCallID: "tc"}), nil)
	newest := requestIDOf(t, h.bus.pendingPerms.add(5, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{ToolCallID: "tc"}), nil))
	h.bus.pendingPerms.add(5, marotte.NewEvent(marotte.EventPermissionNeeded, "c2",
		marotte.PermissionNeededPayload{ToolCallID: "tc"}), nil)

	if !h.bus.PendingPermsWithdraw("c1", "tc") {
		t.Fatal("PendingPermsWithdraw(c1, tc) = false, want true")
	}
	got := settledEvents(t, bufferedSince(h, head))
	if len(got) != 1 || got[0].RequestID != newest || got[0].SettledBy != marotte.SettledByMoot ||
		got[0].Kind != marotte.DecisionKindPermission {
		t.Fatalf("decision_settled = %+v, want one {permission, ask %d, moot}", got, newest)
	}
	var left []int64
	for _, e := range h.bus.pendingPerms.list("") {
		left = append(left, e.Payload.(marotte.PermissionNeededPayload).RequestID)
	}
	if len(left) != 2 {
		t.Errorf("pending after withdraw = %v, want c1's older round and c2's ask", left)
	}
	if h.bus.PendingPermsWithdraw("c1", "other") {
		t.Error("PendingPermsWithdraw(c1, other) = true for a tool call nothing asked about")
	}
}

// A settled ask retracts its held push under the subject it was pushed under: a step's ask is
// the run's, whichever chat's bridge carried it.
func TestTakePendingPerm_RetractsTheAsksOwnSubject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chatID  marotte.ChatID
		payload any
		want    marotte.PushSubject
	}{
		{"a chat's ask", "c1", marotte.PermissionNeededPayload{}, marotte.ChatSubject("c1")},
		{"a step's ask on its parent chat", "c1", marotte.UserInputNeededPayload{RunID: "wf1"}, marotte.RunSubject("wf1")},
		{"an ask on a parentless run's bridge", "run:wf2", marotte.ElicitationNeededPayload{}, marotte.RunSubject("wf2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChatStore()
			fp := &recordingPush{sends: make(chan string, 4)}
			h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
			cs.wire(h)
			askID := requestIDOf(t, h.bus.pendingPerms.add(9, marotte.ServerEvent{Type: marotte.EventPermissionNeeded, ChatID: tc.chatID, Payload: tc.payload}, newFakeBridge()))
			reply, ok := h.bus.TakePendingPerm(tc.chatID, askID, marotte.SettledByUser)
			if !ok {
				t.Fatal("TakePendingPerm refused a pending request")
			}
			if outcome, err := reply.Respond(t.Context(), nil); outcome != command.AnswerDelivered || err != nil {
				t.Fatalf("Respond = (%v, %v), want (AnswerDelivered, nil)", outcome, err)
			}
			fp.mu.Lock()
			got := slices.Clone(fp.retracted)
			fp.mu.Unlock()
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("retracted %+v, want [%+v]", got, tc.want)
			}
		})
	}
}
