package rule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The refusals the editor shows in these exact words.
const (
	sentenceNotGroup    = "Use the item's own negative, such as “is off” or “does not match”"
	sentenceLeadsBranch = "“Triggered by” only leads a branch"
	sentenceTooLarge    = `This Bot's rules could hold more waiting events than a Bot can store. Lower "keep up to" on a trigger.`
	sentenceTooMany     = "This Bot's rules could hold more than a Bot can store. Remove a trigger or a rule."
)

// opNotGroup is the NOT group the condition model has none of, refused with the sentence that points at each
// item's own negative.
const opNotGroup CondOp = "not"

type addFunc func(path, msg string)

type errList []FieldError

func (e *errList) add(path, msg string) { *e = append(*e, FieldError{Field: path, Error: msg}) }

// Validate checks one rule's spec in full and answers every error, each at its JSON path; nil when it is valid.
// The context-mention check is command.ValidateRuleSpec's, which calls this.
func Validate(spec Spec, ctx ValidateContext) []FieldError {
	var errs errList
	checkName(spec.Name, errs.add)
	checkPrompt(spec.Prompt, errs.add)
	checkTriggers(spec.Triggers, ctx, errs.add)
	if spec.Condition != nil {
		checkCondition(&spec, errs.add)
	}
	return errs
}

func hasControl(s string, layout bool) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && (!layout || (r != '\n' && r != '\t'))
	})
}

func checkName(name string, add addFunc) {
	switch {
	case !utf8.ValidString(name):
		add("name", "Use plain text")
	case strings.TrimSpace(name) == "":
		add("name", "Give the rule a name")
	case utf8.RuneCountInString(name) > maxNameRunes:
		add("name", fmt.Sprintf("Use at most %d characters", maxNameRunes))
	case hasControl(name, false):
		add("name", "Keep the name on one line")
	}
}

func checkPrompt(prompt string, add addFunc) {
	switch {
	case !utf8.ValidString(prompt):
		add("prompt", "Use plain text")
	case strings.TrimSpace(prompt) == "":
		add("prompt", "Write what the Bot should do")
	case len(prompt) > maxPromptBytes:
		add("prompt", fmt.Sprintf("Use at most %d KiB", maxPromptBytes>>10))
	case hasControl(prompt, true):
		add("prompt", "Remove the control characters")
	}
}

func checkTriggers(ts []Trigger, ctx ValidateContext, add addFunc) {
	if len(ts) == 0 || len(ts) > maxTriggers {
		add("triggers", fmt.Sprintf("A rule has 1 to %d triggers", maxTriggers))
	}
	seen := map[string]bool{}
	for i := range ts {
		path := fmt.Sprintf("triggers[%d]", i)
		checkTriggerID(ts[i].ID, ctx, seen, path+".id", add)
		checkTrigger(&ts[i], path, add)
	}
}

func checkTriggerID(id string, ctx ValidateContext, seen map[string]bool, path string, add addFunc) {
	if id == "" {
		return
	}
	switch {
	case seen[id]:
		add(path, "Two triggers have this id")
	case strings.HasPrefix(id, NewIDPrefix):
		if !validRef(id[len(NewIDPrefix):]) {
			add(path, "A new trigger's id is new: and 1 to 16 letters, digits, - or _")
		}
	case !ctx.Kept[id]:
		add(path, "This rule has no trigger with this id")
	}
	seen[id] = true
}

func validRef(ref string) bool {
	if ref == "" || len(ref) > 16 {
		return false
	}
	return !strings.ContainsFunc(ref, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
	})
}

func checkTrigger(t *Trigger, path string, add addFunc) {
	def, ok := kinds[t.Kind]
	if !ok {
		add(path+".kind", "marotte has no trigger of this kind")
		return
	}
	for _, p := range []struct {
		name string
		set  bool
	}{{payloadEvery, t.Every != nil}, {payloadAt, t.At != nil}} {
		switch {
		case p.set && p.name != def.payload:
			add(path+"."+p.name, "This kind of trigger has no "+p.name+" settings")
		case !p.set && p.name == def.payload:
			add(path+"."+p.name, "Fill in this trigger")
		}
	}
	checkLimits(t.Limits, def.time, path+".limits", add)
	if def.check != nil {
		def.check(t, path+"."+def.payload, add)
	}
}

func checkLimits(l *TriggerLimits, timeKind bool, path string, add addFunc) {
	switch {
	case timeKind && l != nil:
		add(path, "A schedule paces itself and takes no limits")
		return
	case timeKind:
		return
	case l == nil:
		add(path, "Set how often this trigger may send")
		return
	}
	if l.MaxPerHour < minPerHour || l.MaxPerHour > maxPerHour {
		add(path+".max_per_hour", fmt.Sprintf("Use %d to %d an hour", minPerHour, maxPerHour))
	}
	if l.MaxPerDay < 0 || l.MaxPerDay > maxPerDay {
		add(path+".max_per_day", fmt.Sprintf("Use up to %d a day, or no daily cap", maxPerDay))
	}
	if l.HoldCap < minHoldCap || l.HoldCap > maxHoldCap {
		add(path+".hold_cap", fmt.Sprintf("Keep %d to %d waiting events", minHoldCap, maxHoldCap))
	}
	if l.Hold != "" && l.Hold != HoldFIFO && l.Hold != holdNewest {
		add(path+".hold", "Keep every event or only the newest")
	}
}

func checkEvery(t *Trigger, path string, add addFunc) {
	e := t.Every
	if e == nil {
		return
	}
	if e.Minutes < MinEveryMinutes || e.Minutes > MaxEveryMinutes {
		add(path+".minutes", fmt.Sprintf("Use %s to %s", formatMinutes(MinEveryMinutes), formatMinutes(MaxEveryMinutes)))
	}
	if e.Anchor != "" && e.Anchor != AnchorLast && e.Anchor != AnchorIdle {
		add(path+".anchor", "Count from the last run or from the Bot's last turn")
	}
}

func checkAt(t *Trigger, path string, add addFunc) {
	a := t.At
	if a == nil {
		return
	}
	if _, ok := parseClock(a.Time); !ok {
		add(path+".time", "Use a time from 00:00 to 23:59")
	}
	noWeekdays := func() {
		if len(a.Weekdays) > 0 {
			add(path+".weekdays", "Days of the week apply only to “on these days”")
		}
	}
	noMonthDay := func() {
		if a.MonthDay != 0 {
			add(path+".month_day", "A day of the month applies only to “monthly”")
		}
	}
	switch a.Days {
	case DaysDaily:
		noWeekdays()
		noMonthDay()
	case DaysWeekdays:
		checkWeekdays(a.Weekdays, path+".weekdays", add)
		noMonthDay()
	case DaysMonthly:
		noWeekdays()
		if a.MonthDay != MonthDayLast && (a.MonthDay < 1 || a.MonthDay > 31) {
			add(path+".month_day", "Use a day from 1 to 31, or the last day")
		}
	default:
		add(path+".days", "Pick every day, some days of the week, or a day of the month")
	}
}

func checkWeekdays(days []int, path string, add addFunc) {
	if len(days) == 0 {
		add(path, "Pick at least one day")
		return
	}
	var seen [7]bool
	for _, d := range days {
		if d < 0 || d > 6 || seen[d] {
			add(path, "Pick each day once")
			return
		}
		seen[d] = true
	}
}

// parseClock reads a strict HH:MM as minutes after midnight.
func parseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func checkTimeLeaf(l *Leaf, path string, add addFunc) {
	w := l.Time
	if w == nil {
		return
	}
	checkWeekdays(w.Weekdays, path+".weekdays", add)
	from, okFrom := parseClock(w.From)
	to, okTo := parseClock(w.To)
	if !okFrom {
		add(path+".from", "Use a time from 00:00 to 23:59")
	}
	switch {
	case !okTo:
		add(path+".to", "Use a time from 00:00 to 23:59")
	case okFrom && from == to:
		add(path+".to", "End the window at another time than it starts")
	}
}

func checkRuleLeaf(l *Leaf, path string, add addFunc) {
	if l.Rule == nil {
		return
	}
	if m := l.Rule.Minutes; m < minLastRanMinutes || m > maxLastRanMinutes {
		add(path+".minutes", fmt.Sprintf("Use %s to %s", formatMinutes(minLastRanMinutes), formatMinutes(maxLastRanMinutes)))
	}
}

// condCheck walks one rule's condition tree.
type condCheck struct {
	add addFunc
	// byID is the rule's triggers by the id the save names them with.
	byID    map[string]*Trigger
	covered map[string]bool
	leaves  int
	// flow says the root is the editor's branch form: an any whose every child is an all holding a triggered_by.
	flow     bool
	misplace bool
}

func checkCondition(spec *Spec, add addFunc) {
	v := condCheck{add: add, byID: map[string]*Trigger{}, covered: map[string]bool{}, flow: isFlow(spec.Condition)}
	for i := range spec.Triggers {
		if id := spec.Triggers[i].ID; id != "" {
			v.byID[id] = &spec.Triggers[i]
		}
	}
	v.node(spec.Condition, "condition", 0, false)
	if !v.flow || v.misplace {
		return
	}
	for i := range spec.Triggers {
		if !v.covered[spec.Triggers[i].ID] {
			add("condition", sentenceLeadsBranch)
			return
		}
	}
}

func isFlow(root *Cond) bool {
	if root.Leaf != nil || root.Op != OpAny || len(root.Children) == 0 {
		return false
	}
	for _, b := range root.Children {
		if b.Leaf != nil || b.Op != OpAll || !slices.ContainsFunc(b.Children, func(c Cond) bool {
			return c.Leaf != nil && c.Leaf.Kind == LeafTriggeredBy
		}) {
			return false
		}
	}
	return true
}

// node checks c at path; depth counts the groups above it and lead says it is a branch's first child.
func (v *condCheck) node(c *Cond, path string, depth int, lead bool) {
	switch {
	case c.Leaf != nil && c.Op != "":
		v.add(path+".leaf", "An item cannot also be a group")
	case c.Leaf != nil:
		if len(c.Children) > 0 {
			v.add(path+".children", "An item holds no other items")
		}
		v.leaf(c.Leaf, path, lead)
	case c.Op == "":
		v.add(path, "Add an item or a group")
	default:
		v.group(c, path, depth)
	}
}

func (v *condCheck) group(c *Cond, path string, depth int) {
	switch {
	case c.Op == opNotGroup:
		v.add(path+".op", sentenceNotGroup)
		return
	case c.Op != OpAll && c.Op != OpAny:
		v.add(path+".op", "Join a group with AND or OR")
		return
	case depth > maxGroupDepth:
		v.add(path, fmt.Sprintf("Groups nest at most %d deep", maxGroupDepth))
		return
	}
	if n := len(c.Children); n == 0 || n > maxGroupChildren {
		v.add(path+".children", fmt.Sprintf("A group holds 1 to %d items", maxGroupChildren))
	}
	for i := range c.Children {
		v.node(&c.Children[i], fmt.Sprintf("%s.children[%d]", path, i), depth+1, v.flow && depth == 1 && i == 0)
	}
}

func (v *condCheck) leaf(l *Leaf, path string, lead bool) {
	v.leaves++
	if v.leaves == maxLeaves+1 {
		v.add(path, fmt.Sprintf("A rule holds at most %d items", maxLeaves))
	}
	lp := path + ".leaf"
	def, ok := leafDefs[l.Kind]
	if !ok {
		v.add(lp+".kind", "marotte has no item of this kind")
		return
	}
	for _, p := range []struct {
		name string
		set  bool
	}{{payloadEvent, l.Event != nil}, {payloadTime, l.Time != nil}, {payloadRule, l.Rule != nil}, {payloadBy, l.By != nil}} {
		switch {
		case p.set && p.name != def.payload:
			v.add(lp+"."+p.name, "This item has no "+p.name+" settings")
		case !p.set && p.name == def.payload:
			v.add(lp+"."+p.name, "Fill in this item")
		}
	}
	switch {
	case l.Kind == LeafTriggeredBy:
		v.triggeredBy(l.By, path, lp+".by", lead)
	case l.Kind == leafEventField && l.Event != nil:
		v.eventLeaf(l.Event, lp+".event")
	case def.check != nil:
		def.check(l, lp+"."+def.payload, v.add)
	}
}

func (v *condCheck) triggeredBy(by *ByLeaf, path, bp string, lead bool) {
	if !lead {
		v.add(path, sentenceLeadsBranch)
		v.misplace = true
	}
	if by == nil {
		return
	}
	if len(by.Triggers) == 0 {
		v.add(bp+".triggers", "Pick at least one trigger")
		return
	}
	for k, id := range by.Triggers {
		p := fmt.Sprintf("%s.triggers[%d]", bp, k)
		switch {
		case v.byID[id] == nil:
			v.add(p, "This rule has no such trigger")
		case !lead:
		case v.covered[id]:
			v.add(p, sentenceLeadsBranch)
		default:
			v.covered[id] = true
		}
	}
}

func (v *condCheck) eventLeaf(e *EventLeaf, path string) {
	d, opOK := findOp(e.Op)
	if !opOK {
		v.add(path+".op", "Pick how to compare the field")
	} else {
		checkOperand(e, d, path, v.add)
	}
	if e.Trigger != "" && v.byID[e.Trigger] == nil {
		v.add(path+".trigger", "This rule has no such trigger")
		return
	}
	if e.Field == "" {
		v.add(path+".field", "Pick a field")
		return
	}
	found, list := v.fieldOn(e)
	switch {
	case !found:
		v.add(path+".field", "No trigger of this rule has this field")
	case list && opOK && !d.list:
		v.add(path+".op", "A list cannot be compared as a number")
	}
}

// fieldOn answers whether a trigger the leaf applies to carries its field, and whether one carries it as a list.
func (v *condCheck) fieldOn(e *EventLeaf) (found, list bool) {
	for id, t := range v.byID {
		def, ok := kinds[t.Kind]
		if !ok || (e.Trigger != "" && id != e.Trigger) {
			continue
		}
		found = found || slices.Contains(baseFields, e.Field) || slices.Contains(def.fields, e.Field)
		list = list || slices.Contains(def.lists, e.Field)
	}
	return found, list
}

func checkOperand(e *EventLeaf, d opDef, path string, add addFunc) {
	switch {
	case d.op == fieldOneOf:
		checkOneOf(e, path, add)
	case d.value:
		if len(e.Values) > 0 {
			add(path+".values", "This comparison reads one value")
		}
		if msg := valueError(e.Value, d.op == fieldAbove || d.op == fieldBelow); msg != "" {
			add(path+".value", msg)
		}
	default:
		if e.Value != "" {
			add(path+".value", "This comparison reads no value")
		}
		if len(e.Values) > 0 {
			add(path+".values", "This comparison reads no value")
		}
	}
}

func checkOneOf(e *EventLeaf, path string, add addFunc) {
	if e.Value != "" {
		add(path+".value", "This comparison reads a list of values")
	}
	if n := len(e.Values); n == 0 || n > maxOneOfValues {
		add(path+".values", fmt.Sprintf("Give 1 to %d values", maxOneOfValues))
		return
	}
	for i, val := range e.Values {
		if msg := valueError(val, false); msg != "" {
			add(fmt.Sprintf("%s.values[%d]", path, i), msg)
		}
	}
}

func valueError(s string, number bool) string {
	switch {
	case len(s) > maxValueBytes:
		return fmt.Sprintf("Use at most %d bytes", maxValueBytes)
	case !utf8.ValidString(s) || hasControl(s, false):
		return "Use plain text on one line"
	case number:
		if f, err := strconv.ParseFloat(s, 64); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return "Use a number"
		}
	}
	return ""
}

// The worst case a Bot's record can grow to, per what a full queue and full state hold.
const (
	kib = 1 << 10
	// eventBytes is one stored event: its 8 KiB block and a webhook's 4 KiB of condition fields.
	eventBytes = 12 * kib
	// ruleMarkBytes is one rule's marks and recent event ids.
	ruleMarkBytes = 22 * kib
	// triggerStateBytes is one trigger's 16 KiB cursor, 12 KiB last event and 1 KiB of counts.
	triggerStateBytes = 29 * kib
)

// ValidateBot checks what binds a Bot's rules together: at most MaxRules, and their worst-case size at most
// maxBotBytes, refused at the largest hold cap when lowering one can help.
func ValidateBot(specs []Spec) []FieldError {
	var errs errList
	if len(specs) > MaxRules {
		errs.add("rules", fmt.Sprintf("A Bot has at most %d rules", MaxRules))
	}
	size, largest := worstCase(specs)
	switch {
	case size <= maxBotBytes:
	case largest == "":
		errs.add("rules", sentenceTooMany)
	default:
		errs.add(largest, sentenceTooLarge)
	}
	return errs
}

// worstCase answers the Bot's worst-case encoded size and the path of its largest hold cap, the first on a tie.
func worstCase(specs []Spec) (size int, largest string) {
	// The one event the Bot may hold queued on its chat, which no cap drops.
	size = eventBytes
	most := 0
	for i := range specs {
		enc, err := json.Marshal(&specs[i])
		if err != nil {
			return maxBotBytes + 1, ""
		}
		// A rule's one waiting Run now event belongs to no trigger, so no hold cap counts it.
		size += len(enc) + eventBytes + ruleMarkBytes
		for j, t := range specs[i].Triggers {
			size += triggerStateBytes
			if kinds[t.Kind].time || t.Limits == nil {
				size += eventBytes
				continue
			}
			size += t.Limits.HoldCap * eventBytes
			if t.Limits.HoldCap > most {
				most = t.Limits.HoldCap
				largest = fmt.Sprintf("rules[%d].triggers[%d].limits.hold_cap", i, j)
			}
		}
	}
	return size, largest
}

// Rearms says a kept trigger's edit resets its state: any change but its limits, which apply from the next
// delivery.
func Rearms(stored, incoming *Trigger) bool {
	a, errA := json.Marshal(rearmView(*stored))
	b, errB := json.Marshal(rearmView(*incoming))
	return errA != nil || errB != nil || !bytes.Equal(a, b)
}

func rearmView(t Trigger) Trigger {
	t.ID, t.Limits = "", nil
	if t.Every != nil && t.Every.Anchor == "" {
		e := *t.Every
		e.Anchor = AnchorLast
		t.Every = &e
	}
	return t
}
