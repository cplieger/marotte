package marotte

import "errors"

// BotRoleID is the mode id of every Bot's session-scoped role, and the only mode a Bot's record names.
const BotRoleID = "marotte-bot"

// DefaultModeID is KAS's bundled default mode, the client's DEFAULT_MODE_ID (static-src/roles.ts).
const DefaultModeID = "vibe"

// AdoptMode writes id as the chat's mode and reports whether the record changed. A Bot's record only ever names
// BotRoleID, whatever id is, and a regular chat's never does.
func (c *Chat) AdoptMode(id string) bool {
	switch {
	case c.Bot != nil:
		id = BotRoleID
	case id == BotRoleID:
		return false
	}
	if c.CurrentModeID == id {
		return false
	}
	c.CurrentModeID = id
	return true
}

// IsStartedBot reports whether c is a Bot past its first start, the only Bot whose sessions carry the role.
func (c *Chat) IsStartedBot() bool {
	return c.Bot != nil && c.Bot.StartedAt != 0
}

// Bot is the block that makes a chat a Bot: a chat whose record carries it is one, for good, and every setting
// marotte keeps about the Bot lives here, in the chat's own chat.json. Field order is fieldalignment's.
type Bot struct {
	// Goal is what the Bot keeps working toward.
	Goal string `json:"goal"`
	// Steering is the user's custom steering for the Bot, markdown.
	Steering string `json:"steering,omitempty"`
	// StartedAt is the unix ms of the first start; 0 is a draft.
	StartedAt int64 `json:"started_at,omitempty"`
	// ClosedAt is the unix ms the Bot's tab was closed; 0 while it is open.
	ClosedAt int64 `json:"closed_at,omitempty"`
	// SpecVersion moves once per user command write that changes the Bot's view (BotWriter), which a save sends
	// back as its base_version.
	SpecVersion int  `json:"spec_version"`
	Memory      bool `json:"memory"`
	Paused      bool `json:"paused,omitempty"`
}

// BotView is a Bot as a save states it: the model and effort its next turn runs on, so a pick parked behind a turn
// or thinking already off compares equal to the save that sends it back.
type BotView struct {
	Name, Goal, Steering, Model, Effort string
	Memory, Started, Supervised         bool
}

// BotView is c's view; c must be a Bot.
func (c *Chat) BotView() BotView {
	return BotView{
		Name: c.Name, Goal: c.Bot.Goal, Steering: c.Bot.Steering, Model: c.NextModel(), Effort: c.NextEffort(),
		Memory: c.Bot.Memory, Started: c.Bot.StartedAt != 0, Supervised: c.SupervisedMode,
	}
}

// BotWriter wraps a user command's record mutation so it bumps SpecVersion once when it changes a Bot's view: every
// command that writes the view goes through it, so a stale save's base check sees the change. Engine reports write
// unwrapped and never move SpecVersion.
func BotWriter(mutate func(c *Chat, exists bool) bool) func(c *Chat, exists bool) bool {
	return func(c *Chat, exists bool) bool {
		var was *BotView
		if c.Bot != nil {
			v := c.BotView()
			was = &v
		}
		if !mutate(c, exists) {
			return false
		}
		if was != nil && c.Bot != nil && c.BotView() != *was {
			b := *c.Bot
			b.SpecVersion++
			c.Bot = &b
		}
		return true
	}
}

// ErrBotDraft refuses a session, prompt or fork for a Bot not yet started: a draft has no role to run on.
var ErrBotDraft = errors.New("this Bot has not been created yet; create it to send it a message")

// BotSetup is GET /api/bots/{id}: what the Bot's setup shows, read from its record. Field order is fieldalignment's.
type BotSetup struct {
	Bot *Bot `json:"bot"`
	// Memory describes the memory file; nil while memory is off.
	Memory *BotMemoryStat `json:"memory,omitempty"`
	Name   string         `json:"name"`
	// Model and Effort are Chat.NextModel and Chat.NextEffort, the pick a bot_save sending them back leaves as it is.
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	Supervised bool   `json:"supervised"`
}

// BotMemoryStat is the memory file's size and last write, both 0 before the file exists.
type BotMemoryStat struct {
	Bytes int64 `json:"bytes"`
	// UpdatedAt is the file's modification time in unix ms.
	UpdatedAt int64 `json:"updated_at"`
}

// BotMemoryText is GET /api/bots/{id}/memory: the Bot's notes, empty before the file exists.
type BotMemoryText struct {
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`
	UpdatedAt int64  `json:"updated_at"`
}

// BotState is a Bot's lifecycle state.
type BotState string

const (
	// botStateDraft is a Bot whose setup has not been submitted.
	botStateDraft BotState = "draft"
	// botStateActive is a started Bot that is neither paused nor closed.
	botStateActive BotState = "active"
	// botStatePaused is a started Bot the user paused.
	botStatePaused BotState = "paused"
	// botStateClosed is a Bot whose tab is closed, whatever else holds.
	botStateClosed BotState = "closed"
)

// BotSummary is the Bot's part of ChatHeader: its facts a reader needs without loading the record.
type BotSummary struct {
	State BotState `json:"state"`
}

// summary is the header's view of b; nil for a nil b, a regular chat.
func (b *Bot) summary() *BotSummary {
	if b == nil {
		return nil
	}
	return &BotSummary{State: b.state()}
}

func (b *Bot) state() BotState {
	switch {
	case b.ClosedAt != 0:
		return botStateClosed
	case b.StartedAt == 0:
		return botStateDraft
	case b.Paused:
		return botStatePaused
	default:
		return botStateActive
	}
}
