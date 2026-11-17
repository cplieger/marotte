// Package rule is a Bot's rules and every pure function over them: validation, matching, evaluation, schedules and
// rendering. It imports only the standard library, so internal/marotte can hold its types.
package rule

import (
	"encoding/json"
	"time"
)

// Bounds a stored rule never exceeds. The catalogue read publishes them, so the editor mirrors none by hand.
const (
	MaxRules          = 20
	maxTriggers       = 8
	maxNameRunes      = 80
	maxPromptBytes    = 8 << 10
	maxLeaves         = 24
	maxGroupDepth     = 4
	maxGroupChildren  = 12
	maxOneOfValues    = 50
	maxValueBytes     = 256
	MinEveryMinutes   = 5
	MaxEveryMinutes   = 7 * 24 * 60
	minLastRanMinutes = 5
	maxLastRanMinutes = 31 * 24 * 60
	minPerHour        = 1
	maxPerHour        = 60
	maxPerDay         = 24 * 60
	minHoldCap        = 1
	maxHoldCap        = 25
	// maxBotBytes is half the record's 8 MiB header cap, so a Bot's rules at their worst case never reach it.
	maxBotBytes = 4 << 20
	// maxStringBytes bounds each string of an event block, maxBlockBytes the whole block as the prompt fences it.
	maxStringBytes = 1 << 10
	maxBlockBytes  = 8 << 10
	// defaultPerHour and defaultHoldCap are a new event trigger's limits.
	defaultPerHour = 12
	defaultHoldCap = 10
	// BotMaxPerHour is the automatic prompts one Bot receives in a rolling hour, whatever its rules say.
	BotMaxPerHour = 60
)

// MinGap spaces two deliveries of one rule.
const MinGap = time.Minute

// Limit names the pace that holds a delivery back.
type Limit uint8

// The paces; LimitNone lets a delivery start now.
const (
	LimitNone Limit = iota
	// LimitGap is the rule's MinGap since its last delivery.
	LimitGap
	// LimitHour and LimitDay are the trigger's MaxPerHour and MaxPerDay.
	LimitHour
	LimitDay
	// LimitBot is BotMaxPerHour over every rule of the Bot.
	LimitBot
)

// NewIDPrefix starts the placeholder id a save gives a new trigger, so its rule's condition can name the trigger
// before the server mints its id; bot_save replaces every placeholder with a minted id.
const NewIDPrefix = "new:"

// Rule is one rule of a Bot, in the user's order in Bot.Rules.
type Rule struct {
	ID     string `json:"id"`
	Spec   Spec   `json:"spec"`
	Status Status `json:"status"`
}

// Spec is the user's part of a rule, what the editor sends.
type Spec struct {
	// Condition is nil when the rule has none: every evaluation fires.
	Condition *Cond     `json:"condition,omitempty"`
	Name      string    `json:"name"`
	Prompt    string    `json:"prompt"`
	Triggers  []Trigger `json:"triggers"`
	Enabled   bool      `json:"enabled"`
}

// TriggerKind names what a trigger watches.
type TriggerKind string

// The trigger kinds built so far.
const (
	KindEvery TriggerKind = "every"
	KindAt    TriggerKind = "at"
)

// Trigger is one event source of a rule. Exactly the payload field its Kind names is set.
type Trigger struct {
	// Limits is required on every kind but the time kinds, which pace themselves and refuse it.
	Limits *TriggerLimits `json:"limits,omitempty"`
	Every  *EveryTrigger  `json:"every,omitempty"`
	At     *AtTrigger     `json:"at,omitempty"`
	ID     string         `json:"id"`
	Kind   TriggerKind    `json:"kind"`
}

// TriggerLimits paces one trigger's deliveries; an event over a limit waits, it is never skipped.
type TriggerLimits struct {
	Hold HoldPolicy `json:"hold"`
	// MaxPerHour counts this trigger's deliveries over a rolling hour.
	MaxPerHour int `json:"max_per_hour"`
	// MaxPerDay counts them over the server's local calendar day; 0 is no daily cap.
	MaxPerDay int `json:"max_per_day,omitempty"`
	// HoldCap counts this trigger's waiting events, stages new and fired together.
	HoldCap int `json:"hold_cap"`
}

// HoldPolicy is what a trigger keeps while its Bot is busy.
type HoldPolicy string

// The hold policies; "" reads as HoldFIFO.
const (
	HoldFIFO   HoldPolicy = "fifo"
	holdNewest HoldPolicy = "newest"
)

// Anchor is what an every trigger's interval counts from.
type Anchor string

// The anchors; "" reads as AnchorLast.
const (
	// AnchorLast counts from this trigger's last evaluation, fire or skip.
	AnchorLast Anchor = "last"
	// AnchorIdle counts from the Bot's last turn end, its last evaluation or its arming, whichever is latest.
	AnchorIdle Anchor = "idle"
)

// EveryTrigger is due Minutes after its anchor.
type EveryTrigger struct {
	Anchor  Anchor `json:"anchor,omitempty"`
	Minutes int    `json:"minutes"`
}

// AtDays is which days an at trigger falls on.
type AtDays string

// The at trigger's day choices.
const (
	DaysDaily    AtDays = "daily"
	DaysWeekdays AtDays = "weekdays"
	DaysMonthly  AtDays = "monthly"
)

// MonthDayLast in AtTrigger.MonthDay is the month's last day.
const MonthDayLast = -1

// AtTrigger is due at Time, local to the server, on the days Days names.
type AtTrigger struct {
	Days AtDays `json:"days"`
	// Time is HH:MM.
	Time string `json:"time"`
	// Weekdays are time.Weekday values (0 is Sunday), read only for DaysWeekdays.
	Weekdays []int `json:"weekdays,omitempty"`
	// MonthDay is 1 to 31 or MonthDayLast, read only for DaysMonthly; a day past a month's end is its last day.
	MonthDay int `json:"month_day,omitempty"`
}

// CondOp is a group's join. There is no "not": each leaf carries its own negative.
type CondOp string

// The two joins.
const (
	OpAll CondOp = "all"
	OpAny CondOp = "any"
)

// Cond is a group or a leaf: exactly one of Op and Leaf is set.
type Cond struct {
	Leaf     *Leaf  `json:"leaf,omitempty"`
	Op       CondOp `json:"op,omitempty"`
	Children []Cond `json:"children,omitempty"`
}

// LeafKind names what a leaf reads.
type LeafKind string

// The leaf kinds built so far.
const (
	leafEventField  LeafKind = "event_field"
	LeafTriggeredBy LeafKind = "triggered_by"
	leafTimeWindow  LeafKind = "time_window"
	LeafBotBusy     LeafKind = "bot_busy"
	LeafBotWaiting  LeafKind = "bot_waiting"
	LeafRuleLastRan LeafKind = "rule_last_ran"
)

// Leaf is one item of a condition. Its payload field matches Kind; bot_busy and bot_waiting carry none.
type Leaf struct {
	Event *EventLeaf   `json:"event,omitempty"`
	Time  *TimeLeaf    `json:"time,omitempty"`
	Rule  *LastRanLeaf `json:"rule,omitempty"`
	By    *ByLeaf      `json:"by,omitempty"`
	Kind  LeafKind     `json:"kind"`
	// Not is the leaf's negative operator: it negates the evaluated result, and unknown stays unknown.
	Not bool `json:"not,omitempty"`
}

// FieldOp compares an event field.
type FieldOp string

// The event field operators.
const (
	fieldIs         FieldOp = "is"
	fieldOneOf      FieldOp = "one_of"
	fieldContains   FieldOp = "contains"
	fieldStartsWith FieldOp = "starts_with"
	fieldMatches    FieldOp = "matches"
	fieldAbove      FieldOp = "above"
	fieldBelow      FieldOp = "below"
	fieldIsSet      FieldOp = "is_set"
)

// EventLeaf compares one field of the event being evaluated.
type EventLeaf struct {
	Field string  `json:"field"`
	Op    FieldOp `json:"op"`
	// Value is the operand of every operator but one_of, which reads Values, and is_set, which reads neither.
	Value string `json:"value,omitempty"`
	// Trigger scopes the leaf to one trigger's events: for any other trigger it is true unevaluated, whatever Not says.
	Trigger string   `json:"trigger,omitempty"`
	Values  []string `json:"values,omitempty"`
}

// TimeLeaf is true between From and To, local to the server, on Weekdays; a To before From wraps past midnight.
type TimeLeaf struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Weekdays []int  `json:"weekdays"`
}

// LastRanLeaf is true when the rule last fired more than Minutes ago, or never fired.
type LastRanLeaf struct {
	Minutes int `json:"minutes"`
}

// ByLeaf is true when the evaluated event came from one of Triggers. It only leads a branch of the flow.
type ByLeaf struct {
	Triggers []string `json:"triggers"`
}

// Status is the engine's part of a rule, written only by the engine and never accepted from a client.
type Status struct {
	LastFire    *Mark                   `json:"last_fire,omitempty"`
	LastSkip    *Mark                   `json:"last_skip,omitempty"`
	LastFailure *Mark                   `json:"last_failure,omitempty"`
	Triggers    map[string]TriggerState `json:"triggers,omitempty"`
	// Sent is the rule's delivery times (unix ms) in the last hour, for its 1-minute gap and the Bot ceiling.
	Sent []int64 `json:"sent,omitempty"`
	// Recent is the last event ids the rule's triggers evaluated, newest last.
	Recent []string `json:"recent,omitempty"`
	// Presses are the first answers of the rule's last Run now presses, oldest first (RecordPress).
	Presses []Press       `json:"presses,omitempty"`
	Queue   []QueuedEvent `json:"queue,omitempty"`
	// Dropped counts events a cap, a replacement, a pause or a close dropped since the last delivery.
	Dropped int `json:"dropped,omitempty"`
}

// Mark is one evaluation or delivery the rule's card shows.
type Mark struct {
	Trigger string `json:"trigger"`
	EventID string `json:"event_id"`
	Summary string `json:"summary"`
	Reason  string `json:"reason,omitempty"`
	At      int64  `json:"at"`
}

// Press is a Run now press's first answer, stored when the press was decided, which a retry of its op_id answers again.
type Press struct {
	EventID string       `json:"event_id"`
	Outcome PressOutcome `json:"outcome"`
	// Reason is a skip's reason without "Skipped: ", or a failure's error.
	Reason string `json:"reason,omitempty"`
}

// PressOutcome is what a Run now press did.
type PressOutcome string

// The press outcomes.
const (
	PressFired   PressOutcome = "fired"
	PressQueued  PressOutcome = "queued"
	PressSkipped PressOutcome = "skipped"
	// PressFailed is an evaluation that failed; Reason is its error.
	PressFailed PressOutcome = "failed"
)

// The stages of a QueuedEvent.
const (
	StageNew   = "new"
	StageFired = "fired"
)

// QueuedEvent is an event received and not yet evaluated, or fired and not yet delivered.
type QueuedEvent struct {
	Stage   string `json:"stage"`
	Trigger string `json:"trigger"`
	EventID string `json:"event_id"`
	// MessageID is the prompt Notify queued behind a turn, set with Queued.
	MessageID string `json:"message_id,omitempty"`
	// Data is the event as NewEvent bounds it, read back by DecodeEvent: at most 12 KiB with a webhook's
	// condition copy.
	Data    json.RawMessage `json:"data"`
	SeenAt  int64           `json:"seen_at"`
	FiredAt int64           `json:"fired_at,omitempty"`
	// PrevFire is the rule's LastFire.At (unix ms) when this event fired, 0 for none: its recheck reads it, since
	// LastFire may by then be this event's own fire or a later one.
	PrevFire int64 `json:"prev_fire,omitempty"`
	Depth    int   `json:"depth"`
	Tries    int   `json:"tries"`
	// Queued marks a fired event whose prompt waits on the chat's run order; it stays until its turn opens.
	Queued bool `json:"queued,omitempty"`
}

// TriggerState is one trigger's memory across restarts.
type TriggerState struct {
	Day *DayCount `json:"day,omitempty"`
	// Cursor is the source's own state, at most 16 KiB.
	Cursor json.RawMessage `json:"cursor,omitempty"`
	// LastEvent is the last real event's stored data, as QueuedEvent.Data.
	LastEvent json.RawMessage `json:"last_event,omitempty"`
	// Sent is this trigger's delivery times (unix ms) in the last hour, at most MaxPerHour.
	Sent []int64 `json:"sent,omitempty"`
	// ArmedAt is the instant this trigger was last armed, strictly increasing: the trigger's generation.
	ArmedAt int64 `json:"armed_at"`
	// LastAt is this trigger's last event; for a time trigger the instant of its last evaluation, never a due time.
	LastAt int64 `json:"last_at,omitempty"`
}

// Slot is the due time one evaluation of a time trigger is for, which its event carries as due_at and late_by.
type Slot struct {
	DueAt  time.Time
	LateBy time.Duration
}

// DayCount is a trigger's deliveries on one local calendar day.
type DayCount struct {
	Date string `json:"date"`
	N    int    `json:"n"`
}

// Truth is a condition's three-valued result.
type Truth uint8

// The three values of Kleene's logic.
const (
	False Truth = iota
	True
	Unknown
)

// Result is one evaluation of a rule's condition.
type Result struct {
	// Reason is the skip reason in the editor's words, without "Skipped: "; empty when Value is True.
	Reason string
	Value  Truth
}

// Snapshot is everything one evaluation reads, filled by the engine before it calls Eval.
type Snapshot struct {
	// Now is the evaluation's instant in the server's zone, which time_window reads.
	Now time.Time
	// Trigger is the id of the trigger the event came from; "" for a Run now event, which belongs to no trigger.
	Trigger string
	Event   Event
	// LastFire is the rule's last fire (unix ms); 0 when it never fired.
	LastFire   int64
	BotBusy    bool
	BotWaiting bool
}

// Event is what conditions read of one event.
type Event struct {
	// Fields are the event's fields by name, each a string, a float64, a bool or, for labels, a []string.
	Fields map[string]any
	// Paths is a webhook's condition copy by payload.<path>; nil on every other kind. A path it lacks was not
	// extracted for this event, so it reads unknown.
	Paths map[string]PathValue
	// Cut names every field the bounding cut or dropped; a leaf on one reads unknown.
	Cut []string
}

// PathValue is one payload path a webhook extracted. Exactly one of Absent, Unknown and a non-nil Value holds.
type PathValue struct {
	Value any
	// Absent marks a path the body lacks: not set.
	Absent bool
	// Unknown marks a value past maxValueBytes or past the fields budget.
	Unknown bool
}

// FieldError is one refused field of a spec, named by its JSON path ("triggers[1].every.minutes").
type FieldError struct {
	Field string `json:"field"`
	Error string `json:"error"`
}

// ValidateContext is what validation knows beyond the spec.
type ValidateContext struct {
	// Kept holds the stored rule's trigger ids when the save keeps the rule; nil for a new rule.
	Kept map[string]bool
}

// Catalog is what the editor may offer and the words it says them in, generated from the tables Validate reads, so
// the client mirrors no bound or phrase by hand. A phrase is a template whose {name} placeholders the client fills
// from the item's fields: {minutes} as a duration ("1 h 30 min"), {time}, {from} and {to} as HH:MM, {weekdays} as
// an at trigger's recurrence ("weekdays", "Mondays", "on Tue to Thu", "every day"), {days} as a time window's set
// ("on Mon to Fri", "on Sun", "every day"), {month_day} as "the 15th" or "the last day", {values} joined with ", ",
// and {op} as the operator's phrase.
type Catalog struct {
	Words  Words       `json:"words"`
	Kinds  []KindEntry `json:"kinds"`
	Leaves []LeafEntry `json:"leaves"`
	Ops    []OpEntry   `json:"ops"`
	// Common names the fields every event carries.
	Common []string    `json:"common_fields"`
	Limits LimitBounds `json:"limits"`
	Rule   Bounds      `json:"rule"`
}

// Bounds are a rule's and a condition's bounds.
type Bounds struct {
	MaxRules         int `json:"max_rules"`
	MaxTriggers      int `json:"max_triggers"`
	MaxNameRunes     int `json:"max_name_runes"`
	MaxPromptBytes   int `json:"max_prompt_bytes"`
	MaxLeaves        int `json:"max_leaves"`
	MaxGroupDepth    int `json:"max_group_depth"`
	MaxGroupChildren int `json:"max_group_children"`
	MaxOneOfValues   int `json:"max_one_of_values"`
	MaxValueBytes    int `json:"max_value_bytes"`
}

// LimitBounds are an event trigger's limits and the constant paces beside them.
type LimitBounds struct {
	Holds          []HoldPolicy `json:"holds"`
	MinPerHour     int          `json:"min_per_hour"`
	MaxPerHour     int          `json:"max_per_hour"`
	DefaultPerHour int          `json:"default_per_hour"`
	MaxPerDay      int          `json:"max_per_day"`
	MinHoldCap     int          `json:"min_hold_cap"`
	MaxHoldCap     int          `json:"max_hold_cap"`
	DefaultHoldCap int          `json:"default_hold_cap"`
	GapMinutes     int          `json:"gap_minutes"`
	BotMaxPerHour  int          `json:"bot_max_per_hour"`
}

// KindEntry is one trigger kind the editor offers.
type KindEntry struct {
	// Phrases are the kind's summary templates by variant: an every trigger's anchor, an at trigger's days.
	Phrases map[string]string `json:"phrases"`
	Kind    TriggerKind       `json:"kind"`
	Group   string            `json:"group"`
	Label   string            `json:"label"`
	// Sample is an event of the kind, for Test this rule: a JSON object of its fields by name.
	Sample json.RawMessage `json:"sample"`
	// Fields are the kind's own event fields beside Common; Lists are those holding a list of strings.
	Fields []string `json:"fields"`
	Lists  []string `json:"lists,omitempty"`
	Params []Param  `json:"params"`
	// Time marks a kind that paces itself and carries no limits.
	Time bool `json:"time"`
}

// LeafEntry is one state item the editor offers. Its phrases are keyed "is" (the leaf as written), "not" (its
// negative), and where the leaf has one, "never" (a rule_last_ran on a rule that never fired) or "unknown".
type LeafEntry struct {
	Phrases map[string]string `json:"phrases"`
	Kind    LeafKind          `json:"kind"`
	Group   string            `json:"group"`
	Params  []Param           `json:"params"`
}

// OpEntry is one event field operator with its positive and negative phrases.
type OpEntry struct {
	Op       FieldOp `json:"op"`
	Phrase   string  `json:"phrase"`
	Negative string  `json:"negative"`
	// Value says the operator reads a value; one_of reads values and is_set reads none.
	Value bool `json:"value"`
	// List says the operator applies to a list field.
	List bool `json:"list"`
}

// Param is one field of a kind or a leaf with its bounds: a number in Min to Max, one of Choices, or one of the
// named numbers in Named (an at trigger's month_day "last").
type Param struct {
	Named   map[string]int `json:"named,omitempty"`
	Name    string         `json:"name"`
	Choices []string       `json:"choices,omitempty"`
	Min     int            `json:"min,omitempty"`
	Max     int            `json:"max,omitempty"`
}

// Words are the words the placeholders are formatted with.
type Words struct {
	Minute   string `json:"minute"`
	Hour     string `json:"hour"`
	Day      string `json:"day"`
	Weekdays string `json:"weekdays"`
	EveryDay string `json:"every_day"`
	LastDay  string `json:"last_day"`
	// Days and DaysPlural are indexed by time.Weekday: "Sun" and "Sundays" first.
	Days       []string `json:"days"`
	DaysPlural []string `json:"days_plural"`
}
