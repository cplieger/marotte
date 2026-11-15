package command

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// botFakes records what bot_save hands its collaborators: reload marks, KAS renames, model switches and kickoffs.
type botFakes struct {
	kickoffErr error
	reloads    []marotte.ChatID
	renames    []string
	switches   []marotte.SwitchModelCommand
	kickoffs   []*marotte.PromptCommand
	renameErr  error
	switchErr  error
	mu         sync.Mutex
}

func (f *botFakes) MarkBotReload(id marotte.ChatID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads = append(f.reloads, id)
}

func (f *botFakes) RenameSession(_ context.Context, sessionID, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, sessionID+"="+title)
	return f.renameErr
}

func (f *botFakes) SwitchModel(_ context.Context, _ marotte.ChatID, p marotte.SwitchModelCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switches = append(f.switches, p)
	return f.switchErr
}

func (f *botFakes) kickoff(_ context.Context, _ marotte.ChatID, p *marotte.PromptCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kickoffs = append(f.kickoffs, p)
	return f.kickoffErr
}

// newBotRoles is bot_save's roles over store, with every collaborator recorded and the memory files under a temp
// KIRO_HOME.
func newBotRoles(t *testing.T, store chatStore) (*botRoles, *botFakes) {
	t.Helper()
	f := &botFakes{}
	return &botRoles{
		chats:           store,
		bridges:         newTestHost(t, store),
		renamer:         f,
		reload:          f,
		models:          f,
		kickoff:         f.kickoff,
		paths:           bot.Paths{KiroHome: t.TempDir()},
		supervisedLocks: keylock.New(cmp.Compare[marotte.ChatID]),
		saveLocks:       keylock.New(cmp.Compare[marotte.ChatID]),
	}, f
}

// seedBot writes id as a Bot named name whose block is b, on its role.
func seedBot(t *testing.T, store chatStore, id marotte.ChatID, name string, b *marotte.Bot) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.ID = string(id)
		c.Name = name
		c.NameSetByUser = true
		c.CurrentModeID = marotte.BotRoleID
		c.Bot = b
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the Bot %q: %v", id, err)
	}
}

func botSaveReq(t *testing.T, id marotte.ChatID, p *marotte.BotSaveCommand) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdBotSave, ChatID: id, Payload: payload}
}

func saveBot(t *testing.T, roles *botRoles, id marotte.ChatID, p *marotte.BotSaveCommand) map[string]any {
	t.Helper()
	body, err := cmdBotSave(t.Context(), roles, botSaveReq(t, id, p))
	if err != nil {
		t.Fatalf("bot_save = %d %v, want 200", statusOf(err), err)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("bot_save answered %T, want a map", body)
	}
	return m
}

func fieldsOf(err error) []string {
	se, ok := errors.AsType[*statusError](err)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(se.fields))
	for _, f := range se.fields {
		out = append(out, f.Field)
	}
	return out
}

func botOf(t *testing.T, store chatStore, id marotte.ChatID) *marotte.Chat {
	t.Helper()
	c, ok := store.Get(t.Context(), id)
	if !ok || c.Bot == nil {
		t.Fatalf("Bot %q: record %v, want a Bot record", id, ok)
	}
	return c
}

func TestCmdBotSave_RefusesEachInvalidFieldByName(t *testing.T) {
	cases := []struct {
		name string
		edit func(p *marotte.BotSaveCommand)
		want []string
	}{
		{name: "a start with no goal", edit: func(p *marotte.BotSaveCommand) { p.Goal = "  \n"; p.Start = true }, want: []string{"goal"}},
		{name: "a goal past 8 KiB", edit: func(p *marotte.BotSaveCommand) { p.Goal = strings.Repeat("g", 8<<10+1) }, want: []string{"goal"}},
		{name: "a goal holding a control character", edit: func(p *marotte.BotSaveCommand) { p.Goal = "watch\x07CI" }, want: []string{"goal"}},
		{name: "steering past 64 KiB", edit: func(p *marotte.BotSaveCommand) { p.Steering = strings.Repeat("s", 64<<10+1) }, want: []string{"steering"}},
		{name: "steering holding a control character", edit: func(p *marotte.BotSaveCommand) { p.Steering = "a\x00b" }, want: []string{"steering"}},
		{name: "an empty name", edit: func(p *marotte.BotSaveCommand) { p.Name = "   " }, want: []string{"name"}},
		{name: "a name past the rename cap", edit: func(p *marotte.BotSaveCommand) { p.Name = strings.Repeat("n", marotte.MaxUserChatNameUnits+1) }, want: []string{"name"}},
		{name: "a model that is no identifier", edit: func(p *marotte.BotSaveCommand) { p.Model = "a model" }, want: []string{"model"}},
		{name: "an effort that is no tier id", edit: func(p *marotte.BotSaveCommand) { p.Effort = "High" }, want: []string{"effort"}},
		{name: "every bad field at once", edit: func(p *marotte.BotSaveCommand) { p.Name = ""; p.Goal = "x\x01"; p.Steering = "\x02" }, want: []string{"name", "goal", "steering"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
			roles, f := newBotRoles(t, store)
			p := &marotte.BotSaveCommand{Name: "CI watcher", Goal: "Keep CI green.\n\tAlways."}
			tc.edit(p)

			_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", p))

			if statusOf(err) != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%v)", statusOf(err), err)
			}
			if got := fieldsOf(err); !slices.Equal(got, tc.want) {
				t.Errorf("fields = %v, want %v", got, tc.want)
			}
			if c := botOf(t, store, "c-bot"); c.Name != "New Bot" || c.Bot.StartedAt != 0 || c.Bot.SpecVersion != 0 {
				t.Errorf("record = %q started %d version %d, want it unchanged", c.Name, c.Bot.StartedAt, c.Bot.SpecVersion)
			}
			if len(f.kickoffs) != 0 {
				t.Errorf("a refused save sent %d kickoff(s)", len(f.kickoffs))
			}
		})
	}
}

func TestCmdBotSave_AcceptsNewlinesAndTabsInTheGoal(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, _ := newBotRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "CI watcher", Goal: "Keep CI green.\n\tAlways.", Steering: "# Rules\n\n- be brief\n"})

	if c := botOf(t, store, "c-bot"); c.Bot.Goal != "Keep CI green.\n\tAlways." || c.Bot.Steering != "# Rules\n\n- be brief\n" {
		t.Errorf("goal %q steering %q, want the payload's", c.Bot.Goal, c.Bot.Steering)
	}
}

func TestCmdBotSave_AStaleBaseVersionSavesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Old goal", StartedAt: 1, SpecVersion: 3})
	roles, f := newBotRoles(t, store)

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot",
		&marotte.BotSaveCommand{Name: "Watcher", Goal: "New goal", Model: "model-b", BaseVersion: 2}))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != "bot_changed" {
		t.Errorf("status %d reason %q, want 409 bot_changed", statusOf(err), reasonOf(err))
	}
	if len(f.switches) != 0 {
		t.Errorf("a refused save switched the model: %+v", f.switches)
	}
	if c := botOf(t, store, "c-bot"); c.Bot.Goal != "Old goal" || c.Bot.SpecVersion != 3 {
		t.Errorf("goal %q version %d, want the record unchanged", c.Bot.Goal, c.Bot.SpecVersion)
	}
	if len(f.reloads) != 0 {
		t.Errorf("a refused save marked %v for reload", f.reloads)
	}
}

// saveAfterRead is a chat store that runs other once, inside the first Get: between this save's read and its write.
type saveAfterRead struct {
	chatStore
	other func()
}

func (s *saveAfterRead) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	c, ok := s.chatStore.Get(ctx, id)
	if s.other != nil {
		other := s.other
		s.other = nil
		other()
	}
	return c, ok
}

func TestCmdBotSave_ASecondDevicesSaveWaitsAndIsRefusedOnTheStaleBase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Old goal", StartedAt: 1, SpecVersion: 3})
		racing := &saveAfterRead{chatStore: store}
		roles, f := newBotRoles(t, racing)
		other := *roles
		other.chats = store
		on := true
		var otherErr error
		done := make(chan struct{})
		racing.other = func() {
			go func() {
				defer close(done)
				_, otherErr = cmdBotSave(t.Context(), &other, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
					Name: "Watcher", Goal: "Their goal", Model: "model-b", Supervised: &on, BaseVersion: 3,
				}))
			}()
			synctest.Wait()
		}

		body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "My goal", BaseVersion: 3})
		<-done

		if body["spec_version"] != 4 {
			t.Errorf("first save answered spec_version %v, want 4", body["spec_version"])
		}
		if statusOf(otherErr) != http.StatusConflict || reasonOf(otherErr) != "bot_changed" {
			t.Errorf("second save = %d %q, want 409 bot_changed", statusOf(otherErr), reasonOf(otherErr))
		}
		if len(f.switches) != 0 {
			t.Errorf("the refused save switched the model: %+v", f.switches)
		}
		if c := botOf(t, store, "c-bot"); c.Bot.Goal != "My goal" || c.Bot.SpecVersion != 4 || c.SupervisedMode {
			t.Errorf("goal %q version %d supervised %v, want the first save's at version 4, unsupervised",
				c.Bot.Goal, c.Bot.SpecVersion, c.SupervisedMode)
		}
	})
}

func TestCmdBotSave_AChangeBumpsTheSpecVersionAndAnswersIt(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Old goal", StartedAt: 1, SpecVersion: 3})
	roles, _ := newBotRoles(t, store)

	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "New goal", BaseVersion: 3})

	if got := botOf(t, store, "c-bot").Bot.SpecVersion; got != 4 {
		t.Errorf("spec_version = %d, want 4", got)
	}
	if body["spec_version"] != 4 {
		t.Errorf("answer spec_version = %v, want 4", body["spec_version"])
	}
	if _, ok := body["chat"].(marotte.ChatHeader); !ok {
		t.Errorf("answer chat = %T, want the header", body["chat"])
	}
}

func TestCmdBotSave_ASaveThatChangesNothingKeepsTheSpecVersion(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Goal", StartedAt: 1, Memory: true, SpecVersion: 3})
	roles, f := newBotRoles(t, store)

	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Goal", Memory: true, BaseVersion: 3})

	if got := botOf(t, store, "c-bot").Bot.SpecVersion; got != 3 || body["spec_version"] != 3 {
		t.Errorf("spec_version = %d, answered %v, want 3 kept", got, body["spec_version"])
	}
	if len(f.reloads) != 0 {
		t.Errorf("an unchanged save marked %v for reload", f.reloads)
	}
}

func TestCmdBotSave_AChatChoiceChangeAloneBumpsTheSpecVersion(t *testing.T) {
	on := true
	cases := []struct {
		name string
		edit func(p *marotte.BotSaveCommand)
		kept func(c *marotte.Chat) bool
	}{
		{
			name: "supervised",
			edit: func(p *marotte.BotSaveCommand) { p.Supervised = &on },
			kept: func(c *marotte.Chat) bool { return c.SupervisedMode },
		},
		{name: "the model", edit: func(p *marotte.BotSaveCommand) { p.Model = "model-b" }},
		{name: "the effort", edit: func(p *marotte.BotSaveCommand) { p.Effort = "high" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
			roles, f := newBotRoles(t, store)
			p := &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch."}
			tc.edit(p)

			body := saveBot(t, roles, "c-bot", p)

			if got := botOf(t, store, "c-bot").Bot.SpecVersion; got != 1 || body["spec_version"] != 1 {
				t.Fatalf("spec_version = %d, answered %v, want 1", got, body["spec_version"])
			}
			switches := len(f.switches)
			_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch."}))
			if statusOf(err) != http.StatusConflict || reasonOf(err) != "bot_changed" {
				t.Errorf("a save on the old base = %d %q, want 409 bot_changed", statusOf(err), reasonOf(err))
			}
			if len(f.switches) != switches {
				t.Errorf("the stale save switched the model: %+v", f.switches[switches:])
			}
			if tc.kept != nil && !tc.kept(botOf(t, store, "c-bot")) {
				t.Error("the stale save reverted the first save's choice")
			}
		})
	}
}

func TestCmdBotSave_StartStampsTheBotAndSendsTheKickoff(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, f := newBotRoles(t, store)

	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "CI watcher", Goal: "Keep CI green.", Memory: true, Start: true,
	})

	c := botOf(t, store, "c-bot")
	if c.Bot.StartedAt == 0 {
		t.Error("started_at is 0 after a start")
	}
	if c.Bot.Goal != "Keep CI green." || !c.Bot.Memory || c.Name != "CI watcher" || !c.NameSetByUser {
		t.Errorf("record = %q %q memory %v by user %v, want every field the start sent", c.Name, c.Bot.Goal, c.Bot.Memory, c.NameSetByUser)
	}
	if len(f.kickoffs) != 1 {
		t.Fatalf("kickoffs = %d, want 1", len(f.kickoffs))
	}
	k := f.kickoffs[0]
	if k.Text != botKickoffText {
		t.Errorf("kickoff text = %q, want the kickoff prompt", k.Text)
	}
	if k.DisplayText != "Started CI watcher" {
		t.Errorf("kickoff label = %q, want \"Started CI watcher\"", k.DisplayText)
	}
	if k.Caller != nil {
		t.Errorf("kickoff caller = %+v, want none: the user pressed Create bot", k.Caller)
	}
	if k.MessageID == "" {
		t.Error("the kickoff carries no message id")
	}
	if _, ok := body["kickoff_error"]; ok {
		t.Errorf("a sent kickoff answered kickoff_error %v", body["kickoff_error"])
	}
	dir, _ := bot.Dir("c-bot", roles.paths)
	if info, err := os.Stat(filepath.Join(dir, "memory.md")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("memory file: %v, want it created at the start", err)
	}
}

func TestCmdBotSave_AStartWithMemoryOffMakesNoFile(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, _ := newBotRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Quiet", Goal: "Watch.", Start: true})

	dir, _ := bot.Dir("c-bot", roles.paths)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Bot directory: %v, want none with memory off", err)
	}
}

func TestCmdBotSave_ARefusedKickoffLeavesTheBotStartedAndAnswersTheError(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, f := newBotRoles(t, store)
	f.kickoffErr = StatusError(http.StatusServiceUnavailable, errors.New("the agent engine is starting"))

	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Start: true})

	if botOf(t, store, "c-bot").Bot.StartedAt == 0 {
		t.Error("a refused kickoff left the Bot a draft")
	}
	if body["kickoff_error"] != "the agent engine is starting" {
		t.Errorf("kickoff_error = %v, want the refusal's text", body["kickoff_error"])
	}
}

func TestCmdBotSave_AStartOnAStartedBotSendsNoSecondKickoff(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 7})
	roles, f := newBotRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Start: true})

	if len(f.kickoffs) != 0 {
		t.Errorf("kickoffs = %d, want 0 on a started Bot", len(f.kickoffs))
	}
	if got := botOf(t, store, "c-bot").Bot.StartedAt; got != 7 {
		t.Errorf("started_at = %d, want the first start's 7 kept", got)
	}
}

func TestCmdBotSave_AnEditOfARoleFieldMarksAStartedBotForReload(t *testing.T) {
	cases := []struct {
		name string
		edit func(p *marotte.BotSaveCommand)
	}{
		{name: "the goal", edit: func(p *marotte.BotSaveCommand) { p.Goal = "A new goal." }},
		{name: "memory", edit: func(p *marotte.BotSaveCommand) { p.Memory = !p.Memory }},
		{name: "the steering", edit: func(p *marotte.BotSaveCommand) { p.Steering = "Be brief." }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
			roles, f := newBotRoles(t, store)
			p := &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch."}
			tc.edit(p)

			saveBot(t, roles, "c-bot", p)

			if !slices.Equal(f.reloads, []marotte.ChatID{"c-bot"}) {
				t.Errorf("reload marks = %v, want one for the Bot", f.reloads)
			}
		})
	}
}

func TestCmdBotSave_ADraftsEditMarksNoReload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, f := newBotRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Steering: "s", Memory: true})

	if len(f.reloads) != 0 || len(f.renames) != 0 {
		t.Errorf("reloads %v renames %v, want none: a draft has no session", f.reloads, f.renames)
	}
	if c := botOf(t, store, "c-bot"); c.Name != "Watcher" || !c.NameSetByUser {
		t.Errorf("name = %q by user %v, want the draft renamed by the user", c.Name, c.NameSetByUser)
	}
}

func TestCmdBotSave_ARenamedBotRenamesEverySessionAndMarksAReload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-old")
		c.RecordSession("sess-live")
		return true
	}); err != nil {
		t.Fatal(err)
	}
	roles, f := newBotRoles(t, store)
	f.renameErr = errors.New("KAS refused the rename")

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "  CI watcher\t", Goal: "Watch."})

	if c := botOf(t, store, "c-bot"); c.Name != "CI watcher" || !c.NameSetByUser {
		t.Errorf("name = %q by user %v, want the sanitized name set by the user", c.Name, c.NameSetByUser)
	}
	if want := []string{"sess-old=CI watcher", "sess-live=CI watcher"}; !slices.Equal(f.renames, want) {
		t.Errorf("KAS renames = %v, want %v", f.renames, want)
	}
	if !slices.Equal(f.reloads, []marotte.ChatID{"c-bot"}) {
		t.Errorf("reload marks = %v, want one", f.reloads)
	}
}

func TestCmdBotSave_ARegularChatIsNoBot(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedNamed(t, store, "c-chat", "Plain")
	roles, _ := newBotRoles(t, store)

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-chat", &marotte.BotSaveCommand{Name: "x", Goal: "y"}))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != "bot_not_found" {
		t.Errorf("status %d reason %q, want 409 bot_not_found", statusOf(err), reasonOf(err))
	}
	if c, _ := store.Get(t.Context(), "c-chat"); c.Bot != nil || c.Name != "Plain" {
		t.Errorf("record = %q bot %+v, want it unchanged", c.Name, c.Bot)
	}
}

func TestCmdBotSave_AModelOrEffortChangeGoesThroughTheModelSwitch(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
		c.Model, c.Effort = "model-a", "low"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	roles, f := newBotRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Model: "model-a", Effort: "low"})
	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Model: "model-b", Effort: "high"})

	want := []marotte.SwitchModelCommand{{Model: "model-b", Effort: "high"}}
	if !slices.Equal(f.switches, want) {
		t.Errorf("switches = %+v, want %+v: only a change switches", f.switches, want)
	}
}

func TestCmdBotSave_SupervisedIsWrittenOnlyWhenSent(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
		c.SupervisedMode = true
		return true
	}); err != nil {
		t.Fatal(err)
	}
	roles, _ := newBotRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch."})
	if c := botOf(t, store, "c-bot"); !c.SupervisedMode {
		t.Fatal("a save naming no supervised switch turned it off")
	}
	off := false
	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Supervised: &off, BaseVersion: 1})
	if c := botOf(t, store, "c-bot"); c.SupervisedMode {
		t.Error("supervised false was not written")
	}
}

func TestCmdBotSave_ARefusedModelSwitchSavesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, f := newBotRoles(t, store)
	f.switchErr = StatusError(http.StatusConflict, errors.New("that model is not available on this account"))
	on := true

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot",
		&marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Model: "model-z", Supervised: &on, Start: true}))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want the switch's 409", statusOf(err))
	}
	if c := botOf(t, store, "c-bot"); c.Name != "New Bot" || c.Bot.StartedAt != 0 || c.Bot.SpecVersion != 0 || c.SupervisedMode {
		t.Errorf("record = %q started %d version %d supervised %v, want it unchanged",
			c.Name, c.Bot.StartedAt, c.Bot.SpecVersion, c.SupervisedMode)
	}
	if len(f.kickoffs) != 0 {
		t.Errorf("a refused save sent %d kickoff(s)", len(f.kickoffs))
	}
}

func TestCmdBotSave_ASupervisedRefusalAfterAModelSwitchNamesWhatLanded(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, SpecVersion: 2})
	roles, f := newBotRoles(t, store)
	roles.bridges = newBridgeHost(store, &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"})
	on := true

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "A new goal.", Model: "model-b", Supervised: &on, BaseVersion: 2,
	}))

	if statusOf(err) != http.StatusBadGateway || reasonOf(err) != "bot_supervised" {
		t.Errorf("status %d reason %q, want 502 bot_supervised", statusOf(err), reasonOf(err))
	}
	if got := fieldsOf(err); !slices.Equal(got, []string{"supervised"}) {
		t.Errorf("fields = %v, want the supervised switch named", got)
	}
	if want := []marotte.SwitchModelCommand{{Model: "model-b"}}; !slices.Equal(f.switches, want) {
		t.Errorf("switches = %+v, want %+v", f.switches, want)
	}
	c := botOf(t, store, "c-bot")
	if c.SupervisedMode || c.Bot.Goal != "Watch." {
		t.Errorf("supervised %v goal %q, want neither saved", c.SupervisedMode, c.Bot.Goal)
	}
	if c.Bot.SpecVersion != 3 {
		t.Errorf("spec_version = %d, want 3: the model pick landed", c.Bot.SpecVersion)
	}
}

func TestCmdBotSave_ARefusedSupervisedSwitchAloneSavesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, SpecVersion: 2})
	roles, f := newBotRoles(t, store)
	roles.bridges = newBridgeHost(store, &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"})
	on := true

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "A new goal.", Supervised: &on, BaseVersion: 2,
	}))

	if err == nil || reasonOf(err) == "bot_supervised" {
		t.Errorf("err = %v reason %q, want the switch's own refusal: nothing landed", err, reasonOf(err))
	}
	if len(f.switches) != 0 {
		t.Errorf("switches = %+v, want none", f.switches)
	}
	c := botOf(t, store, "c-bot")
	if c.SupervisedMode || c.Bot.Goal != "Watch." || c.Bot.SpecVersion != 2 {
		t.Errorf("supervised %v goal %q version %d, want the record unchanged",
			c.SupervisedMode, c.Bot.Goal, c.Bot.SpecVersion)
	}
}

func TestCmdBotSave_TheRefusalBodyNamesEachBadField(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{})
	roles, _ := newBotRoles(t, store)
	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Start: true}))
	rec := httptest.NewRecorder()

	writeError(rec, err)

	var body struct {
		Reason string `json:"reason"`
		Fields []struct {
			Field string `json:"field"`
			Error string `json:"error"`
		} `json:"fields"`
	}
	if jErr := json.Unmarshal(rec.Body.Bytes(), &body); jErr != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), jErr)
	}
	if rec.Code != http.StatusBadRequest || body.Reason != "bot_invalid" {
		t.Errorf("answer %d reason %q, want 400 bot_invalid", rec.Code, body.Reason)
	}
	if len(body.Fields) != 1 || body.Fields[0].Field != "goal" || body.Fields[0].Error == "" {
		t.Errorf("fields = %+v, want the goal with its reason", body.Fields)
	}
}
