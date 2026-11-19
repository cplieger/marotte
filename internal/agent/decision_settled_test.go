package agent

// A claim's delivered answer announces decision_settled with kind and attribution; a losing claim announces nothing.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
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
			askID := requestIDOf(t, h.bus.pendingPerms.add(9, "", marotte.NewEvent(tc.event, "c1", marotte.PermissionNeededPayload{}), newFakeBridge()))

			reply, ok := h.bus.TakePendingPerm("c1", askID, tc.wantKind, tc.settledBy)
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
	askID := requestIDOf(t, h.bus.pendingPerms.add(9, "", marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{}), newFakeBridge()))
	reply, ok := h.bus.TakePendingPerm("c1", askID, marotte.DecisionKindPermission, marotte.SettledByUser)
	if !ok {
		t.Fatal("first claim refused")
	}
	if outcome, err := reply.Respond(t.Context(), nil); outcome != command.AnswerDelivered || err != nil {
		t.Fatalf("Respond = (%v, %v), want (AnswerDelivered, nil)", outcome, err)
	}

	head := h.bus.fanout.Position().Head
	if _, ok := h.bus.TakePendingPerm("c1", askID, marotte.DecisionKindPermission, marotte.SettledByUser); ok {
		t.Error("second claim on one request id succeeded, want refused")
	}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 0 {
		t.Errorf("a losing claim emitted %d events, want 0: %+v", len(got), got)
	}
}

// A withdrawn ask retires as moot by the askId KAS named, in its own chat.
func TestPendingPermsWithdraw_RetiresTheChatsAskAsMoot(t *testing.T) {
	h, _, _ := newTestHub()
	head := h.bus.fanout.Position().Head
	named := requestIDOf(t, h.bus.pendingPerms.add(3, "pr-1", marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{ToolCallID: "tc"}), nil))
	h.bus.pendingPerms.add(5, "pr-1", marotte.NewEvent(marotte.EventPermissionNeeded, "c2",
		marotte.PermissionNeededPayload{ToolCallID: "tc"}), nil)

	if h.bus.PendingPermsWithdraw("c1", "pr-unknown") {
		t.Error("PendingPermsWithdraw(c1, pr-unknown) = true for an ask id nothing filed")
	}
	if h.bus.PendingPermsWithdraw("c1", "") {
		t.Error(`PendingPermsWithdraw(c1, "") = true, want an empty ask id to withdraw nothing`)
	}
	if !h.bus.PendingPermsWithdraw("c1", "pr-1") {
		t.Fatal("PendingPermsWithdraw(c1, pr-1) = false, want true")
	}
	got := settledEvents(t, bufferedSince(h, head))
	if len(got) != 1 || got[0].RequestID != named || got[0].SettledBy != marotte.SettledByMoot ||
		got[0].Kind != marotte.DecisionKindPermission {
		t.Fatalf("decision_settled = %+v, want one {permission, ask %d, moot}", got, named)
	}
	if left := h.bus.pendingPerms.list(""); len(left) != 1 || left[0].ChatID != "c2" {
		t.Errorf("pending after withdraw = %+v, want c2's ask alone", left)
	}
}

// Two consent rounds of one tool call are two asks: KAS's close of the dead round retires that
// round's card and leaves the live round's, whichever arrived last.
func TestPendingPermsWithdraw_RetiresTheAskKASNamed(t *testing.T) {
	h, _, _ := newTestHub()
	head := h.bus.fanout.Position().Head
	round1 := requestIDOf(t, h.bus.pendingPerms.add(3, "pr-1", marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{ToolCallID: "tc", ConsentRound: 1}), nil))
	round2 := requestIDOf(t, h.bus.pendingPerms.add(5, "pr-2", marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{ToolCallID: "tc", ConsentRound: 2}), nil))

	if !h.bus.PendingPermsWithdraw("c1", "pr-1") {
		t.Fatal("PendingPermsWithdraw(c1, pr-1) = false, want true")
	}
	got := settledEvents(t, bufferedSince(h, head))
	if len(got) != 1 || got[0].RequestID != round1 || got[0].SettledBy != marotte.SettledByMoot {
		t.Errorf("decision_settled = %+v, want round 1's ask %d moot", got, round1)
	}
	left := h.bus.pendingPerms.list("c1")
	if len(left) != 1 || left[0].Payload.(marotte.PermissionNeededPayload).RequestID != round2 {
		t.Errorf("pending after withdraw = %+v, want round 2's ask %d still listed", left, round2)
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
			h := newRuntimeOn(t.Context(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs, WithPush(fp))
			cs.wire(h)
			askID := requestIDOf(t, h.bus.pendingPerms.add(9, "", marotte.ServerEvent{Type: marotte.EventPermissionNeeded, ChatID: tc.chatID, Payload: tc.payload}, newFakeBridge()))
			reply, ok := h.bus.TakePendingPerm(tc.chatID, askID, marotte.DecisionKindPermission, marotte.SettledByUser)
			if !ok {
				t.Fatal("TakePendingPerm refused a pending request")
			}
			if outcome, err := reply.Respond(t.Context(), nil); outcome != command.AnswerDelivered || err != nil {
				t.Fatalf("Respond = (%v, %v), want (AnswerDelivered, nil)", outcome, err)
			}
			fp.mu.Lock()
			got := slices.Clone(fp.retracted)
			fp.mu.Unlock()
			if want := (retraction{subject: tc.want, ask: notice.AskKey(askID)}); len(got) != 1 || got[0] != want {
				t.Errorf("retracted %+v, want [%+v]", got, want)
			}
		})
	}
}
