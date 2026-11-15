package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

const (
	reasonBotDraft    = "bot_draft"
	reasonBotNotFound = "bot_not_found"
	reasonBotChanged  = "bot_changed"
	reasonBotInvalid  = "bot_invalid"
	reasonBotTab      = "bot_tab"
	reasonMemoryOff   = "memory_off"
)

const (
	maxBotGoalBytes     = 8 << 10
	maxBotSteeringBytes = 64 << 10
)

// botKickoffText is the first prompt a Bot gets, sent as the user's own when Create bot starts it.
const botKickoffText = "You have just been set up. Read your goal, say in a few lines how you will work toward it " +
	"and what you will check each time a rule prompts you, then start."

var (
	errBotNotFound = errors.New("this chat is not a Bot")
	errBotChanged  = errors.New("this Bot was changed elsewhere; reload its setup and save again")
	errBotInvalid  = errors.New("some of this Bot's setup cannot be saved")
	errBotTab      = errors.New("a Bot's tab is always top level and never pinned")
	errMemoryOff   = errors.New("this Bot keeps no memory file")
)

// BotDraftRefusal answers marotte.ErrBotDraft as 409 bot_draft when c is a Bot not yet started, else nil: a draft
// has no role to run a session, prompt or fork on.
func BotDraftRefusal(c *marotte.Chat) error {
	if c == nil || c.Bot == nil || c.Bot.StartedAt != 0 {
		return nil
	}
	return StatusErrorReason(http.StatusConflict, reasonBotDraft, marotte.ErrBotDraft)
}

// startedBotRefusal answers 409 bot_not_found for a missing record or one with no bot block, 409 bot_draft for a
// draft, else nil.
func startedBotRefusal(c *marotte.Chat, exists bool) error {
	if !exists || c == nil || c.Bot == nil {
		return StatusErrorReason(http.StatusConflict, reasonBotNotFound, errBotNotFound)
	}
	return BotDraftRefusal(c)
}

// refuseBotDraft is BotDraftRefusal over chatID's record; a missing or unreadable one is left to the caller's own path.
func refuseBotDraft(ctx context.Context, chats chatStore, chatID marotte.ChatID) error {
	c, _ := chats.Get(ctx, chatID)
	return BotDraftRefusal(c)
}

// modelSwitcher applies a model or effort pick the way the model pill's switch_model does.
type modelSwitcher interface {
	SwitchModel(ctx context.Context, chatID marotte.ChatID, p marotte.SwitchModelCommand) error
}

// botRoles are the Bot commands' collaborators.
type botRoles struct {
	chats   chatStore
	bridges bridgeAccess
	renamer SessionRenamer
	reload  BotReloader
	models  modelSwitcher
	// kickoff sends a prompt through the dispatcher's prompt command, as the page's Send does.
	kickoff func(ctx context.Context, chatID marotte.ChatID, p *marotte.PromptCommand) error
	// supervisedLocks is set_supervised_mode's own lock set, so a save and the switch never interleave.
	supervisedLocks *keylock.Set[marotte.ChatID]
	paths           bot.Paths
}

// botSave is a validated bot_save payload.
type botSave struct {
	supervised *bool
	name       string
	goal       string
	steering   string
	model      string
	effort     marotte.EffortLevel
	base       int
	memory     bool
	start      bool
}

// cmdBotSave writes a Bot's setup in one record write and applies what the write changed: a renamed started Bot's
// KAS sessions, a reload mark when the role's text moved, and on a draft's start the memory file and the kickoff
// prompt. Supervised and the model go through their own commands' paths first, so a refused pick saves nothing.
func cmdBotSave(ctx context.Context, roles *botRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.BotSaveCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	s, bad := validBotSave(&p)
	if len(bad) > 0 {
		return nil, statusErrorFields(http.StatusBadRequest, reasonBotInvalid, bad, errBotInvalid)
	}
	cur, ok := roles.chats.Get(ctx, cmd.ChatID)
	if !ok || cur.Bot == nil {
		return nil, StatusErrorReason(http.StatusConflict, reasonBotNotFound, errBotNotFound)
	}
	if cur.Bot.SpecVersion != s.base {
		return nil, StatusErrorReason(http.StatusConflict, reasonBotChanged, errBotChanged)
	}
	if err := applyBotChatChoices(ctx, roles, cmd.ChatID, cur, &s); err != nil {
		return nil, err
	}
	w, err := writeBotSave(ctx, roles.chats, cmd.ChatID, &s)
	if err != nil {
		return nil, err
	}
	return responseWith(afterBotSave(ctx, roles, cmd.ChatID, &w)), nil
}

// afterBotSave applies what a save's write changed and answers the save's body.
func afterBotSave(ctx context.Context, roles *botRoles, chatID marotte.ChatID, w *botWrite) map[string]any {
	if w.started && w.nameChanged {
		renameSessions(ctx, roles.renamer, chatID, w.chain, w.rec.Name)
	}
	if w.started && !w.startedNow && (w.nameChanged || w.roleChanged) {
		roles.reload.MarkBotReload(chatID)
	}
	out := map[string]any{"chat": w.header, "spec_version": w.version}
	if w.startedNow {
		if msg := startBot(ctx, roles, chatID, w.rec); msg != "" {
			out["kickoff_error"] = msg
		}
	}
	return out
}

// validBotSave trims and checks p, naming every field it refuses.
func validBotSave(p *marotte.BotSaveCommand) (botSave, []fieldError) {
	var bad []fieldError
	refuse := func(field string, err error) { bad = append(bad, fieldError{Field: field, Error: err.Error()}) }
	name, err := userChatName(p.Name)
	if err != nil {
		refuse("name", err)
	}
	if err := botText(p.Goal, maxBotGoalBytes); err != nil {
		refuse("goal", err)
	} else if p.Start && strings.TrimSpace(p.Goal) == "" {
		refuse("goal", errBotNoGoal)
	}
	if err := botText(p.Steering, maxBotSteeringBytes); err != nil {
		refuse("steering", err)
	}
	if !ids.ValidIdent(p.Model) {
		refuse("model", ErrInvalidPayload)
	}
	effort := marotte.EffortLevel(p.Effort)
	if effort != "" && effort != marotte.SwitchEffortOff && !effort.Valid() {
		refuse("effort", ErrInvalidPayload)
	}
	return botSave{
		supervised: p.Supervised, name: name, goal: p.Goal, steering: p.Steering, model: p.Model, effort: effort,
		base: p.BaseVersion, memory: p.Memory, start: p.Start,
	}, bad
}

var (
	errBotNoGoal    = errors.New("a goal is needed to create the Bot")
	errBotTextLong  = errors.New("this text is too long")
	errBotTextRunes = errors.New("this text holds a character it cannot keep")
)

// botText refuses text past limit bytes or holding a control character other than a newline or tab. The JSON decode
// already replaced any invalid UTF-8.
func botText(text string, limit int) error {
	if len(text) > limit {
		return errBotTextLong
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return errBotTextRunes
		}
	}
	return nil
}

// applyBotChatChoices applies the save's supervised switch and model pick where they differ from cur.
func applyBotChatChoices(ctx context.Context, roles *botRoles, chatID marotte.ChatID, cur *marotte.Chat, s *botSave) error {
	if s.supervised != nil && *s.supervised != cur.SupervisedMode {
		if err := applySupervised(ctx, roles.bridges, roles.chats, roles.supervisedLocks, chatID, *s.supervised); err != nil {
			return err
		}
	}
	if (s.model != "" && s.model != cur.Model) || (s.effort != "" && string(s.effort) != cur.Effort) {
		return roles.models.SwitchModel(ctx, chatID, marotte.SwitchModelCommand{Model: s.model, Effort: s.effort})
	}
	return nil
}

// botWrite is what writeBotSave changed.
type botWrite struct {
	rec         *marotte.Chat
	chain       []string
	header      marotte.ChatHeader
	version     int
	started     bool
	startedNow  bool
	nameChanged bool
	roleChanged bool
}

// writeBotSave writes s onto chatID's Bot block, refusing a record that stopped being a Bot or moved past s.base since
// the caller read it. Only a change bumps SpecVersion.
func writeBotSave(ctx context.Context, chats chatStore, chatID marotte.ChatID, s *botSave) (botWrite, error) {
	var w botWrite
	var refused error
	if _, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		switch {
		case !exists || c.Bot == nil:
			refused = StatusErrorReason(http.StatusConflict, reasonBotNotFound, errBotNotFound)
			return false
		case c.Bot.SpecVersion != s.base:
			refused = StatusErrorReason(http.StatusConflict, reasonBotChanged, errBotChanged)
			return false
		}
		b := *c.Bot
		w.roleChanged = b.Goal != s.goal || b.Memory != s.memory || b.Steering != s.steering
		w.startedNow = s.start && b.StartedAt == 0
		w.nameChanged = setUserName(c, s.name)
		changed := w.nameChanged || w.roleChanged || w.startedNow
		if changed {
			b.Goal, b.Memory, b.Steering = s.goal, s.memory, s.steering
			if w.startedNow {
				b.StartedAt = time.Now().UnixMilli()
			}
			b.SpecVersion++
			c.Bot = &b
		}
		w.started = b.StartedAt != 0
		w.version = b.SpecVersion
		w.chain = c.SessionChain()
		rec := *c
		w.rec = &rec
		w.header = c.Header()
		return changed
	}); err != nil {
		return w, StatusError(http.StatusInternalServerError, err)
	}
	return w, refused
}

// startBot makes a just-started Bot's memory file and sends its kickoff, answering the kickoff's refusal as text, or
// "" once sent. The Bot stays started either way: its first rule to fire opens its session too.
func startBot(ctx context.Context, roles *botRoles, chatID marotte.ChatID, rec *marotte.Chat) string {
	if rec.Bot.Memory {
		if err := bot.EnsureMemory(rec, roles.paths); err != nil {
			slog.Warn("bots: a started Bot's memory file could not be made; its first open tries again",
				"chat_id", chatID, keyError, err)
		}
	}
	err := roles.kickoff(ctx, chatID, &marotte.PromptCommand{
		Text: botKickoffText, MessageID: "m-" + ids.NewMessageID(), DisplayText: "Started " + rec.Name,
	})
	if err != nil {
		slog.Warn("bots: a started Bot's first message was not sent", "chat_id", chatID, keyError, err)
		return rpcerr.Text(err)
	}
	slog.Info("bots: Bot started", "chat_id", chatID)
	return ""
}

// cmdBotSetPaused pauses or resumes a started Bot's rules. A closed Bot keeps closed_at either way, and the state it
// already holds writes nothing.
func cmdBotSetPaused(ctx context.Context, roles *botRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.BotSetPausedCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	var refused error
	changed := false
	if _, err := roles.chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if refused = startedBotRefusal(c, exists); refused != nil || c.Bot.Paused == p.Paused {
			return false
		}
		b := *c.Bot
		b.Paused = p.Paused
		c.Bot = &b
		changed = true
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if refused != nil {
		return nil, refused
	}
	if changed {
		slog.Info("bots: Bot paused or resumed", "chat_id", cmd.ChatID, "paused", p.Paused)
	}
	return responseWith(nil), nil
}

// cmdBotClearMemory rewrites a started Bot's memory file to its starting text; 404 memory_off while memory is off.
func cmdBotClearMemory(ctx context.Context, roles *botRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	c, ok := roles.chats.Get(ctx, cmd.ChatID)
	if err := startedBotRefusal(c, ok); err != nil {
		return nil, err
	}
	if !c.Bot.Memory {
		return nil, StatusErrorReason(http.StatusNotFound, reasonMemoryOff, errMemoryOff)
	}
	if err := bot.ClearMemory(c, roles.paths); err != nil {
		slog.Warn("bots: a Bot's memory file could not be cleared", "chat_id", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("bots: Bot memory cleared", "chat_id", cmd.ChatID)
	return responseWith(nil), nil
}
