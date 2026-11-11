package marotte

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
	Memory   bool  `json:"memory"`
	Paused   bool  `json:"paused,omitempty"`
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
