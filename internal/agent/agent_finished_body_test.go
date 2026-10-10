package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// emit() clears the chat's status as turn_closed goes out, so the push must not read it at the push site.
func TestPushTurnOutcome_PushBodyCarriesAgentText(t *testing.T) {
	cs := newTestChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(context.Background(), "/tmp/push-desc", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	id, log := h.stagePromptTurn(t, "c1")
	sayText(t, log)
	// Broadcast mid-turn through the production write: only that path stages the description on the open turn.
	h.Broadcast(ctx, marotte.NewEvent(marotte.EventChatStatus, "c1", marotte.ChatStatusPayload{
		Status:      "in_progress",
		Description: "Wiring the PR status poller",
	}))
	resp := &marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", id, 0, resp)

	select {
	case body := <-fp.sends:
		if body != "Response complete · Wiring the PR status poller" {
			t.Errorf("push body = %q, want the agent's own line", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}

	// Still cleared afterwards, or a later connect reports a finished turn's label as current.
	if got := h.bus.chatStatus.snapshot()["c1"]; got.Description != "" {
		t.Errorf("the chat status survived turn end: %+v", got)
	}
}

// A failed turn must not push the agent's description. Bodies are hardcoded: an
// expectation computed by the code under test passes for any mapping.
func TestPushTurnOutcome_PushReadsTheSeverity(t *testing.T) {
	cases := []struct {
		name   string
		stop   marotte.StopReason
		silent bool   // the turn seals nothing, so end_turn grades empty
		want   string // "" means no push at all
	}{
		{name: "clean", stop: marotte.StopReasonEndTurn, want: "Response complete · Wiring the PR status poller"},
		{name: "failed", stop: marotte.StopReasonError, want: "Turn failed · The agent reported an error and the turn stopped."},
		{name: "refused", stop: marotte.StopReasonRefusal, want: "Turn failed · The model declined to continue."},
		// STOPPED: the reader asked for the cancel, and an unreadable end claims nothing.
		{name: "cancelled", stop: marotte.StopReasonCancelled, want: ""},
		{name: "unknown", stop: marotte.StopReasonUnknown, want: ""},
		// STOPPED too: the push reads the narrowed conclusion, so a silent end_turn is `empty`.
		{name: "empty", stop: marotte.StopReasonEndTurn, silent: true, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChatStore()
			fp := &recordingPush{sends: make(chan string, 4)}
			h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
			cs.wire(h)
			ctx := t.Context()
			_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			id, log := h.stagePromptTurn(t, "c1")
			if !tc.silent {
				sayText(t, log)
			}
			// Present for every case, so an arm leaking it fails visibly.
			h.Broadcast(ctx, marotte.NewEvent(marotte.EventChatStatus, "c1", marotte.ChatStatusPayload{
				Status: "in_progress", Description: "Wiring the PR status poller",
			}))
			h.SettleTurnOnResponse(ctx, "c1", id, 0,
				&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": string(tc.stop)})})

			if tc.want == "" {
				joinInflight(t, h)
				select {
				case body := <-fp.sends:
					t.Errorf("a %q turn pushed %q; a turn that merely stopped reports nothing", tc.stop, body)
				default:
				}
				return
			}
			select {
			case body := <-fp.sends:
				if body != tc.want {
					t.Errorf("push body = %q, want %q", body, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("a %q turn pushed nothing", tc.stop)
			}
		})
	}
}

// TestPushBody_CarriesOnlyThisTurnsDescription pins that the body is the description
// declared during the ending turn, not a retained claim from the previous one. Turn N+1 is
// wire-started: a prompt-class source would empty the cache and the case could never go red.
func TestPushBody_CarriesOnlyThisTurnsDescription(t *testing.T) {
	newFixture := func(t *testing.T) (*Runtime, *recordingPush) {
		t.Helper()
		cs := newTestChatStore()
		fp := &recordingPush{sends: make(chan string, 4)}
		h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
		cs.wire(h)
		_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
		return h, fp
	}
	awaitBody := func(t *testing.T, fp *recordingPush) string {
		t.Helper()
		select {
		case body := <-fp.sends:
			return body
		case <-time.After(2 * time.Second):
			t.Fatal("no push sent for a clean turn")
			return ""
		}
	}

	t.Run("the declaring turn gets its own words", func(t *testing.T) {
		h, fp := newFixture(t)
		id, log := h.stagePromptTurn(t, "c1")
		sayText(t, log)
		h.Broadcast(t.Context(), marotte.NewEvent(marotte.EventChatStatus, "c1", marotte.ChatStatusPayload{
			Status:      marotte.ChatStatusWaitingOnUser,
			Description: "waiting on the user to disposition both proposals",
		}))
		endTurn(t, h, "c1", id)

		if got := awaitBody(t, fp); got != "Response complete · waiting on the user to disposition both proposals" {
			t.Errorf("push body = %q, want the description this turn declared", got)
		}
	})

	t.Run("the next turn does not inherit it", func(t *testing.T) {
		h, fp := newFixture(t)
		id, log := h.stagePromptTurn(t, "c1")
		sayText(t, log)
		h.Broadcast(t.Context(), marotte.NewEvent(marotte.EventChatStatus, "c1", marotte.ChatStatusPayload{
			Status:      marotte.ChatStatusWaitingOnUser,
			Description: "waiting on the user to disposition both proposals",
		}))
		endTurn(t, h, "c1", id)
		_ = awaitBody(t, fp)
		// The claim is retained past turn end by design, so the cache still holds a description turn N+1 never declared.
		if got := h.bus.chatStatus.snapshot()["c1"]; got.Status != marotte.ChatStatusWaitingOnUser {
			t.Fatalf("the fixture lost the retention: status is %q", got.Status)
		}

		wire := h.stageWireTurn(t, "c1")
		if wire == nil {
			t.Fatal("the fixture could not open a wire-started turn")
		}
		sayText(t, wire)
		h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)

		if got := awaitBody(t, fp); got != "Response complete" {
			t.Errorf("push body = %q, want %q: this turn declared nothing", got, "Response complete")
		}
	})
}

// A chat notification travels as a chat SUBJECT, or it loses the per-chat coalescing tag.
func TestPushTurnOutcome_PushSubjectIsTheChat(t *testing.T) {
	cs := newTestChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(context.Background(), "/tmp/push-subject", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	id, log := h.stagePromptTurn(t, "c1")
	sayText(t, log)
	resp := &marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", id, 0, resp)

	select {
	case <-fp.sends:
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}
	if fp.subject.ChatID != "c1" {
		t.Errorf("subject chat id = %q, want %q", fp.subject.ChatID, "c1")
	}
	if fp.subject.Key != "" {
		t.Errorf("a chat notification carries a subject key %q; the chat id IS its subject", fp.subject.Key)
	}
}
