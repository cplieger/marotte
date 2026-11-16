package agent

// Every command that writes a Bot's record moves spec_version exactly when the Bot's view changes (the fields a
// bot_save states), so a page that loaded before another device's write gets 409 bot_changed instead of writing its
// stale values back.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

const (
	specBotName  = "Watcher"
	specBotGoal  = "Watch CI."
	specBotModel = "m-old"
)

// seedSpecBot writes c1 as a started Bot at spec_version 3 on specBotModel, effort low.
func seedSpecBot(t *testing.T, cs *testChatStore) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.NameSetByUser = specBotName, true
		c.CurrentModeID = marotte.BotRoleID
		c.Model, c.Effort = specBotModel, "low"
		c.Bot = &marotte.Bot{Goal: specBotGoal, StartedAt: 1, Memory: true, SpecVersion: 3}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the Bot: %v", err)
	}
}

func specVersionOf(t *testing.T, cs *testChatStore) int {
	t.Helper()
	c, ok := cs.Get(t.Context(), "c1")
	if !ok || c.Bot == nil {
		t.Fatalf("c1 is no Bot record (found %v)", ok)
	}
	return c.Bot.SpecVersion
}

func postTyped(t *testing.T, h *Runtime, typ marotte.CommandType, payload string) int {
	t.Helper()
	rec := postCmd(t, h, marotte.ClientCommand{Type: typ, ChatID: "c1", Payload: json.RawMessage(payload)})
	if rec.Code != http.StatusOK {
		t.Logf("%s %s = %d %s", typ, payload, rec.Code, rec.Body.String())
	}
	return rec.Code
}

// staleSave is a bot_save at base 3 that states the Bot as seedSpecBot wrote it.
func staleSave(t *testing.T, h *Runtime) (int, string) {
	t.Helper()
	rec := postCmd(t, h, botSaveCmd(t, "c1", marotte.BotSaveCommand{
		Name: specBotName, Goal: specBotGoal, Memory: true, Model: specBotModel, BaseVersion: 3,
	}))
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Reason
}

func TestBotRecordWrites_SpecVersionMovesExactlyWhenTheBotsViewChanges(t *testing.T) {
	rows := []struct {
		// kept reports whether the record still holds the row's change.
		kept  func(c *marotte.Chat) bool
		act   func(t *testing.T, h *Runtime) int
		name  string
		bumps bool
	}{
		{
			name: "rename_chat to a new name",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdRenameChat, `{"name":"CI watcher"}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.Name == "CI watcher" },
			bumps: true,
		},
		{
			name: "rename_chat to the name it has",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdRenameChat, `{"name":"Watcher"}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.Name == specBotName },
			bumps: false,
		},
		{
			name: "switch_model to another model",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSwitchModel, `{"model":"m-new"}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.NextModel() == "m-new" },
			bumps: true,
		},
		{
			name: "switch_model to the model it runs",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSwitchModel, `{"model":"m-old"}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.NextModel() == specBotModel },
			bumps: false,
		},
		{
			name:  "set_effort to another tier",
			act:   func(t *testing.T, h *Runtime) int { return postTyped(t, h, marotte.CmdSetEffort, `{"level":"high"}`) },
			kept:  func(c *marotte.Chat) bool { return c.NextEffort() == "high" },
			bumps: true,
		},
		{
			name: "switch_model to another tier of its model",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSwitchModel, `{"effort":"high"}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.NextEffort() == "high" },
			bumps: true,
		},
		{
			name: "set_thinking on while thinking already runs",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSetThinking, `{"enabled":true}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.Thinking == marotte.ThinkingOn && c.NextEffort() == "low" },
			bumps: false,
		},
		{
			name: "set_thinking off",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSetThinking, `{"enabled":false}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.NextEffort() == string(marotte.SwitchEffortOff) },
			bumps: true,
		},
		{
			name: "set_supervised_mode on",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSetSupervisedMode, `{"enabled":true}`)
			},
			kept:  func(c *marotte.Chat) bool { return c.SupervisedMode },
			bumps: true,
		},
		{
			name: "set_supervised_mode to the value it has",
			act: func(t *testing.T, h *Runtime) int {
				return postTyped(t, h, marotte.CmdSetSupervisedMode, `{"enabled":false}`)
			},
			kept:  func(c *marotte.Chat) bool { return !c.SupervisedMode },
			bumps: false,
		},
		{
			name:  "bot_set_paused",
			act:   func(t *testing.T, h *Runtime) int { return postTyped(t, h, marotte.CmdBotSetPaused, `{"paused":true}`) },
			kept:  func(c *marotte.Chat) bool { return c.Bot.Paused },
			bumps: false,
		},
		{
			name:  "bot_clear_memory",
			act:   func(t *testing.T, h *Runtime) int { return postTyped(t, h, marotte.CmdBotClearMemory, `{}`) },
			kept:  func(c *marotte.Chat) bool { return c.Bot.Memory },
			bumps: false,
		},
		{
			name: "close_tab",
			act: func(t *testing.T, h *Runtime) int {
				tab := reopenTab(t, h, "op-open")
				return postCloseTabOp(t, h, tab, "op-close")
			},
			kept:  func(c *marotte.Chat) bool { return c.Bot.ClosedAt != 0 },
			bumps: false,
		},
		{
			name: "close_tab, then a reopen",
			act: func(t *testing.T, h *Runtime) int {
				if code := postCloseTabOp(t, h, reopenTab(t, h, "op-open"), "op-close"); code != http.StatusOK {
					return code
				}
				reopenTab(t, h, "op-reopen")
				return http.StatusOK
			},
			kept:  func(c *marotte.Chat) bool { return c.Bot.ClosedAt == 0 },
			bumps: false,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			st, err := tabs.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("Setup: tabs.NewStore: %v", err)
			}
			h, cs := botRuntimeWith(t, []Option{WithTabs(st)})
			seedSpecBot(t, cs)

			if code := row.act(t, h); code != http.StatusOK {
				t.Fatalf("the row's command = %d, want 200", code)
			}

			want := 3
			if row.bumps {
				want = 4
			}
			if got := specVersionOf(t, cs); got != want {
				t.Errorf("spec_version after the row = %d, want %d", got, want)
			}
			code, reason := staleSave(t, h)
			switch {
			case row.bumps && (code != http.StatusConflict || reason != "bot_changed"):
				t.Errorf("a save at the base loaded before the row = %d %q, want 409 bot_changed", code, reason)
			case !row.bumps && code != http.StatusOK:
				t.Errorf("a save at the base the row left = %d %q, want 200", code, reason)
			}
			c, _ := cs.Get(t.Context(), "c1")
			if !row.kept(c) {
				t.Errorf("the record lost the row's change: name %q model %q effort %q supervised %v paused %v closed_at %d",
					c.Name, c.NextModel(), c.NextEffort(), c.SupervisedMode, c.Bot.Paused, c.Bot.ClosedAt)
			}
		})
	}
}

func TestBotRecordWrites_AStartBumpsTheDraftsSpecVersion(t *testing.T) {
	h, cs := botRuntime(t)
	seedBotDraft(t, cs, "c1")

	rec := postCmd(t, h, botSaveCmd(t, "c1", marotte.BotSaveCommand{Name: specBotName, Goal: specBotGoal, Start: true}))
	if rec.Code != http.StatusOK {
		t.Fatalf("bot_save start = %d %s, want 200", rec.Code, rec.Body.String())
	}

	if got := specVersionOf(t, cs); got != 1 {
		t.Errorf("spec_version after the start = %d, want 1", got)
	}
	again := postCmd(t, h, botSaveCmd(t, "c1", marotte.BotSaveCommand{Name: specBotName, Goal: "Other.", BaseVersion: 0}))
	if again.Code != http.StatusConflict {
		t.Errorf("a save at the draft's base = %d %s, want 409", again.Code, again.Body.String())
	}
}

func TestRenameChat_ARegularChatWritesNoBot(t *testing.T) {
	h, cs := botRuntime(t)
	seedModeChat(t, cs, "c1", false, false)

	if code := postTyped(t, h, marotte.CmdRenameChat, `{"name":"Renamed"}`); code != http.StatusOK {
		t.Fatalf("rename_chat = %d, want 200", code)
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Name != "Renamed" || c.Bot != nil {
		t.Errorf("name %q bot %+v, want the new name and no Bot block", c.Name, c.Bot)
	}
}

func TestSwitchModel_ABotsParkedPickBumpsOnceAndItsLandingBumpsNothing(t *testing.T) {
	h, cs := botRuntime(t)
	seedSpecBot(t, cs)
	id, _ := h.stagePromptTurn(t, "c1")

	if code := postTyped(t, h, marotte.CmdSwitchModel, `{"model":"m-new"}`); code != http.StatusOK {
		t.Fatalf("switch_model = %d, want 200", code)
	}
	c, _ := cs.Get(t.Context(), "c1")
	if c.PendingModel != "m-new" || c.Bot.SpecVersion != 4 {
		t.Fatalf("pending %q spec_version %d mid-turn, want m-new parked at 4", c.PendingModel, c.Bot.SpecVersion)
	}

	endTurn(t, h, "c1", id)
	awaitModel(t, cs, "c1", "m-new")

	if got := specVersionOf(t, cs); got != 4 {
		t.Errorf("spec_version after the parked pick landed = %d, want 4 kept", got)
	}
}
