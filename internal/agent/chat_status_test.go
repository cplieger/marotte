package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/sse"
)

// Merge is MergeStamped without the stamp.
func (c *chatStatusCache) Merge(chatID marotte.ChatID, p marotte.ChatStatusPayload) marotte.ChatStatusPayload {
	merged, _ := c.MergeStamped(chatID, p)
	return merged
}

// Get returns a chat's last status.
func (c *chatStatusCache) Get(chatID marotte.ChatID) marotte.ChatStatusPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byChat[chatID]
}

// TestChatStatusCache covers the status_snapshot input no message or replay supplies.
func TestChatStatusCache(t *testing.T) {
	c := newChatStatusCache()

	if got := c.Get("nobody"); got.Status != "" {
		t.Errorf("unknown chat returned %q, want the zero payload", got.Status)
	}

	c.Merge("c1", marotte.ChatStatusPayload{Status: "in_progress", Description: "reading files"})
	got := c.Get("c1")
	if got.Status != "in_progress" || got.Description != "reading files" {
		t.Errorf("got %+v, want in_progress/reading files", got)
	}

	// Newest wins.
	c.Merge("c1", marotte.ChatStatusPayload{Status: "waiting_on_user", Description: "needs a decision"})
	if got := c.Get("c1"); got.Status != "waiting_on_user" {
		t.Errorf("got %q, want the latest status", got.Status)
	}

	// Cleared, so a later connect cannot report a finished turn's label.
	c.Clear("c1")
	if got := c.Get("c1"); got.Status != "" {
		t.Errorf("status %q survived the turn", got.Status)
	}

	// waiting_on_user is the one status ClearAtTurnEnd retains. It also ends when the user answers
	// a structured channel (internal/command/discharge.go's dischargeByAnswer rows).
	c.Merge("c2", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser, Description: "needs a decision"})
	c.ClearAtTurnEnd("c2")
	if got := c.Get("c2"); got.Status != marotte.ChatStatusWaitingOnUser || got.Description != "needs a decision" {
		t.Errorf("got %+v, want waiting_on_user retained whole past turn end", got)
	}
	// Every other status goes.
	c.Merge("c3", marotte.ChatStatusPayload{Status: "in_progress", Description: "reading files"})
	c.ClearAtTurnEnd("c3")
	if got := c.Get("c3"); got.Status != "" {
		t.Errorf("status %q survived turn end; only waiting_on_user is retained", got.Status)
	}

	// An empty chat id is ignored: global events carry no chat.
	c.Merge("", marotte.ChatStatusPayload{Status: "in_progress"})
	if got := c.Get(""); got.Status != "" {
		t.Error("an empty chat id was recorded")
	}

	// A both-empty payload must leave no entry; only Snapshot can tell.
	c.Merge("c4", marotte.ChatStatusPayload{Status: "in_progress", Description: "x"})
	c.Merge("c4", marotte.ChatStatusPayload{})
	if _, ok := c.Snapshot()["c4"]; ok {
		t.Error("a both-empty Merge left a phantom entry; Get cannot see one, so assert through Snapshot")
	}
	// A status with no description is a real declaration and stays.
	c.Merge("c5", marotte.ChatStatusPayload{Status: "in_progress"})
	if got := c.Get("c5"); got.Status != "in_progress" {
		t.Errorf("status-only Merge left %q, want in_progress", got.Status)
	}

	// ClearWaiting is narrower than Clear: it never deletes a status the running turn declared.
	c.Merge("c6", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser, Description: "needs a decision"})
	if !c.ClearWaiting("c6") {
		t.Error("ClearWaiting reported no claim for a retained waiting_on_user entry")
	}
	if got := c.Get("c6"); got.Status != "" {
		t.Errorf("status %q survived ClearWaiting", got.Status)
	}
	if c.ClearWaiting("c6") {
		t.Error("ClearWaiting reported a claim for a chat with no entry")
	}
	// An in_progress entry belongs to its turn.
	c.Merge("c7", marotte.ChatStatusPayload{Status: "in_progress", Description: "reading the parser"})
	if c.ClearWaiting("c7") {
		t.Error("ClearWaiting reported a claim for an in_progress entry")
	}
	if got := c.Get("c7"); got.Status != "in_progress" || got.Description != "reading the parser" {
		t.Errorf("got %+v, want the in_progress entry left whole", got)
	}
}

// TestDischargeWaiting_BroadcastsTheClear pins that other devices need the empty chat_status frame,
// published only when the claim actually went.
func TestDischargeWaiting_BroadcastsTheClear(t *testing.T) {
	t.Run("a retained claim is cleared and broadcast", func(t *testing.T) {
		rt, _, _ := newTestHub()
		rt.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{
			Status:      marotte.ChatStatusWaitingOnUser,
			Description: "waiting on the user to disposition both proposals",
		})
		head := rt.bus.fanout.Position().Head

		rt.DischargeWaiting(t.Context(), "c1")

		if got := rt.bus.chatStatus.Get("c1"); got.Status != "" {
			t.Errorf("status %q survived the discharge", got.Status)
		}
		got := chatStatusFrames(t, bufferedSince(rt, head))
		if len(got) != 1 {
			t.Fatalf("published %d chat_status frames, want 1: %+v", len(got), got)
		}
		if got[0].Status != "" || got[0].Description != "" {
			t.Errorf("frame = %+v, want both fields empty so setAgentStatus deletes them", got[0])
		}
	})

	t.Run("a chat with no entry publishes nothing", func(t *testing.T) {
		rt, _, _ := newTestHub()
		head := rt.bus.fanout.Position().Head

		rt.DischargeWaiting(t.Context(), "c1")

		if got := chatStatusFrames(t, bufferedSince(rt, head)); len(got) != 0 {
			t.Errorf("published %d chat_status frames for a chat with no claim, want 0: %+v", len(got), got)
		}
	})

	t.Run("a live in_progress entry is left alone", func(t *testing.T) {
		rt, _, _ := newTestHub()
		live := marotte.ChatStatusPayload{Status: "in_progress", Description: "reading the parser"}
		rt.bus.chatStatus.Merge("c1", live)
		head := rt.bus.fanout.Position().Head

		rt.DischargeWaiting(t.Context(), "c1")

		if got := rt.bus.chatStatus.Get("c1"); got != live {
			t.Errorf("entry = %+v, want %+v: the running turn declared it", got, live)
		}
		if got := chatStatusFrames(t, bufferedSince(rt, head)); len(got) != 0 {
			t.Errorf("published %d chat_status frames over a live declaration, want 0: %+v", len(got), got)
		}
	})
}

// TestChatStatusMerge_OmitIsUnchanged is exhaustive over the merge truth table, with distinct
// values so every row shows which side supplied each field.
func TestChatStatusMerge_OmitIsUnchanged(t *testing.T) {
	const (
		prevStatus = marotte.ChatStatusWaitingOnUser
		prevDesc   = "d1"
		nextStatus = "idle"
		nextDesc   = "d2"
	)
	for _, tc := range []struct {
		name                        string
		prevS, prevD, nextS, nextD  bool
		wantStatus, wantDescription string
	}{
		{name: "absent prev, empty next", wantStatus: "", wantDescription: ""},
		{name: "absent prev, description only", nextD: true, wantStatus: "", wantDescription: nextDesc},
		{name: "absent prev, status only", nextS: true, wantStatus: nextStatus, wantDescription: ""},
		{name: "absent prev, both", nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},

		{name: "description-only prev, empty next", prevD: true, wantStatus: "", wantDescription: ""},
		{name: "description-only prev, description only", prevD: true, nextD: true, wantStatus: "", wantDescription: nextDesc},
		{name: "description-only prev, status only", prevD: true, nextS: true, wantStatus: nextStatus, wantDescription: prevDesc},
		{name: "description-only prev, both", prevD: true, nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},

		{name: "status-only prev, empty next", prevS: true, wantStatus: "", wantDescription: ""},
		{name: "status-only prev, description only", prevS: true, nextD: true, wantStatus: prevStatus, wantDescription: nextDesc},
		{name: "status-only prev, status only", prevS: true, nextS: true, wantStatus: nextStatus, wantDescription: ""},
		{name: "status-only prev, both", prevS: true, nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},

		{name: "both prev, empty next", prevS: true, prevD: true, wantStatus: "", wantDescription: ""},
		{name: "both prev, description only", prevS: true, prevD: true, nextD: true, wantStatus: prevStatus, wantDescription: nextDesc},
		{name: "both prev, status only", prevS: true, prevD: true, nextS: true, wantStatus: nextStatus, wantDescription: prevDesc},
		{name: "both prev, both", prevS: true, prevD: true, nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh cache per row, or row N's entry becomes row N+1's prev.
			c := newChatStatusCache()
			if tc.prevS || tc.prevD {
				seed := marotte.ChatStatusPayload{}
				if tc.prevS {
					seed.Status = prevStatus
				}
				if tc.prevD {
					seed.Description = prevDesc
				}
				c.Merge("c1", seed)
			}
			next := marotte.ChatStatusPayload{}
			if tc.nextS {
				next.Status = nextStatus
			}
			if tc.nextD {
				next.Description = nextDesc
			}

			got := c.Merge("c1", next)

			if got.Status != tc.wantStatus || got.Description != tc.wantDescription {
				t.Errorf("Merge returned %+v, want {%q %q}", got, tc.wantStatus, tc.wantDescription)
			}
			if !tc.nextS && !tc.nextD {
				// Get cannot tell an absent key from a stored {"",""}.
				if n := len(c.Snapshot()); n != 0 {
					t.Errorf("a both-empty next left %d entries, want 0: %+v", n, c.Snapshot())
				}
				return
			}
			if stored := c.Get("c1"); stored.Status != tc.wantStatus || stored.Description != tc.wantDescription {
				t.Errorf("stored entry = %+v, want {%q %q}", stored, tc.wantStatus, tc.wantDescription)
			}
		})
	}

	t.Run("an empty chat id publishes the declaration unchanged", func(t *testing.T) {
		c := newChatStatusCache()
		declared := marotte.ChatStatusPayload{Status: "in_progress", Description: "reading files"}

		got := c.Merge("", declared)

		if got != declared {
			t.Errorf("Merge returned %+v, want the declaration %+v: emit publishes this", got, declared)
		}
		if n := len(c.Snapshot()); n != 0 {
			t.Errorf("an empty chat id recorded %d entries, want 0", n)
		}
	})
}

// TestEmitChatStatus_PublishesTheMergedPayload asserts the frame separately: the client replaces both fields from it.
func TestEmitChatStatus_PublishesTheMergedPayload(t *testing.T) {
	rt, _, _ := newTestHub()
	rt.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{
		Status:      marotte.ChatStatusWaitingOnUser,
		Description: "d1",
	})
	head := rt.bus.fanout.Position().Head

	rt.bus.Broadcast(t.Context(), marotte.NewEvent(marotte.EventChatStatus, "c1",
		marotte.ChatStatusPayload{Description: "d2"}))

	if got := rt.bus.chatStatus.Get("c1"); got.Status != marotte.ChatStatusWaitingOnUser || got.Description != "d2" {
		t.Errorf("entry = %+v, want {waiting_on_user d2}: the omitted status means unchanged", got)
	}
	frames := chatStatusFrames(t, bufferedSince(rt, head))
	if len(frames) != 1 {
		t.Fatalf("published %d chat_status frames, want 1: %+v", len(frames), frames)
	}
	if frames[0].Status != marotte.ChatStatusWaitingOnUser || frames[0].Description != "d2" {
		t.Errorf("frame = %+v, want {waiting_on_user d2}: setAgentStatus deletes agent_status on an empty status", frames[0])
	}
}

// TestEmitChatStatus_StatusOnlyKeepsTheDescription is the mirror, and pins that a real declaration discharges the dot.
func TestEmitChatStatus_StatusOnlyKeepsTheDescription(t *testing.T) {
	rt, _, _ := newTestHub()
	rt.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{
		Status:      marotte.ChatStatusWaitingOnUser,
		Description: "d1",
	})
	head := rt.bus.fanout.Position().Head

	rt.bus.Broadcast(t.Context(), marotte.NewEvent(marotte.EventChatStatus, "c1",
		marotte.ChatStatusPayload{Status: "idle"}))

	got := rt.bus.chatStatus.Get("c1")
	if got.Status != "idle" || got.Description != "d1" {
		t.Errorf("entry = %+v, want {idle d1}: the omitted description means unchanged", got)
	}
	if got.Status == marotte.ChatStatusWaitingOnUser {
		t.Error("the status stayed waiting_on_user, so the dot never discharges on a real declaration")
	}
	frames := chatStatusFrames(t, bufferedSince(rt, head))
	if len(frames) != 1 {
		t.Fatalf("published %d chat_status frames, want 1: %+v", len(frames), frames)
	}
	if frames[0].Status != "idle" || frames[0].Description != "d1" {
		t.Errorf("frame = %+v, want {idle d1}", frames[0])
	}
}

// TestEmitChatStatus_DoesNotStageAMergedDescription pins that Turn.statusDesc takes the raw declaration,
// or a previous turn's words reach this turn's push.
func TestEmitChatStatus_DoesNotStageAMergedDescription(t *testing.T) {
	rt, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	rt.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{
		Status:      marotte.ChatStatusWaitingOnUser,
		Description: "d1",
	})
	// A wire turn is KAS's own open, not an answer, so the retention survives it.
	if rt.stageWireTurn(t, "c1") == nil {
		t.Fatal("the fixture could not open a wire turn")
	}

	rt.bus.Broadcast(t.Context(), marotte.NewEvent(marotte.EventChatStatus, "c1",
		marotte.ChatStatusPayload{Status: "in_progress"}))

	// Read the field directly: claimOwn would move the turn into finalizing.
	lc, ok := rt.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("no chat lifecycle for c1")
	}
	lc.mu.Lock()
	staged := lc.own.statusDesc
	lc.mu.Unlock()

	if staged != "" {
		t.Errorf("staged description = %q, want empty: the declaration carried none, so the merge must not reach the turn", staged)
	}
}

// chatStatusFrames decodes the chat_status payloads out of a replay slice.
func chatStatusFrames(t *testing.T, events []sse.ReplayEvent) []marotte.ChatStatusPayload {
	t.Helper()
	var out []marotte.ChatStatusPayload
	for _, e := range events {
		var msg struct {
			Type    marotte.EventType         `json:"type"`
			Payload marotte.ChatStatusPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == marotte.EventChatStatus {
			out = append(out, msg.Payload)
		}
	}
	return out
}

// TestOpenTurn_DischargesTheWaitingRetention pins that a prompt's turn_open answers the claim; a `!cmd` reaches no agent.
func TestOpenTurn_DischargesTheWaitingRetention(t *testing.T) {
	waiting := marotte.ChatStatusPayload{
		Status:      marotte.ChatStatusWaitingOnUser,
		Description: "waiting on the user to disposition both proposals",
	}
	prompt := &marotte.EntryPrompt{ID: "m-c1", Text: "prompt"}
	name := func(c *marotte.Chat) { c.Name = "test chat" }

	t.Run("a prompt clears it", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		rt.bus.chatStatus.Merge("c1", waiting)
		head := rt.bus.fanout.Position().Head

		if _, err := rt.OpenTurn(t.Context(), "c1", command.TurnOpen{Source: marotte.TurnSourcePrompt, Prompt: prompt, Init: name}); err != nil {
			t.Fatalf("the fixture could not open a prompt turn: %v", err)
		}
		if got := rt.bus.chatStatus.Get("c1"); got.Status != "" {
			t.Errorf("status %q survived the prompt that answered it, so a reconnect repaints the dot", got.Status)
		}
		// A second device converges on the frame.
		if got := chatStatusFrames(t, bufferedSince(rt, head)); len(got) != 1 {
			t.Errorf("the prompt published %d chat_status frames, want 1: %+v", len(got), got)
		}
	})

	t.Run("a local shell does not", func(t *testing.T) {
		rt, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		rt.bus.chatStatus.Merge("c1", waiting)

		if _, err := rt.OpenTurn(t.Context(), "c1", command.TurnOpen{Source: marotte.TurnSourceLocalShell, Prompt: prompt, Init: name}); err != nil {
			t.Fatalf("the fixture could not open a shell turn: %v", err)
		}
		got := rt.bus.chatStatus.Get("c1")
		if got.Status != marotte.ChatStatusWaitingOnUser {
			t.Errorf("status is %q, want %q: a `!cmd` reaches no agent, so it answers nothing", got.Status, marotte.ChatStatusWaitingOnUser)
		}
		if got.Description != waiting.Description {
			t.Errorf("description is %q, want %q", got.Description, waiting.Description)
		}
	})
}
