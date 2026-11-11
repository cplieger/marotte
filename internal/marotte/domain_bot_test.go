package marotte

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestChat_ABotBlockRoundTripsThroughChatJSON pins every Bot field through chat.json: the record is a Bot's only
// store, so a field that does not survive a reload is a setting the user loses on restart.
func TestChat_ABotBlockRoundTripsThroughChatJSON(t *testing.T) {
	cases := []struct {
		name string
		bot  Bot
	}{
		{name: "draft", bot: Bot{}},
		{name: "started", bot: Bot{Goal: "Keep the CI green", Steering: "# Rules\n\n- be brief", StartedAt: 1_700_000_000_000, Memory: true}},
		{name: "paused", bot: Bot{Goal: "Watch the queue", StartedAt: 1_700_000_000_000, Paused: true}},
		{name: "closed", bot: Bot{Goal: "Triage issues", StartedAt: 1_700_000_000_000, ClosedAt: 1_700_000_900_000, Memory: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bot := tc.bot
			raw, err := json.Marshal(&Chat{ID: "c1", Name: "My Bot", Bot: &bot})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Contains(raw, []byte(`"bot":{`)) {
				t.Errorf("chat.json has no bot block: %s", raw)
			}
			var got Chat
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			if got.Bot == nil {
				t.Fatalf("reloaded chat lost its bot block: %s", raw)
			}
			if *got.Bot != tc.bot {
				t.Errorf("reloaded bot = %+v, want %+v", *got.Bot, tc.bot)
			}
		})
	}
}

// TestChat_ARegularChatCarriesNoBot pins that only a Bot's record and header say "bot": History labels a row from the
// header key's presence.
func TestChat_ARegularChatCarriesNoBot(t *testing.T) {
	c := &Chat{ID: "c1", Name: "Hello"}
	record, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal chat: %v", err)
	}
	header, err := json.Marshal(c.Header())
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	for name, b := range map[string][]byte{"chat.json": record, "header": header} {
		if bytes.Contains(b, []byte(`"bot"`)) {
			t.Errorf("a regular chat's %s carries bot: %s", name, b)
		}
	}
	if c.Header().Bot != nil {
		t.Errorf("Header().Bot = %+v, want nil for a regular chat", c.Header().Bot)
	}
}

// TestChatHeader_StatesTheBotsState pins design.md §3.2's state rule: "closed" while closed_at is set, whatever else
// holds, else "draft" before the first start, "paused" while paused, otherwise "active".
func TestChatHeader_StatesTheBotsState(t *testing.T) {
	cases := []struct {
		name string
		bot  Bot
		want BotState
	}{
		{name: "draft", bot: Bot{}, want: botStateDraft},
		{name: "draft_with_a_goal", bot: Bot{Goal: "Keep the CI green"}, want: botStateDraft},
		{name: "active", bot: Bot{Goal: "g", StartedAt: 1}, want: botStateActive},
		{name: "paused", bot: Bot{Goal: "g", StartedAt: 1, Paused: true}, want: botStatePaused},
		{name: "closed_active", bot: Bot{Goal: "g", StartedAt: 1, ClosedAt: 2}, want: botStateClosed},
		{name: "closed_paused", bot: Bot{Goal: "g", StartedAt: 1, ClosedAt: 2, Paused: true}, want: botStateClosed},
		{name: "closed_draft", bot: Bot{ClosedAt: 2}, want: botStateClosed},
		{name: "a_paused_draft_is_a_draft", bot: Bot{Paused: true}, want: botStateDraft},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bot := tc.bot
			h := (&Chat{ID: "c1", Name: "My Bot", Bot: &bot}).Header()
			if h.Bot == nil {
				t.Fatalf("Header().Bot = nil for a Bot's chat, want a summary")
			}
			if h.Bot.State != tc.want {
				t.Errorf("Header().Bot.State = %q, want %q", h.Bot.State, tc.want)
			}
			raw, err := json.Marshal(h)
			if err != nil {
				t.Fatalf("marshal header: %v", err)
			}
			if want := `"bot":{"state":"` + string(tc.want) + `"}`; !bytes.Contains(raw, []byte(want)) {
				t.Errorf("header JSON = %s, want it to carry %s", raw, want)
			}
		})
	}
}

// TestChatHeader_CarriesNoBotText pins that the goal and the steering stay off the header: every boot reads the
// whole list, and the Bot's page reads its text on its own route.
func TestChatHeader_CarriesNoBotText(t *testing.T) {
	bot := Bot{Goal: "goal-text-marker", Steering: "steering-text-marker", StartedAt: 1}
	raw, err := json.Marshal((&Chat{ID: "c1", Bot: &bot}).Header())
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	for _, marker := range []string{"goal-text-marker", "steering-text-marker"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Errorf("header carries the Bot's %s: %s", marker, raw)
		}
	}
}

// TestBotWireShapes_AreStrict pins the new payloads' tags: every field is on the wire under a json name, and the
// summary's state has no omitempty, which would generate an optional TS field the client could invent a default for.
func TestBotWireShapes_AreStrict(t *testing.T) {
	for _, rt := range []reflect.Type{reflect.TypeFor[Bot](), reflect.TypeFor[BotSummary]()} {
		for f := range rt.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				t.Errorf("%s.%s has no json name; every field is part of the record or the wire", rt.Name(), f.Name)
			}
		}
	}
	state, ok := reflect.TypeFor[BotSummary]().FieldByName("State")
	if !ok {
		t.Fatal("BotSummary has no State field")
	}
	if tag := state.Tag.Get("json"); tag != "state" {
		t.Errorf("BotSummary.State json tag = %q, want \"state\" with no omitempty", tag)
	}
	header, ok := reflect.TypeFor[ChatHeader]().FieldByName("Bot")
	if !ok {
		t.Fatal("ChatHeader has no Bot field")
	}
	if tag := header.Tag.Get("json"); tag != "bot,omitempty" {
		t.Errorf("ChatHeader.Bot json tag = %q, want \"bot,omitempty\": a regular chat's header has no bot", tag)
	}
}

// TestTabBotFact_HasNoClientDoor pins that no client can state a tab's Bot fact: OpenTab.Bot is never decoded and the
// open_tab payload has no field for it.
func TestTabBotFact_HasNoClientDoor(t *testing.T) {
	f, ok := reflect.TypeFor[OpenTab]().FieldByName("Bot")
	if !ok {
		t.Fatal("OpenTab has no Bot field")
	}
	if tag := f.Tag.Get("json"); tag != "-" {
		t.Errorf(`OpenTab.Bot json tag = %q, want "-"`, tag)
	}
	var spec OpenTab
	if err := json.Unmarshal([]byte(`{"kind":"chat","ref":"c1","bot":true,"Bot":true}`), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.Bot {
		t.Error("a decoded OpenTab naming bot set Bot")
	}
	for f := range reflect.TypeFor[OpenTabCommand]().Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if strings.EqualFold(f.Name, "bot") || name == "bot" {
			t.Errorf("OpenTabCommand.%s (json %q) lets a client state the Bot fact", f.Name, name)
		}
	}
}
