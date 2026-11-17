package command

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
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
	reasonSupervised  = "bot_supervised"
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
	errSupervised  = errors.New("the model was switched, but supervised mode could not be changed")
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

// refuseBotDraft is BotDraftRefusal over chatID's record, 503 when it cannot be read; a missing one is left to the
// caller's own path.
func refuseBotDraft(ctx context.Context, chats chatStore, chatID marotte.ChatID) error {
	c, _, err := chats.Lookup(ctx, chatID)
	if err != nil {
		return StatusError(http.StatusServiceUnavailable, fmt.Errorf("%w: %w", errChatUnread, err))
	}
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
	// writes is the dispatcher's botWrites set, held from this save's read through its write (lockedBotSave).
	writes *keylock.Set[marotte.ChatID]
	// rules is the rule engine, nil until composition wires it (Dispatcher.SetRuleEngine).
	rules RuleEngine
	// replays answers a bot_save's op_id again, keyed by chat and op; written under the Bot's writes lock.
	replays *opLedger[rulesReplay]
	paths   bot.Paths
}

// botSave is a validated bot_save payload.
type botSave struct {
	supervised *bool
	// rules is nil when the save sends none, which keeps the stored rules; plan is them checked against the record.
	rules    *rulesSave
	name     string
	goal     string
	steering string
	model    string
	effort   marotte.EffortLevel
	plan     rulePlan
	base     int
	memory   bool
	start    bool
}

// cmdBotSave writes a Bot's setup, its rules included, in one record write and applies what the save changed: a
// renamed started Bot's KAS sessions, a reload mark when the role's text moved, the rule engine's reindex, and on a
// draft's start the memory file and the kickoff prompt. A replayed op_id answers its first save's ids. The model switch, the supervised switch and the save's own write each move spec_version when they change
// the Bot's view, so the answered version is above every one published during the save. A stale base_version, or a
// model pick the switch refuses, saves nothing. A supervised switch refused after a model pick that changed the
// record answers bot_supervised naming the field and keeps that pick with spec_version bumped. bot_supervised,
// whatever its status, leaves the base stale, and so does a refused model pick whose stop already landed.
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
	w, err := lockedBotSave(ctx, roles, cmd.ChatID, &s)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"chat": w.header, "spec_version": w.version}
	if s.rules != nil {
		out["rule_ids"], out["trigger_ids"] = w.replay.ruleIDs, w.replay.triggerIDs
	}
	if w.changes.has(fieldStarted) {
		if msg := startBot(ctx, roles, cmd.ChatID, w.rec); msg != "" {
			out["kickoff_error"] = msg
		}
	}
	return responseWith(out), nil
}

// lockedBotSave runs s from the base check through its write and the session renames under the Bot's botWrites
// lock, and releases it before the start's kickoff, which waits on kiro-cli and writes nothing a save checks.
func lockedBotSave(ctx context.Context, roles *botRoles, chatID marotte.ChatID, s *botSave) (botWrite, error) {
	unlock, lockErr := lockSessionChange(ctx, roles.writes, chatID)
	if lockErr != nil {
		return botWrite{}, lockErr
	}
	defer unlock()
	cur, ok := roles.chats.Get(ctx, chatID)
	if !ok || cur.Bot == nil {
		return botWrite{}, StatusErrorReason(http.StatusConflict, reasonBotNotFound, errBotNotFound)
	}
	key := replayKey(chatID, s)
	if r, seen := roles.replays.peek(key); seen {
		return botWrite{rec: cur, header: cur.Header(), version: r.version, replay: r}, nil
	}
	if err := planBotRules(cur, s); err != nil {
		return botWrite{}, err
	}
	if cur.Bot.SpecVersion != s.base {
		return botWrite{}, StatusErrorReason(http.StatusConflict, reasonBotChanged, errBotChanged)
	}
	read := cur.BotView()
	if err := pickBotModel(ctx, roles.models, chatID, &read, s); err != nil {
		return botWrite{}, refusedChoice(ctx, roles.chats, chatID, &read, err, false)
	}
	if s.supervised != nil && *s.supervised != read.Supervised {
		if err := applySupervised(ctx, roles.bridges, roles.chats, roles.supervisedLocks, chatID, *s.supervised); err != nil {
			return botWrite{}, refusedChoice(ctx, roles.chats, chatID, &read, err, true)
		}
	}
	w, err := writeBotSave(ctx, roles.chats, chatID, &read, s)
	if err != nil {
		return botWrite{}, err
	}
	if s.rules != nil {
		w.replay = rulesReplay{ruleIDs: s.plan.ruleIDs, triggerIDs: s.plan.triggerIDs, version: w.version}
		roles.replays.put(key, w.replay)
	}
	applyBotSave(ctx, roles, chatID, &w)
	return w, nil
}

// applyBotSave logs a save's changes, renames a renamed started Bot's sessions and marks an edited one for reload.
func applyBotSave(ctx context.Context, roles *botRoles, chatID marotte.ChatID, w *botWrite) {
	if len(w.changes) > 0 {
		slog.Info("bots: Bot saved", "chat_id", chatID, "changed", w.changes, "spec_version", w.version)
	}
	if w.rulesWritten {
		reindex(roles.rules, chatID)
	}
	if w.rec.Bot.StartedAt == 0 {
		return
	}
	if w.changes.has(fieldName) {
		renameSessions(ctx, roles.renamer, chatID, w.chain, w.rec.Name)
	}
	if !w.changes.has(fieldStarted) && w.changes.has(fieldName, fieldGoal, fieldSteering, fieldMemory) {
		roles.reload.MarkBotReload(chatID)
	}
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
		rules:      decodeRulesSave(p),
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

// pickBotModel hands the save's model and effort to the model pill's switch when they differ from the Bot's view;
// an empty one keeps the view's. The switch decides what the pick changes, and the view's diff counts it.
func pickBotModel(ctx context.Context, models modelSwitcher, chatID marotte.ChatID, read *marotte.BotView, s *botSave) error {
	model := cmp.Or(s.model, read.Model)
	if model == read.Model && (s.effort == "" || string(s.effort) == read.Effort) {
		return nil
	}
	return models.SwitchModel(ctx, chatID, marotte.SwitchModelCommand{Model: model, Effort: s.effort})
}

// refusedChoice answers a refused model pick or supervised switch. A supervised refusal after a switch that changed
// the record answers 502 bot_supervised naming the field.
func refusedChoice(
	ctx context.Context, chats chatStore, chatID marotte.ChatID, read *marotte.BotView, err error, supervised bool,
) error {
	w, wErr := writeBotSave(ctx, chats, chatID, read, nil)
	switch {
	case wErr != nil:
		return wErr
	case !supervised || len(w.changes) == 0:
		return err
	}
	code, _, _ := StatusOf(err)
	return statusErrorFields(cmp.Or(code, http.StatusInternalServerError), reasonSupervised,
		[]fieldError{{Field: "supervised", Error: rpcerr.Text(err)}}, errSupervised)
}

// The BotView fields a save can change, as botChanges names them.
const (
	fieldName       = "name"
	fieldGoal       = "goal"
	fieldSteering   = "steering"
	fieldMemory     = "memory"
	fieldStarted    = "started"
	fieldModel      = "model"
	fieldEffort     = "effort"
	fieldSupervised = "supervised"
	fieldRules      = "rules"
)

// botChanges names each field where two views of one Bot differ.
type botChanges []string

func diffBotViews(was, now *marotte.BotView) botChanges {
	var out botChanges
	for _, d := range []struct {
		field  string
		differ bool
	}{
		{fieldName, was.Name != now.Name},
		{fieldGoal, was.Goal != now.Goal},
		{fieldSteering, was.Steering != now.Steering},
		{fieldMemory, was.Memory != now.Memory},
		{fieldStarted, was.Started != now.Started},
		{fieldModel, was.Model != now.Model},
		{fieldEffort, was.Effort != now.Effort},
		{fieldSupervised, was.Supervised != now.Supervised},
		{fieldRules, was.Rules != now.Rules},
	} {
		if d.differ {
			out = append(out, d.field)
		}
	}
	return out
}

func (c botChanges) has(fields ...string) bool {
	return slices.ContainsFunc(fields, func(f string) bool { return slices.Contains(c, f) })
}

// botWrite is what writeBotSave wrote.
type botWrite struct {
	rec     *marotte.Chat
	chain   []string
	changes botChanges
	replay  rulesReplay
	header  marotte.ChatHeader
	version int
	// rulesWritten says the write changed a rule's spec or status, which the rule engine reads again.
	rulesWritten bool
}

// writeBotSave writes s onto chatID's Bot block as a user command write that moves spec_version, nil writing
// nothing, and answers the record with its changes against read, which count what the save's switches already
// landed. It refuses a record that stopped being a Bot.
func writeBotSave(
	ctx context.Context, chats chatStore, chatID marotte.ChatID, read *marotte.BotView, s *botSave,
) (botWrite, error) {
	var w botWrite
	var refused error
	write := marotte.BotWriter(func(c *marotte.Chat, _ bool) bool {
		if s == nil {
			return false
		}
		was := *c.Bot
		b := was
		named := setUserName(c, s.name)
		b.Goal, b.Memory, b.Steering = s.goal, s.memory, s.steering
		now := time.Now()
		if s.start && b.StartedAt == 0 {
			b.StartedAt = now.UnixMilli()
		}
		switch {
		case s.rules != nil:
			applyRules(&b, &was, &s.plan, now)
			w.rulesWritten = true
		case !rulesArmed(&was) && rulesArmed(&b):
			w.rulesWritten = armBot(&b, now)
		}
		c.Bot = &b
		return named || w.rulesWritten || b.Goal != was.Goal || b.Memory != was.Memory || b.Steering != was.Steering ||
			b.StartedAt != was.StartedAt
	})
	if _, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || c.Bot == nil {
			refused = StatusErrorReason(http.StatusConflict, reasonBotNotFound, errBotNotFound)
			return false
		}
		wrote := write(c, exists)
		now := c.BotView()
		w.changes = diffBotViews(read, &now)
		w.version = c.Bot.SpecVersion
		w.chain = c.SessionChain()
		rec := *c
		w.rec = &rec
		w.header = c.Header()
		return wrote
	}); err != nil {
		return w, StatusError(http.StatusInternalServerError, err)
	}
	return w, refused
}

// startBot makes a just-started Bot's memory file and sends its kickoff, answering the kickoff's refusal as text (a
// close that took the Bot's tab first refuses it, Membership.BeginMessage), or "" once sent. The Bot stays started
// either way.
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

// cmdBotSetPaused pauses or resumes a started Bot's rules: a pause drops what its rules hold waiting but a Run now
// press, a resume arms them afresh. A closed Bot keeps closed_at either way and arms nothing, and the state it already
// holds writes nothing.
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
		if p.Paused {
			disarmBot(&b, true)
		} else {
			armBot(&b, time.Now())
		}
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
		reindex(roles.rules, cmd.ChatID)
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
