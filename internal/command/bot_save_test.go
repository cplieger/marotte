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
	"time"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// botFakes records what bot_save hands its collaborators: reload marks, KAS renames, model switches and kickoffs.
type botFakes struct {
	kickoffErr error
	store      chatStore
	renameErr  error
	switchErr  error
	// land is what an accepted switch writes onto the record through BotWriter, as switch_model does; nil writes
	// nothing, as a pick already in force does.
	land func(c *marotte.Chat, p marotte.SwitchModelCommand)
	// onRename runs before a KAS rename is recorded.
	onRename func(title string)
	reloads  []marotte.ChatID
	renames  []string
	switches []marotte.SwitchModelCommand
	kickoffs []*marotte.PromptCommand
	mu       sync.Mutex
}

func (f *botFakes) MarkBotReload(id marotte.ChatID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads = append(f.reloads, id)
}

func (f *botFakes) RenameSession(_ context.Context, sessionID, title string) error {
	f.mu.Lock()
	hook := f.onRename
	f.mu.Unlock()
	if hook != nil {
		hook(title)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, sessionID+"="+title)
	return f.renameErr
}

func (f *botFakes) SwitchModel(ctx context.Context, chatID marotte.ChatID, p marotte.SwitchModelCommand) error {
	f.mu.Lock()
	f.switches = append(f.switches, p)
	err, land := f.switchErr, f.land
	f.mu.Unlock()
	if err != nil || land == nil {
		return err
	}
	_, mErr := f.store.Mutate(ctx, chatID, marotte.BotWriter(func(c *marotte.Chat, _ bool) bool {
		land(c, p)
		return true
	}))
	return mErr
}

// landPick is switch_model on a Bot with no live session: the pick lands on the record at once, and a tier picked
// while thinking is off turns it on.
func landPick(c *marotte.Chat, p marotte.SwitchModelCommand) {
	c.MoveToModel(cmp.Or(p.Model, c.Model))
	c.PendingModel, c.PendingEffort = "", ""
	switch p.Effort {
	case "":
	case marotte.SwitchEffortOff:
		c.Thinking = marotte.ThinkingOff
	default:
		c.Effort = string(p.Effort)
		if c.Thinking == marotte.ThinkingOff {
			c.Thinking = marotte.ThinkingOn
		}
	}
}

// parkPick is switch_model behind a running turn: a pick of another model is parked with its tier.
func parkPick(c *marotte.Chat, p marotte.SwitchModelCommand) {
	if p.Model != c.Model {
		c.PendingModel, c.PendingEffort = p.Model, string(p.Effort)
	}
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
	f := &botFakes{store: store}
	return &botRoles{
		chats:           store,
		bridges:         newTestHost(t, store),
		renamer:         f,
		reload:          f,
		models:          f,
		kickoff:         f.kickoff,
		paths:           bot.Paths{KiroHome: t.TempDir()},
		supervisedLocks: keylock.New(cmp.Compare[marotte.ChatID]),
		writes:          keylock.New(cmp.Compare[marotte.ChatID]),
		replays:         newOpLedger[rulesReplay](),
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

func TestCmdBotSave_ARenameArrivingDuringASaveWaitsAndLandsAfterIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Old goal", StartedAt: 1, SpecVersion: 3})
		racing := &saveAfterRead{chatStore: store}
		roles, f := newBotRoles(t, racing)
		d := New()
		d.records = store
		d.register(marotte.CmdRenameChat, d.botWrite(bind3(chatStore(store), SessionRenamer(f), BotReloader(f), cmdRenameChat)))
		roles.writes = d.botWrites
		rename, _ := d.handler(marotte.CmdRenameChat)
		var renameErr error
		done := make(chan struct{})
		racing.other = func() {
			go func() {
				defer close(done)
				_, renameErr = rename(t.Context(), renameReq(t, "c-bot", "CI watcher"))
			}()
			synctest.Wait()
		}

		body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "My goal", BaseVersion: 3})
		<-done

		if body["spec_version"] != 4 || renameErr != nil {
			t.Errorf("save answered spec_version %v, rename %v; want 4 and the rename applied after", body["spec_version"], renameErr)
		}
		if c := botOf(t, store, "c-bot"); c.Name != "CI watcher" || c.Bot.Goal != "My goal" || c.Bot.SpecVersion != 5 {
			t.Errorf("name %q goal %q version %d, want the save's goal then the rename, at 5", c.Name, c.Bot.Goal, c.Bot.SpecVersion)
		}
	})
}

func TestCmdBotSave_AStartsHeldKickoffKeepsNoBotCommandWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{SpecVersion: 1})
		if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool { c.Model = "model-a"; return true }); err != nil {
			t.Fatalf("Setup: the Bot's model: %v", err)
		}
		roles, f := newBotRoles(t, store)
		d := New()
		d.records = store
		roles.writes = d.botWrites
		host := newTestHost(t, store)
		rename := d.botWrite(bind3(chatStore(store), SessionRenamer(f), BotReloader(f), cmdRenameChat))
		effort := d.botWrite(bind6(bridgeAccess(host), chatStore(store), broadcaster(host), Workspace{}, effortRecorder(host),
			keylock.New(cmp.Compare[marotte.ChatID]), cmdSetEffort))
		kicked, release := make(chan struct{}), make(chan struct{})
		roles.kickoff = func(context.Context, marotte.ChatID, *marotte.PromptCommand) error {
			close(kicked)
			<-release
			return nil
		}
		go func() {
			_, _ = cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
				Name: "Watcher", Goal: "Keep CI green.", Start: true, BaseVersion: 1,
			}))
		}()
		<-kicked
		version := botOf(t, store, "c-bot").Bot.SpecVersion
		answers := map[string]error{}
		var mu sync.Mutex
		run := func(name string, h Handler, cmd *marotte.ClientCommand) {
			go func() {
				_, err := h(t.Context(), cmd)
				mu.Lock()
				answers[name] = err
				mu.Unlock()
			}()
		}

		run("bot_save", func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) { return cmdBotSave(ctx, roles, cmd) },
			botSaveReq(t, "c-bot", &marotte.BotSaveCommand{Name: "Watcher", Goal: "Keep main green.", BaseVersion: version}))
		synctest.Wait()
		run("rename_chat", rename, renameReq(t, "c-bot", "CI watcher"))
		synctest.Wait()
		run("set_effort", effort, effortReq(t, "c-bot", "high"))
		synctest.Wait()

		mu.Lock()
		for _, name := range []string{"bot_save", "rename_chat", "set_effort"} {
			switch err, ok := answers[name]; {
			case !ok:
				t.Errorf("%s waited on the start's held kickoff, want it answered at once", name)
			case err != nil:
				t.Errorf("%s = %d %v beside the start's held kickoff, want 200", name, statusOf(err), err)
			}
		}
		mu.Unlock()
		close(release)
	})
}

func TestCmdBotSave_ARenameArrivingDuringTheSavesSessionRenamesTitlesTheSessionLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, SpecVersion: 3})
		if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
			c.RecordSession("sess-live")
			return true
		}); err != nil {
			t.Fatalf("Setup: the Bot's session: %v", err)
		}
		roles, f := newBotRoles(t, store)
		d := New()
		d.records = store
		roles.writes = d.botWrites
		rename := d.botWrite(bind3(chatStore(store), SessionRenamer(f), BotReloader(f), cmdRenameChat))
		var renameErr error
		done := make(chan struct{})
		var once sync.Once
		f.onRename = func(title string) {
			if title != "CI watcher" {
				return
			}
			once.Do(func() {
				go func() {
					defer close(done)
					_, renameErr = rename(t.Context(), renameReq(t, "c-bot", "Nightly watcher"))
				}()
				synctest.Wait()
			})
		}

		saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{Name: "CI watcher", Goal: "Watch.", BaseVersion: 3})
		<-done

		if renameErr != nil {
			t.Fatalf("rename_chat = %v, want it applied after the save", renameErr)
		}
		if want := []string{"sess-live=CI watcher", "sess-live=Nightly watcher"}; !slices.Equal(f.renames, want) {
			t.Errorf("KAS renames = %v, want %v: the later rename titles the session last", f.renames, want)
		}
		if c := botOf(t, store, "c-bot"); c.Name != "Nightly watcher" {
			t.Errorf("name = %q, want the later rename's", c.Name)
		}
	})
}

func TestDispatcher_ABotWriteRunInProcessDoesNotWaitForTheLockItsCallerHolds(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	d := New()
	d.records = store
	ran := false
	d.RegisterBotWriteChange(marotte.CmdRenameChat, func(context.Context, *marotte.ClientCommand) (any, error) {
		ran = true
		return nil, nil
	})
	unlock, err := d.botWrites.Lock(t.Context(), "c-bot")
	if err != nil {
		t.Fatalf("Setup: lock: %v", err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := d.Run(ctx, &marotte.ClientCommand{Type: marotte.CmdRenameChat, ChatID: "c-bot"}); err != nil || !ran {
		t.Errorf("Run under the caller's lock = %v ran %v, want it run at once", err, ran)
	}
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

// equalBotPayload is the save that sends back exactly what seedEqualBot stored.
func equalBotPayload() *marotte.BotSaveCommand {
	return &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", Steering: "Be brief.", Memory: true, Model: "model-a", Effort: "low"}
}

// seedEqualBot writes the started Bot equalBotPayload describes, then applies edit to its record.
func seedEqualBot(t *testing.T, store chatStore, edit func(c *marotte.Chat)) {
	t.Helper()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", Steering: "Be brief.", Memory: true, StartedAt: 1})
	if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
		c.Model, c.Effort = "model-a", "low"
		if edit != nil {
			edit(c)
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCmdBotSave_AnEqualSaveKeepsTheSpecVersionAndAChangeBumpsItOnce(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		// seed edits the stored Bot; equal edits the save so it still states that Bot; change edits it to a change.
		seed          func(c *marotte.Chat)
		land          func(c *marotte.Chat, p marotte.SwitchModelCommand)
		equal, change func(p *marotte.BotSaveCommand)
	}{
		{name: "the name", change: func(p *marotte.BotSaveCommand) { p.Name = "CI watcher" }},
		{name: "the goal", change: func(p *marotte.BotSaveCommand) { p.Goal = "Watch closely." }},
		{name: "the steering", change: func(p *marotte.BotSaveCommand) { p.Steering = "" }},
		{name: "memory", change: func(p *marotte.BotSaveCommand) { p.Memory = false }},
		{
			name:   "the start",
			seed:   func(c *marotte.Chat) { c.Bot.StartedAt = 0 },
			change: func(p *marotte.BotSaveCommand) { p.Start = true },
		},
		{
			name:   "the model",
			land:   landPick,
			equal:  func(p *marotte.BotSaveCommand) { p.Model = "" },
			change: func(p *marotte.BotSaveCommand) { p.Model = "model-b" },
		},
		{
			name:   "the effort",
			land:   landPick,
			equal:  func(p *marotte.BotSaveCommand) { p.Effort = "" },
			change: func(p *marotte.BotSaveCommand) { p.Effort = "high" },
		},
		{
			name:   "thinking turned off",
			land:   landPick,
			change: func(p *marotte.BotSaveCommand) { p.Effort = string(marotte.SwitchEffortOff) },
		},
		{
			name:   "Off on a Bot whose thinking is off",
			seed:   func(c *marotte.Chat) { c.Thinking = marotte.ThinkingOff },
			equal:  func(p *marotte.BotSaveCommand) { p.Effort = string(marotte.SwitchEffortOff) },
			change: func(p *marotte.BotSaveCommand) { p.Effort = "high" },
		},
		{
			name:   "a parked pick sent back",
			seed:   func(c *marotte.Chat) { c.PendingModel, c.PendingEffort = "model-b", "high" },
			land:   parkPick,
			equal:  func(p *marotte.BotSaveCommand) { p.Model, p.Effort = "model-b", "high" },
			change: func(p *marotte.BotSaveCommand) { p.Model = "model-c" },
		},
		{
			name:   "supervised",
			equal:  func(p *marotte.BotSaveCommand) { p.Supervised = &off },
			change: func(p *marotte.BotSaveCommand) { p.Supervised = &on },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEqualBot(t, store, tc.seed)
			roles, f := newBotRoles(t, store)
			f.land = landPick
			if tc.land != nil {
				f.land = tc.land
			}
			equal := equalBotPayload()
			if tc.equal != nil {
				tc.equal(equal)
			}

			if body := saveBot(t, roles, "c-bot", equal); body["spec_version"] != 0 || botOf(t, store, "c-bot").Bot.SpecVersion != 0 {
				t.Errorf("an equal save answered spec_version %v, stored %d, want 0 kept",
					body["spec_version"], botOf(t, store, "c-bot").Bot.SpecVersion)
			}
			change := equalBotPayload()
			tc.change(change)
			if body := saveBot(t, roles, "c-bot", change); body["spec_version"] != 1 {
				t.Errorf("a change answered spec_version %v, want 1", body["spec_version"])
			}
			change.BaseVersion = 1
			if body := saveBot(t, roles, "c-bot", change); body["spec_version"] != 1 || botOf(t, store, "c-bot").Bot.SpecVersion != 1 {
				t.Errorf("the change sent again answered spec_version %v, stored %d, want 1 kept",
					body["spec_version"], botOf(t, store, "c-bot").Bot.SpecVersion)
			}
		})
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
			f.land = landPick
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
	if len(f.reloads) != 0 {
		t.Errorf("reload marks = %v, want none: the kickoff opens the Bot's first session on its role", f.reloads)
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

func TestCmdBotSave_AnEffortPickKeepsTheModelPickedBehindATurn(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEqualBot(t, store, func(c *marotte.Chat) { c.PendingModel, c.PendingEffort = "model-b", "high" })
	roles, f := newBotRoles(t, store)
	p := equalBotPayload()
	p.Model, p.Effort = "", "max"

	saveBot(t, roles, "c-bot", p)

	if want := []marotte.SwitchModelCommand{{Model: "model-b", Effort: "max"}}; !slices.Equal(f.switches, want) {
		t.Errorf("switches = %+v, want %+v: an empty model keeps the one picked", f.switches, want)
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
	f.land = landPick
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

func TestCmdBotSave_ANoOpPickBesideARefusedSupervisedSwitchAnswersThePlainRefusal(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEqualBot(t, store, func(c *marotte.Chat) {
		c.Thinking = marotte.ThinkingOff
		c.Bot.SpecVersion = 2
	})
	roles, _ := newBotRoles(t, store)
	roles.bridges = newBridgeHost(store, &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"})
	on := true
	p := equalBotPayload()
	p.Effort, p.Supervised, p.BaseVersion = string(marotte.SwitchEffortOff), &on, 2

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", p))

	if err == nil || reasonOf(err) == "bot_supervised" {
		t.Errorf("err = %v reason %q, want the switch's own refusal: the pick changed nothing", err, reasonOf(err))
	}
	if c := botOf(t, store, "c-bot"); c.SupervisedMode || c.Bot.SpecVersion != 2 {
		t.Errorf("supervised %v version %d, want the record unchanged at 2", c.SupervisedMode, c.Bot.SpecVersion)
	}
}

// stopThenRefuse is switch_model whose Off stop lands on the live session through set_thinking's BotWriter and whose
// park then fails.
type stopThenRefuse struct{ store chatStore }

var errParkFailed = StatusError(http.StatusInternalServerError, errors.New("write pending_model failed"))

func (s stopThenRefuse) SwitchModel(ctx context.Context, chatID marotte.ChatID, _ marotte.SwitchModelCommand) error {
	if _, err := s.store.Mutate(ctx, chatID, marotte.BotWriter(func(c *marotte.Chat, _ bool) bool {
		c.Thinking = marotte.ThinkingOff
		return true
	})); err != nil {
		return err
	}
	return errParkFailed
}

func TestCmdBotSave_APickRefusedAfterItsStopLandedBumpsAndAnswersTheSwitchsError(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEqualBot(t, store, nil)
	roles, f := newBotRoles(t, store)
	roles.models = stopThenRefuse{store: store}
	p := equalBotPayload()
	p.Model, p.Effort, p.Goal = "model-b", string(marotte.SwitchEffortOff), "A new goal."

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", p))

	if !errors.Is(err, errParkFailed) || reasonOf(err) == "bot_supervised" {
		t.Errorf("err = %v reason %q, want the switch's own error", err, reasonOf(err))
	}
	c := botOf(t, store, "c-bot")
	if c.Bot.SpecVersion != 1 || c.Bot.Goal != "Watch." {
		t.Errorf("version %d goal %q, want 1 for the landed stop and the goal unsaved", c.Bot.SpecVersion, c.Bot.Goal)
	}
	if len(f.kickoffs) != 0 {
		t.Errorf("a refused save sent %d kickoff(s)", len(f.kickoffs))
	}
}

// mutateRefusingStore reads through and refuses every write.
type mutateRefusingStore struct {
	*testsupport.InMemoryChatStore
}

var errWriteRefused = errors.New("simulated record write failure")

func (*mutateRefusingStore) Mutate(context.Context, marotte.ChatID, func(*marotte.Chat, bool) bool) (string, error) {
	return "", errWriteRefused
}

func TestCmdBotSave_AFailedWriteAfterARefusedSupervisedSwitchAnswersTheWriteFailure(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, SpecVersion: 2})
	roles, f := newBotRoles(t, store)
	f.land = landPick
	roles.chats = &mutateRefusingStore{InMemoryChatStore: store}
	roles.bridges = newBridgeHost(store, &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"})
	on := true

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.", Model: "model-b", Supervised: &on, BaseVersion: 2,
	}))

	if statusOf(err) != http.StatusInternalServerError || !errors.Is(err, errWriteRefused) {
		t.Errorf("err = %v status %d, want the 500 write failure", err, statusOf(err))
	}
}

func TestCmdBotSave_ASupervisedValueAlreadyStoredSendsNoSwitch(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEqualBot(t, store, nil)
	roles, _ := newBotRoles(t, store)
	bridge := &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"}
	roles.bridges = newBridgeHost(store, bridge)
	off := false
	p := equalBotPayload()
	p.Goal, p.Supervised = "A new goal.", &off

	if _, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", p)); err != nil {
		t.Fatalf("save: %v, want the goal saved without a supervised switch", err)
	}
	if c := botOf(t, store, "c-bot"); c.Bot.Goal != "A new goal." || c.Bot.SpecVersion != 1 {
		t.Errorf("goal %q version %d, want the new goal at 1", c.Bot.Goal, c.Bot.SpecVersion)
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
