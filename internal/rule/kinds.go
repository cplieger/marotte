package rule

// kindDef is one trigger kind's validation, the event fields it carries and what the catalogue says of it.
type kindDef struct {
	// check validates the kind's own payload field, adding each error under path (the payload's JSON path).
	check func(t *Trigger, path string, add addFunc)
	// phrases are triggerPhrase's templates by variant; sample is the kind's own fields of its catalogue sample.
	phrases map[string]string
	sample  map[string]any
	// payload is the Trigger field the kind sets, by its JSON name; "" for a kind with none.
	payload string
	group   string
	label   string
	// fields are the kind's own event fields beside baseFields; lists are those holding a list of strings.
	fields []string
	lists  []string
	params []Param
	// time marks a kind that paces itself: it refuses limits and holds one waiting event.
	time bool
}

// The JSON names of the payload fields a trigger or a leaf sets.
const (
	payloadEvery = "every"
	payloadAt    = "at"
	payloadEvent = "event"
	payloadTime  = "time"
	payloadRule  = "rule"
	payloadBy    = "by"
)

// The keys of a leaf's phrases, LeafEntry's.
const (
	phraseIs      = "is"
	phraseNot     = "not"
	phraseNever   = "never"
	phraseUnknown = "unknown"
)

// kindOrder is the catalogue's order of the built kinds, the editor's (design §13.2).
var kindOrder = []TriggerKind{KindEvery, KindAt}

// sampleAt is the instant every catalogue sample is stamped with.
const sampleAt = "2026-11-16T09:00:00+01:00"

// kinds is every built trigger kind; Validate refuses any other.
var kinds = map[TriggerKind]kindDef{
	KindEvery: {
		payload: payloadEvery, time: true, fields: []string{fieldDueAt, fieldAnchor}, check: checkEvery,
		group: groupTime, label: "Every few minutes or hours",
		phrases: map[string]string{string(AnchorLast): "every {minutes}", string(AnchorIdle): "{minutes} after the Bot's last turn"},
		params: []Param{
			{Name: "minutes", Min: MinEveryMinutes, Max: MaxEveryMinutes},
			{Name: fieldAnchor, Choices: []string{string(AnchorLast), string(AnchorIdle)}},
		},
		sample: map[string]any{fieldTriggerName: "every 1 h", fieldDueAt: sampleAt, fieldAnchor: string(AnchorLast)},
	},
	KindAt: {
		payload: payloadAt, time: true, fields: []string{fieldDueAt}, check: checkAt,
		group: groupTime, label: "At a time of day",
		phrases: map[string]string{
			string(DaysDaily): "every day at {time}", string(DaysWeekdays): "{weekdays} at {time}",
			string(DaysMonthly): "on {month_day} of the month at {time}",
		},
		params: []Param{
			{Name: "days", Choices: []string{string(DaysDaily), string(DaysWeekdays), string(DaysMonthly)}},
			{Name: "time"},
			{Name: paramWeekdays, Min: 0, Max: 6},
			{Name: "month_day", Min: 1, Max: 31, Named: map[string]int{"last": MonthDayLast}},
		},
		sample: map[string]any{fieldTriggerName: "every day at 09:00", fieldDueAt: sampleAt},
	},
}

// The event fields marotte itself writes.
const (
	fieldEventID     = "event_id"
	fieldTrigger     = "trigger"
	fieldTriggerName = "trigger_name"
	fieldRule        = "rule"
	fieldAt          = "at"
	fieldLateBy      = "late_by"
	fieldDueAt       = "due_at"
	fieldAnchor      = "anchor"
)

// baseFields are the fields every event carries.
var baseFields = []string{fieldEventID, fieldTrigger, fieldTriggerName, fieldRule, fieldAt, fieldLateBy}

// The catalogue's group of the time kinds and the time leaf, and the name of a weekday set's param.
const (
	groupTime     = "Time"
	paramWeekdays = "weekdays"
)

// leafDef is one built leaf kind.
type leafDef struct {
	// phrases are keyed by the phrase keys above.
	phrases map[string]string
	check   func(l *Leaf, path string, add addFunc)
	group   string
	// payload is the Leaf field the kind sets, by its JSON name; "" for a kind with none.
	payload string
	params  []Param
}

// leafOrder is the catalogue's order of the state items, design §13.3's table.
var leafOrder = []LeafKind{LeafBotBusy, LeafBotWaiting, leafTimeWindow, leafEventField, LeafRuleLastRan}

var leafDefs = map[LeafKind]leafDef{
	LeafBotBusy: {group: "This Bot", phrases: map[string]string{
		phraseIs: "this Bot is working", phraseNot: "this Bot is not working",
	}},
	LeafBotWaiting: {group: "This Bot", phrases: map[string]string{
		phraseIs: "this Bot is waiting on you", phraseNot: "this Bot is not waiting on you",
	}},
	leafTimeWindow: {
		group: groupTime, payload: payloadTime, check: checkTimeLeaf, phrases: map[string]string{
			phraseIs: "the time is between {from} and {to} {days}", phraseNot: "the time is not between {from} and {to} {days}",
		},
		params: []Param{{Name: "from"}, {Name: "to"}, {Name: paramWeekdays, Min: 0, Max: 6}},
	},
	leafEventField: {
		group: "What happened", payload: payloadEvent, phrases: map[string]string{
			phraseIs: "the event's {field} {op}", phraseNot: "the event's {field} {op}", phraseUnknown: "the event's {field} could not be read",
		},
		params: []Param{{Name: "field"}, {Name: "op", Choices: opNames()}, {Name: "value", Max: maxValueBytes}, {Name: "values", Max: maxOneOfValues}},
	},
	LeafRuleLastRan: {
		group: "This rule", payload: payloadRule, check: checkRuleLeaf, phrases: map[string]string{
			phraseIs: "this rule last ran more than {minutes} ago", phraseNot: "this rule ran in the last {minutes}",
			phraseNever: "this rule has never run",
		},
		params: []Param{{Name: "minutes", Min: minLastRanMinutes, Max: maxLastRanMinutes}},
	},
	// triggered_by is the flow's, never an item, so it has no group and no phrase: a skip reason never names it.
	LeafTriggeredBy: {payload: payloadBy},
}

// opDef is one event field operator, in the editor's order.
type opDef struct {
	op       FieldOp
	phrase   string
	negative string
	// value says the operator reads Value; list says it applies to a list field.
	value bool
	list  bool
}

var opDefs = []opDef{
	{op: fieldIs, phrase: "is {value}", negative: "is not {value}", value: true, list: true},
	{op: fieldOneOf, phrase: "is one of {values}", negative: "is not one of {values}", list: true},
	{op: fieldContains, phrase: "contains {value}", negative: "does not contain {value}", value: true, list: true},
	{op: fieldStartsWith, phrase: "starts with {value}", negative: "does not start with {value}", value: true, list: true},
	{op: fieldMatches, phrase: "matches {value}", negative: "does not match {value}", value: true, list: true},
	{op: fieldAbove, phrase: "is above {value}", negative: "is at most {value}", value: true},
	{op: fieldBelow, phrase: "is below {value}", negative: "is at least {value}", value: true},
	{op: fieldIsSet, phrase: "is set", negative: "is not set", list: true},
}

func findOp(op FieldOp) (opDef, bool) {
	for _, d := range opDefs {
		if d.op == op {
			return d, true
		}
	}
	return opDef{}, false
}

func opNames() []string {
	out := make([]string, len(opDefs))
	for i, d := range opDefs {
		out[i] = string(d.op)
	}
	return out
}
