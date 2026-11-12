package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// modeTranslator is a translator over one chat c1 written by seed, and the events it broadcasts.
func modeTranslator(t *testing.T, seed func(c *marotte.Chat)) (*Translator, *testsupport.RecordingChatStore, *[]marotte.ServerEvent) {
	t.Helper()
	deps, events := newEventCaptureDeps()
	store := testsupport.NewRecordingChatStore()
	deps.store = store
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A chat"
		seed(c)
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return New(rolesOf(deps)), store, events
}

func seedBot(c *marotte.Chat) {
	c.CurrentModeID = marotte.BotRoleID
	c.Bot = &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1}
}

// TestHandleModeUpdate_ABotStaysOnItsRole pins that an agent's own switch never moves a Bot's record off its role, so
// no pill flips and no mode_changed is sent; the door re-asserts the role at the turn's end.
func TestHandleModeUpdate_ABotStaysOnItsRole(t *testing.T) {
	tr, store, events := modeTranslator(t, seedBot)

	tr.HandleModeUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"currentModeId": "kiro_spec"}))

	c, _ := store.Get(t.Context(), "c1")
	if c.CurrentModeID != marotte.BotRoleID {
		t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, marotte.BotRoleID)
	}
	for _, e := range *events {
		if e.Type == marotte.EventModeChanged {
			t.Errorf("mode_changed broadcast %+v for a Bot held on its role", e.Payload)
		}
	}
}

// TestHandleModeUpdate_ABotHealedBackAnnouncesItsRole pins that a Bot record left off its role is put back by any
// mode report, and the pill hears the role, not the mode KAS reported.
func TestHandleModeUpdate_ABotHealedBackAnnouncesItsRole(t *testing.T) {
	tr, store, events := modeTranslator(t, func(c *marotte.Chat) {
		seedBot(c)
		c.CurrentModeID = marotte.DefaultModeID
	})

	tr.HandleModeUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"currentModeId": "kiro_spec"}))

	c, _ := store.Get(t.Context(), "c1")
	if c.CurrentModeID != marotte.BotRoleID {
		t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, marotte.BotRoleID)
	}
	var announced []string
	for _, e := range *events {
		if p, ok := e.Payload.(marotte.ModeChangedPayload); ok && e.Type == marotte.EventModeChanged {
			announced = append(announced, p.ModeID)
		}
	}
	if len(announced) != 1 || announced[0] != marotte.BotRoleID {
		t.Errorf("mode_changed announced %q, want [%q]", announced, marotte.BotRoleID)
	}
}

// TestHandleModeUpdate_ARegularChatNeverTakesTheBotRole pins the other half: a tangent forked from a Bot whose
// session reports the role keeps its own mode.
func TestHandleModeUpdate_ARegularChatNeverTakesTheBotRole(t *testing.T) {
	tr, store, _ := modeTranslator(t, func(c *marotte.Chat) { c.CurrentModeID = marotte.DefaultModeID })

	tr.HandleModeUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"currentModeId": marotte.BotRoleID}))

	c, _ := store.Get(t.Context(), "c1")
	if c.CurrentModeID != marotte.DefaultModeID {
		t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, marotte.DefaultModeID)
	}
}

// TestHandleAgentNotFound_ADraftShowsTheFallback pins that the swallowed toast is a started Bot's: a draft's session
// never asked for the role, so a fallback KAS reports for it is the regular chat's message.
func TestHandleAgentNotFound_ADraftShowsTheFallback(t *testing.T) {
	tr, _, events := modeTranslator(t, func(c *marotte.Chat) {
		c.CurrentModeID = marotte.BotRoleID
		c.Bot = &marotte.Bot{Goal: "Not started yet."}
	})

	tr.HandleAgentNotFound(t.Context(), "c1", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{"requestedAgent": "kiro_spec", "fallbackAgent": marotte.DefaultModeID}),
	})

	var shown int
	for _, e := range *events {
		if p, ok := e.Payload.(marotte.ErrorPayload); ok && p.Code == marotte.ErrCodeAgentNotFound {
			shown++
		}
	}
	if shown != 1 {
		t.Errorf("%d agent_not_found toasts for a draft, want 1", shown)
	}
}

// TestHandleAgentNotFound_ABotPersistsNoFallback pins that KAS falling back from a role it could not load leaves the
// Bot's record on the role, and the door's failure is the one message the user sees, not the fallback's.
func TestHandleAgentNotFound_ABotPersistsNoFallback(t *testing.T) {
	tr, store, events := modeTranslator(t, seedBot)

	tr.HandleAgentNotFound(t.Context(), "c1", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{"requestedAgent": marotte.BotRoleID, "fallbackAgent": marotte.DefaultModeID}),
	})

	c, _ := store.Get(t.Context(), "c1")
	if c.CurrentModeID != marotte.BotRoleID {
		t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, marotte.BotRoleID)
	}
	for _, e := range *events {
		if p, ok := e.Payload.(marotte.ErrorPayload); ok && p.Code == marotte.ErrCodeAgentNotFound {
			t.Errorf("agent_not_found shown on a Bot: %q", p.Message)
		}
	}
}
