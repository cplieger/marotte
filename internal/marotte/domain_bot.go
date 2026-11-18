package marotte

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cplieger/marotte/internal/rule"
)

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
	// LastSave is the answer of the last bot_save that sent rules, nil when it sent no op_id.
	LastSave *BotSaveAnswer `json:"last_save,omitempty"`
	// Goal is what the Bot keeps working toward.
	Goal string `json:"goal"`
	// Steering is the user's custom steering for the Bot, markdown.
	Steering string `json:"steering,omitempty"`
	// Rules are the Bot's rules in the user's order, each its spec beside the engine's status.
	Rules []rule.Rule `json:"rules,omitempty"`
	// StartedAt is the unix ms of the first start; 0 is a draft.
	StartedAt int64 `json:"started_at,omitempty"`
	// ClosedAt is the unix ms the Bot's tab was closed; 0 while it is open.
	ClosedAt int64 `json:"closed_at,omitempty"`
	// SpecVersion is moved only by BotWriter, once per user command write that changes the Bot's view, so no user
	// write reuses a version an earlier one published; a save sends it back as its base_version.
	SpecVersion int `json:"spec_version"`
	// RulesVersion moves with every write of a rule's spec or status, so a reader of the header sees either change.
	RulesVersion int  `json:"rules_version,omitempty"`
	Memory       bool `json:"memory"`
	Paused       bool `json:"paused,omitempty"`
}

// BotSaveAnswer is what a bot_save with rules and an op_id answered, stored with the Bot so a retry of that op_id
// answers it again, across a restart, while SpecVersion is still Version.
type BotSaveAnswer struct {
	OpID       string     `json:"op_id"`
	RuleIDs    []string   `json:"rule_ids"`
	TriggerIDs [][]string `json:"trigger_ids"`
	Version    int        `json:"version"`
}

// BotView is a Bot as a save states it: the model and effort its next turn runs on, so a pick parked behind a turn
// or thinking already off compares equal to the save that sends it back. Rules is the rules' ids and specs as JSON,
// so a rule edit moves SpecVersion while the engine's status writes do not.
type BotView struct {
	Name, Goal, Steering, Model, Effort, Rules string
	Memory, Started, Supervised                bool
}

// BotView is c's view; c must be a Bot.
func (c *Chat) BotView() BotView {
	return BotView{
		Name: c.Name, Goal: c.Bot.Goal, Steering: c.Bot.Steering, Model: c.NextModel(), Effort: c.NextEffort(),
		Rules:  rulesView(c.Bot.Rules),
		Memory: c.Bot.Memory, Started: c.Bot.StartedAt != 0, Supervised: c.SupervisedMode,
	}
}

type ruleView struct {
	Spec *rule.Spec `json:"spec"`
	ID   string     `json:"id"`
}

func rulesView(rules []rule.Rule) string {
	if len(rules) == 0 {
		return ""
	}
	v := make([]ruleView, len(rules))
	for i := range rules {
		v[i] = ruleView{ID: rules[i].ID, Spec: &rules[i].Spec}
	}
	// A Spec holds only strings, ints, bools, slices and pointers to them, which always encode.
	enc, _ := json.Marshal(v)
	return string(enc)
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
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Engine is what the rule engine holds on the Bot only in memory; {} when it holds nothing.
	Engine     BotEngine `json:"engine"`
	Supervised bool      `json:"supervised"`
}

// BotEngineState is the rule engine's state as the Bot's setup shows it.
type BotEngineState string

// BotEngineWriteFailing is an engine stopped because a record write failed.
const BotEngineWriteFailing BotEngineState = "write_failing"

// BotEngine is the rule engine's in-memory facts on one Bot, times in unix ms.
type BotEngine struct {
	State BotEngineState `json:"state,omitempty"`
	// Since is when the write fault began; set with State.
	Since int64 `json:"since,omitempty"`
	// CeilingUntil is when the Bot falls back under its hourly ceiling, while a prompt waits on it.
	CeilingUntil int64 `json:"ceiling_until,omitempty"`
}

// RuleTestOutcome is whether a dry run's event would send the prompt.
type RuleTestOutcome string

// The dry run's outcomes.
const (
	RuleTestFire RuleTestOutcome = "fire"
	RuleTestSkip RuleTestOutcome = "skip"
)

// RuleTestValue is one condition item's value in a dry run.
type RuleTestValue string

// The three values of a condition item.
const (
	RuleTestTrue    RuleTestValue = "true"
	RuleTestFalse   RuleTestValue = "false"
	RuleTestUnknown RuleTestValue = "unknown"
)

// RuleWaitReason is what would hold a fired prompt back.
type RuleWaitReason string

// The holds: the Bot's own turn, the rule's 1-minute gap, its trigger's hourly and daily limits, the Bot's hourly
// ceiling, and the agent engine not yet started.
const (
	RuleWaitBotBusy  RuleWaitReason = "bot_busy"
	RuleWaitGap      RuleWaitReason = "gap"
	RuleWaitHour     RuleWaitReason = "hour"
	RuleWaitDay      RuleWaitReason = "day"
	RuleWaitBot      RuleWaitReason = "bot"
	RuleWaitNotReady RuleWaitReason = "not_ready"
)

// RuleTestAnswer is POST /api/bots/{id}/rules/test: what the rule would do with one event, sending nothing.
type RuleTestAnswer struct {
	Outcome RuleTestOutcome `json:"outcome"`
	// Reason is the skip reason; empty on fire.
	Reason string `json:"reason,omitempty"`
	// Text is the exact prompt the Bot would receive.
	Text string `json:"text"`
	// Leaves are the condition's items in tree order, each named by its JSON path in the spec.
	Leaves []RuleTestLeaf `json:"leaves"`
	// Wait is what would hold a fired prompt back; zero when it would send now, or on skip.
	Wait RuleTestWait `json:"wait"`
}

// RuleTestLeaf is one condition item's value, its path the spec's ("condition.children[1]").
type RuleTestLeaf struct {
	Path  string        `json:"path"`
	Value RuleTestValue `json:"value"`
}

// RuleTestWait is a hold and, for a pace, when it ends in unix ms.
type RuleTestWait struct {
	Reason RuleWaitReason `json:"reason,omitempty"`
	Until  int64          `json:"until,omitempty"`
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

// BotAttention is what a Bot's row flags for the user; "" is nothing.
type BotAttention string

// botAttentionRuleFailed is a Bot with a rule whose LastFailure is set.
const botAttentionRuleFailed BotAttention = "rule_failed"

// BotSummary is the Bot's part of ChatHeader: its facts a reader needs without loading the record. The times are
// unix ms due times as the record places them, already past for an overdue trigger; they are set only for an active
// Bot's armed triggers.
type BotSummary struct {
	State BotState `json:"state"`
	// NextRule names the rule of NextAt, NextTimedRule of NextTimedAt, IdleRule of IdleMinutes.
	NextRule      string       `json:"next_rule,omitempty"`
	NextTimedRule string       `json:"next_timed_rule,omitempty"`
	IdleRule      string       `json:"idle_rule,omitempty"`
	Attention     BotAttention `json:"attention,omitempty"`
	// NextAt is the earliest due time over every armed time trigger.
	NextAt int64 `json:"next_at,omitempty"`
	// NextTimedAt is the same over the triggers a live turn does not postpone: every from the last evaluation, at.
	NextTimedAt int64 `json:"next_timed_at,omitempty"`
	// IdleMinutes is the shortest interval over the armed triggers counted from a turn's end (the first in rule order
	// on a tie); 0 when there is none.
	IdleMinutes int `json:"idle_minutes"`
	// Rules counts the enabled rules.
	Rules        int `json:"rules"`
	RulesVersion int `json:"rules_version"`
}

// summary is the header's view of b, lastTurnEndedAt being Chat.LastTurnEndedAt; nil for a nil b, a regular chat.
func (b *Bot) summary(lastTurnEndedAt int64) *BotSummary {
	if b == nil {
		return nil
	}
	s := &BotSummary{State: b.state(), RulesVersion: b.RulesVersion}
	for i := range b.Rules {
		r := &b.Rules[i]
		if r.Status.LastFailure != nil {
			s.Attention = botAttentionRuleFailed
		}
		if !r.Spec.Enabled {
			continue
		}
		s.Rules++
		if s.State == botStateActive {
			s.schedule(r, lastTurnEndedAt)
		}
	}
	return s
}

// schedule folds r's armed time triggers into s.
func (s *BotSummary) schedule(r *rule.Rule, lastTurnEndedAt int64) {
	for _, t := range r.Spec.Triggers {
		st := r.Status.Triggers[t.ID]
		if st.ArmedAt == 0 {
			continue
		}
		if due, ok := rule.DueAt(t, &st, lastTurnEndedAt, time.Local); ok {
			s.fold(t, due.UnixMilli(), r.Spec.Name)
		}
	}
}

// fold adds one trigger's due time at; the strict comparisons keep the first in rule order on a tie.
func (s *BotSummary) fold(t rule.Trigger, at int64, name string) {
	if s.NextAt == 0 || at < s.NextAt {
		s.NextAt, s.NextRule = at, name
	}
	if t.Kind == rule.KindEvery && t.Every.Anchor == rule.AnchorIdle {
		if s.IdleMinutes == 0 || t.Every.Minutes < s.IdleMinutes {
			s.IdleMinutes, s.IdleRule = t.Every.Minutes, name
		}
		return
	}
	if s.NextTimedAt == 0 || at < s.NextTimedAt {
		s.NextTimedAt, s.NextTimedRule = at, name
	}
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
