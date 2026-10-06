package agent

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Model lives on the Chat, so a footer reading the current model would relabel every past turn on a switch. The
// value is latched at turn start and stamped on turn_close.

// turnClosedModel returns the last turn_closed frame's model and whether the field was present.
func turnClosedModel(t *testing.T, h *Runtime) (model string, present bool) {
	t.Helper()
	for _, e := range bufferedSince(h, 0) {
		var msg struct {
			Type    marotte.EventType `json:"type"`
			Payload struct {
				Entry struct {
					Payload struct {
						Model *string `json:"model"`
					} `json:"payload"`
				} `json:"entry"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type != marotte.EventTurnClosed {
			continue
		}
		if msg.Payload.Entry.Payload.Model == nil {
			return "", false
		}
		return *msg.Payload.Entry.Payload.Model, true
	}
	t.Fatal("no turn_closed frame was broadcast")
	return "", false
}

// closeModel returns the model on the chat's one turn_close.
func closeModel(t *testing.T, cs *testChatStore, chatID marotte.ChatID) string {
	t.Helper()
	closes := closesOf(t, logOf(t, cs, chatID))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want 1", len(closes))
	}
	return closes[0].Model
}

func TestTurnModel_StampedOnThePersistedCloseAndOnTheSSE(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", newChunkMsg("hello"))
	endTurn(t, h, "c1", id)

	// Persisted: turn_closed is not replayed, so a live-only value would vanish on reload.
	if got := closeModel(t, cs, "c1"); got != "sonnet-4" {
		t.Errorf("persisted turn_close.model = %q, want %q", got, "sonnet-4")
	}
	model, present := turnClosedModel(t, h)
	if !present || model != "sonnet-4" {
		t.Errorf("turn_closed model = %q (present=%v), want %q", model, present, "sonnet-4")
	}
}

func TestTurnModel_AbsentWhenTheChatNamesNoModel(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A" // no Model: the session took the backend default
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", newChunkMsg("hello"))
	endTurn(t, h, "c1", id)

	if got := closeModel(t, cs, "c1"); got != "" {
		t.Errorf("turn_close.model = %q, want empty for an unknowable model", got)
	}
	// omitempty: absent rather than "", so the client renders no empty attribution.
	if _, present := turnClosedModel(t, h); present {
		t.Error("turn_closed carried a model field for a chat that names no model")
	}
}

// TestTurnModel_LatchedAtTurnStartNotAtTurnEnd pins that reading the chat at turn end would attribute the turn to the model
// current when it finished.
func TestTurnModel_LatchedAtTurnStartNotAtTurnEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", newChunkMsg("half an answer"))
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model = "opus-4"
		return true
	}); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	h.translateACPEvent("c1", newChunkMsg(" continued"))
	endTurn(t, h, "c1", id)

	if got := closeModel(t, cs, "c1"); got != "sonnet-4" {
		t.Errorf("turn_close.model = %q, want %q — the model that was running when the "+
			"turn started, not the one current when it ended", got, "sonnet-4")
	}
}

// TestTurnModel_SwitchBeforeTheFirstFrameKeepsTheDispatchedModel pins that a switch while the prompt is in flight is parked
// as pending_model and applied by the closer, so the dispatched model holds for the whole turn.
func TestTurnModel_SwitchBeforeTheFirstFrameKeepsTheDispatchedModel(t *testing.T) {
	h, cs, br := newTestHub()
	// spawnBridge writes the session's model back onto the chat, so the fixture must agree with the seeded model.
	br.modelID = "sonnet-4"
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	unblock := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: unblock}

	done := make(chan struct{})
	go func() {
		defer close(done)
		postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: "c1",
			Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
		})
	}()
	waitForCall(t, br, marotte.MethodPrompt)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSwitchModel, ChatID: "c1",
		Payload: json.RawMessage(`{"model":"opus-4"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("switch_model = %d, body %s", rec.Code, rec.Body.String())
	}
	if c, _ := cs.Get(t.Context(), "c1"); c.PendingModel != "opus-4" || c.Model != "sonnet-4" {
		t.Fatalf("setup: chat = {Model %q, PendingModel %q}, want the pick parked as pending over the dispatched model",
			c.Model, c.PendingModel)
	}

	h.translateACPEvent("c1", newChunkMsg("the previous model's answer"))
	close(unblock)
	<-done

	// The POST answers at the ack and the turn closes on its own goroutine, so poll for the turn_close.
	deadline := time.Now().Add(5 * time.Second)
	for {
		closes := closesOf(t, logOf(t, cs, "c1"))
		if len(closes) > 0 {
			if got := closes[0].Model; got != "sonnet-4" {
				t.Errorf("turn_close.model = %q, want %q — the model the prompt was DISPATCHED "+
					"under, not the one a switch parked before the first frame",
					got, "sonnet-4")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no turn_close was persisted for the turn")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForCall blocks until the bridge has recorded a Call to method, failing with a diagnostic at the deadline.
func waitForCall(t *testing.T, br *fakeBridge, method string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if slices.Contains(br.callLog(), method) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge never received %s; calls = %v", method, br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTurnModel_AbandonedTurnCarriesItToo pins that every closer runs the one turn end rule, abandon included.
func TestTurnModel_AbandonedTurnCarriesItToo(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", newChunkMsg("the model got this far"))
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)

	if got := closeModel(t, cs, "c1"); got != "sonnet-4" {
		t.Errorf("abandoned turn_close.model = %q, want %q", got, "sonnet-4")
	}
}
