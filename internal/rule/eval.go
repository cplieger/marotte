package rule

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// Eval evaluates a rule's condition over one snapshot by Kleene's three-valued logic; a nil condition fires. A
// result that is not True carries the skip reason, built from marotte's words and the values the user typed,
// never from the event's own text.
func Eval(c *Cond, s *Snapshot) Result {
	if c == nil {
		return Result{Value: True}
	}
	e := evaluator{s: s}
	v := e.node(c)
	if v == True {
		return Result{Value: True}
	}
	return Result{Value: v, Reason: e.reason(c, v)}
}

type evaluator struct {
	s *Snapshot
}

// LeafValue is one condition item's value in an evaluation, named by its JSON path in the spec.
type LeafValue struct {
	Path  string
	Value Truth
}

// Leaves answers each item of c in tree order with the value Eval reads for it over s, its path rooted at
// "condition"; empty for a nil c.
func Leaves(c *Cond, s *Snapshot) []LeafValue {
	out := []LeafValue{}
	if c != nil {
		evaluator{s: s}.leaves(c, "condition", &out)
	}
	return out
}

func (e evaluator) leaves(c *Cond, path string, out *[]LeafValue) {
	if c.Leaf != nil {
		*out = append(*out, LeafValue{Path: path, Value: e.leaf(c.Leaf)})
		return
	}
	for i := range c.Children {
		e.leaves(&c.Children[i], path+".children["+strconv.Itoa(i)+"]", out)
	}
}

func (e evaluator) node(c *Cond) Truth {
	if c.Leaf != nil {
		return e.leaf(c.Leaf)
	}
	switch c.Op {
	case OpAll:
		return e.join(c.Children, False)
	case OpAny:
		return e.join(c.Children, True)
	}
	return Unknown
}

// join folds children by Kleene's rule for one join: dominant (False for AND, True for OR) wins outright,
// otherwise any unknown child makes the group unknown.
func (e evaluator) join(children []Cond, dominant Truth) Truth {
	out := True
	if dominant == True {
		out = False
	}
	for i := range children {
		switch v := e.node(&children[i]); v {
		case dominant:
			return dominant
		case Unknown:
			out = Unknown
		}
	}
	return out
}

func (e evaluator) leaf(l *Leaf) Truth {
	if l.Kind == leafEventField && l.Event != nil && l.Event.Trigger != "" && l.Event.Trigger != e.s.Trigger {
		// Scope comes before negation: outside its trigger the leaf is true unevaluated, whatever Not says.
		return True
	}
	v := e.positive(l)
	if l.Not {
		switch v {
		case True:
			return False
		case False:
			return True
		}
	}
	return v
}

func truth(b bool) Truth {
	if b {
		return True
	}
	return False
}

func (e evaluator) positive(l *Leaf) Truth {
	switch {
	case l.Kind == LeafBotBusy:
		return truth(e.s.BotBusy)
	case l.Kind == LeafBotWaiting:
		return truth(e.s.BotWaiting)
	case l.Kind == LeafTriggeredBy && l.By != nil:
		// A Run now event belongs to no trigger, so it passes every branch's lead and the branches' states decide.
		return truth(e.s.Trigger == "" || slices.Contains(l.By.Triggers, e.s.Trigger))
	case l.Kind == LeafRuleLastRan && l.Rule != nil:
		return truth(e.s.LastFire == 0 || e.s.Now.UnixMilli()-e.s.LastFire > int64(l.Rule.Minutes)*60_000)
	case l.Kind == leafTimeWindow && l.Time != nil:
		return truth(inWindow(l.Time, e.s))
	case l.Kind == leafEventField && l.Event != nil:
		return e.field(l.Event)
	}
	return Unknown
}

// inWindow reads [from, to) on Now's wall clock; a window that wraps past midnight belongs to the day it starts.
func inWindow(w *TimeLeaf, s *Snapshot) bool {
	from, okFrom := parseClock(w.From)
	to, okTo := parseClock(w.To)
	if !okFrom || !okTo {
		return false
	}
	m := s.Now.Hour()*60 + s.Now.Minute()
	day := int(s.Now.Weekday())
	if from < to {
		return slices.Contains(w.Weekdays, day) && m >= from && m < to
	}
	return (slices.Contains(w.Weekdays, day) && m >= from) || (slices.Contains(w.Weekdays, (day+6)%7) && m < to)
}

// fieldState is what an event holds for one field.
type fieldState uint8

const (
	fieldPresent fieldState = iota
	fieldMissing
	// fieldUnread is a value the event does not hold whole: cut, past the fields budget, or never extracted.
	fieldUnread
)

func (e evaluator) lookup(name string) (any, fieldState) {
	ev := &e.s.Event
	if slices.Contains(ev.Cut, name) {
		return nil, fieldUnread
	}
	if ev.Paths != nil && strings.HasPrefix(name, "payload.") {
		pv, ok := ev.Paths[name]
		switch {
		case !ok || pv.Unknown:
			return nil, fieldUnread
		case pv.Absent || pv.Value == nil:
			return nil, fieldMissing
		}
		return pv.Value, fieldPresent
	}
	v, ok := ev.Fields[name]
	if !ok || v == nil {
		return nil, fieldMissing
	}
	return v, fieldPresent
}

func (e evaluator) field(l *EventLeaf) Truth {
	v, state := e.lookup(l.Field)
	switch state {
	case fieldUnread:
		return Unknown
	case fieldMissing:
		return False
	}
	if list, ok := v.([]string); ok {
		return truth(compareList(l, list))
	}
	return truth(compareScalar(l, v))
}

func compareList(l *EventLeaf, list []string) bool {
	switch l.Op {
	case fieldIsSet:
		return len(list) > 0
	case fieldContains:
		return slices.Contains(list, l.Value)
	case fieldAbove, fieldBelow:
		return false
	}
	return slices.ContainsFunc(list, func(item string) bool { return compareText(l, item) })
}

func compareScalar(l *EventLeaf, v any) bool {
	switch l.Op {
	case fieldIsSet:
		s, isText := v.(string)
		return !isText || s != ""
	case fieldAbove, fieldBelow:
		n, okN := number(v)
		ref, okRef := number(l.Value)
		if !okN || !okRef {
			return false
		}
		return (l.Op == fieldAbove && n > ref) || (l.Op == fieldBelow && n < ref)
	}
	text, ok := scalarText(v)
	return ok && compareText(l, text)
}

// compareText applies a text operator; contains, on a scalar, is a substring.
func compareText(l *EventLeaf, s string) bool {
	switch l.Op {
	case fieldIs:
		return s == l.Value
	case fieldOneOf:
		return slices.Contains(l.Values, s)
	case fieldContains:
		return strings.Contains(s, l.Value)
	case fieldStartsWith:
		return strings.HasPrefix(s, l.Value)
	case fieldMatches:
		return globMatch([]rune(l.Value), []rune(s))
	}
	return false
}

// scalarText writes a value as is and one_of compare it: a number by its shortest decimal form, a boolean as
// true or false.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func number(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		var err error
		if f, err = strconv.ParseFloat(x, 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

// globMatch matches the whole of s against p, where * is any run of characters, ? one character, and every other
// character, [ included, is itself.
func globMatch(p, s []rune) bool {
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case star >= 0:
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// noBranch is the reason when only the flow's branch leads decided: the event came from no branch's trigger.
const noBranch = "no branch of this rule applies to this event"

// unreadable is the reason for a leaf whose payload is missing, which Validate never lets a stored rule hold.
const unreadable = "this rule's conditions could not be read"

// reason names the first two leaves that decided v, in tree order: a false AND's false children, a false OR's
// every child, an unknown group's unknown children.
func (e evaluator) reason(c *Cond, v Truth) string {
	var out []string
	e.deciding(c, v, &out)
	switch len(out) {
	case 0:
		return noBranch
	case 1:
		return out[0]
	}
	return out[0] + " and " + out[1]
}

func (e evaluator) deciding(c *Cond, v Truth, out *[]string) {
	if len(*out) >= 2 {
		return
	}
	if c.Leaf != nil {
		if r := e.leafReason(c.Leaf, v); r != "" && !slices.Contains(*out, r) {
			*out = append(*out, r)
		}
		return
	}
	for i := range c.Children {
		if e.node(&c.Children[i]) == v {
			e.deciding(&c.Children[i], v, out)
		}
	}
}

// leafReason says the state that holds for a leaf that read v, False or Unknown: the opposite of the leaf as
// written. A branch lead is never named.
func (e evaluator) leafReason(l *Leaf, v Truth) string {
	def := leafDefs[l.Kind]
	key := phraseNot
	if l.Not {
		key = phraseIs
	}
	switch {
	case l.Kind == LeafTriggeredBy:
		return ""
	case l.Kind == leafEventField && l.Event != nil:
		return e.fieldReason(l, v)
	case v == Unknown:
		return unreadable
	case l.Kind == leafTimeWindow:
		return fillTemplate(def.phrases[key], "from", l.Time.From, "to", l.Time.To, "days", daysPhrase(l.Time.Weekdays))
	case l.Kind == LeafRuleLastRan && l.Not && e.s.LastFire == 0:
		return def.phrases[phraseNever]
	case l.Kind == LeafRuleLastRan:
		return fillTemplate(def.phrases[key], "minutes", formatMinutes(l.Rule.Minutes))
	}
	return def.phrases[key]
}

func (e evaluator) fieldReason(l *Leaf, v Truth) string {
	ev := l.Event
	phrases := leafDefs[leafEventField].phrases
	if v == Unknown {
		return fillTemplate(phrases[phraseUnknown], "field", ev.Field)
	}
	d, ok := findOp(ev.Op)
	if !ok {
		return unreadable
	}
	op, key := d.negative, phraseNot
	if l.Not {
		op, key = d.phrase, phraseIs
	} else if _, state := e.lookup(ev.Field); state == fieldMissing {
		set, _ := findOp(fieldIsSet)
		op = set.negative
	}
	op = fillTemplate(op, "value", ev.Value, "values", strings.Join(ev.Values, ", "))
	return fillTemplate(phrases[key], "field", ev.Field, "op", op)
}
