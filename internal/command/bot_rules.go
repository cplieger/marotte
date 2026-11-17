package command

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// RuleIndex is told after every write that can change which of a Bot's triggers are armed, so the rule engine
// reads the Bot again. Reindex never blocks.
type RuleIndex interface {
	Reindex(chatID marotte.ChatID)
}

// RuleEngine is the rule engine as the Bot commands reach it.
type RuleEngine interface {
	RuleIndex
	// RunNow fires ruleID's manual event manual:<opID> through evaluation and answers what happened; a replayed
	// opID answers its first outcome. It writes the rule's status itself.
	RunNow(ctx context.Context, chatID marotte.ChatID, ruleID, opID string) (RunNowAnswer, error)
}

// RunNowOutcome is what a Run now press did.
type RunNowOutcome string

// The outcomes, spelled as bot_rule_run_now answers them.
const (
	RunNowFired   RunNowOutcome = "fired"
	RunNowQueued  RunNowOutcome = "queued"
	RunNowSkipped RunNowOutcome = "skipped"
)

// RunNowAnswer is RunNow's answer; Reason is the skip reason, set only for RunNowSkipped.
type RunNowAnswer struct {
	Outcome RunNowOutcome `json:"outcome"`
	Reason  string        `json:"reason,omitempty"`
}

const (
	reasonBotClosed    = "bot_closed"
	reasonRuleNotFound = "rule_not_found"
)

var (
	errBotClosed    = errors.New("this Bot is closed; open it again to run its rules")
	errRuleNotFound = errors.New("this Bot has no such rule; reload its setup")
	errNoRuleEngine = errors.New("rules are not running yet; try again in a moment")
)

// The sentences a refused rule field shows beside it.
const (
	sentenceRuleUnknown   = "This rule no longer exists; reload the setup and save again"
	sentenceRuleTwice     = "This rule is listed twice"
	sentenceMentionPrompt = "Context mentions are not supported in rule prompts; name the file in words"
	sentenceMention       = "Context mentions are not supported here"
)

// ValidateRuleSpec is rule.Validate's answer for spec plus a refusal at each field holding a context-mention token,
// which the notify door would refuse in the rendered prompt. bot_save and the dry-run route validate only through it.
func ValidateRuleSpec(spec rule.Spec, ctx rule.ValidateContext) []rule.FieldError {
	errs := rule.Validate(spec, ctx)
	if mentionRe.MatchString(spec.Name) {
		errs = append(errs, rule.FieldError{Field: "name", Error: sentenceMention})
	}
	if mentionRe.MatchString(spec.Prompt) {
		errs = append(errs, rule.FieldError{Field: "prompt", Error: sentenceMentionPrompt})
	}
	return errs
}

// The id sizes: a rule's 16 random bytes and a trigger's 8, in the id vocabulary's lowercase base32.
const (
	ruleIDBytes    = 16
	triggerIDBytes = 8
)

// newBotBlock is a new draft's bot block, holding the starter rule.
func newBotBlock() *marotte.Bot {
	return &marotte.Bot{Rules: []rule.Rule{starterRule()}}
}

// starterRule is the rule a new Bot draft starts with: it checks in two hours after the Bot's last reply.
func starterRule() rule.Rule {
	return rule.Rule{
		ID: ids.New(ruleIDBytes, ids.StdLower),
		Spec: rule.Spec{
			Name: "Check in on the goal", Enabled: true,
			Prompt: "Re-read your goal and your memory file, check the workflows you launched, then take the next " +
				"step toward the goal. If nothing is due, answer in one line and stop.",
			Triggers: []rule.Trigger{{
				ID: ids.New(triggerIDBytes, ids.StdLower), Kind: rule.KindEvery,
				Every: &rule.EveryTrigger{Minutes: 120, Anchor: rule.AnchorIdle},
			}},
		},
	}
}

// rulesSave is a bot_save's rules decoded; nil on a botSave that sends none.
type rulesSave struct {
	op    string
	rules []incomingRule
}

type incomingRule struct {
	errs []rule.FieldError
	id   string
	spec rule.Spec
}

func decodeRulesSave(p *marotte.BotSaveCommand) *rulesSave {
	if p.Rules == nil {
		return nil
	}
	out := &rulesSave{op: p.OpID, rules: make([]incomingRule, len(p.Rules))}
	for i, r := range p.Rules {
		spec, errs := rule.Decode(r.Spec)
		out.rules[i] = incomingRule{id: r.RuleID, spec: spec, errs: errs}
	}
	return out
}

// rulePlan is a validated rules save against the stored rules: each rule's id and spec with every id minted and
// every placeholder rewritten, in the sent order.
type rulePlan struct {
	rules      []rule.Rule
	ruleIDs    []string
	triggerIDs [][]string
}

// planRules validates s against stored and mints the ids it needs, naming every refused field by its JSON path.
func planRules(s *rulesSave, stored []rule.Rule) (rulePlan, []fieldError) {
	var bad []fieldError
	refuse := func(path string, err rule.FieldError) {
		bad = append(bad, fieldError{Field: path + err.Field, Error: err.Error})
	}
	byID := make(map[string]*rule.Rule, len(stored))
	for i := range stored {
		byID[stored[i].ID] = &stored[i]
	}
	seen := map[string]bool{}
	needsOp := false
	specs := make([]rule.Spec, 0, len(s.rules))
	for i := range s.rules {
		r := &s.rules[i]
		at := fmt.Sprintf("rules[%d].", i)
		var ctx rule.ValidateContext
		switch kept, ok := byID[r.id]; {
		case r.id == "":
			needsOp = true
		case !ok:
			refuse(at, rule.FieldError{Field: "rule_id", Error: sentenceRuleUnknown})
			continue
		case seen[r.id]:
			refuse(at, rule.FieldError{Field: "rule_id", Error: sentenceRuleTwice})
			continue
		default:
			seen[r.id] = true
			ctx.Kept = triggerIDSet(&kept.Spec)
		}
		for _, e := range r.errs {
			refuse(at, e)
		}
		if len(r.errs) > 0 {
			continue
		}
		for _, e := range ValidateRuleSpec(r.spec, ctx) {
			refuse(at, e)
		}
		specs = append(specs, r.spec)
	}
	for _, e := range rule.ValidateBot(specs) {
		refuse("", e)
	}
	if needsOp && (s.op == "" || !ids.ValidIdent(s.op)) {
		bad = append(bad, fieldError{Field: "op_id", Error: ErrInvalidPayload.Error()})
	}
	if len(bad) > 0 {
		return rulePlan{}, bad
	}
	return mintRules(s), nil
}

func triggerIDSet(spec *rule.Spec) map[string]bool {
	set := make(map[string]bool, len(spec.Triggers))
	for _, t := range spec.Triggers {
		set[t.ID] = true
	}
	return set
}

// mintRules gives every new rule and trigger its id and rewrites each placeholder the conditions name.
func mintRules(s *rulesSave) rulePlan {
	plan := rulePlan{
		rules:      make([]rule.Rule, len(s.rules)),
		ruleIDs:    make([]string, len(s.rules)),
		triggerIDs: make([][]string, len(s.rules)),
	}
	for i := range s.rules {
		r := &s.rules[i]
		id := cmp.Or(r.id, ids.New(ruleIDBytes, ids.StdLower))
		spec := r.spec
		tids := mintTriggers(&spec)
		plan.rules[i] = rule.Rule{ID: id, Spec: spec}
		plan.ruleIDs[i], plan.triggerIDs[i] = id, tids
	}
	return plan
}

// mintTriggers gives spec's new triggers their ids, on a copy of its triggers and condition, and answers every
// trigger id in order.
func mintTriggers(spec *rule.Spec) []string {
	spec.Triggers = slices.Clone(spec.Triggers)
	minted := map[string]string{}
	tids := make([]string, len(spec.Triggers))
	for j := range spec.Triggers {
		t := &spec.Triggers[j]
		if t.ID == "" || strings.HasPrefix(t.ID, rule.NewIDPrefix) {
			fresh := ids.New(triggerIDBytes, ids.StdLower)
			if t.ID != "" {
				minted[t.ID] = fresh
			}
			t.ID = fresh
		}
		tids[j] = t.ID
	}
	if spec.Condition != nil && len(minted) > 0 {
		c := rewriteTriggers(*spec.Condition, minted)
		spec.Condition = &c
	}
	return tids
}

// rewriteTriggers is c with every trigger id minted names replaced, built afresh so the decoded spec is untouched.
func rewriteTriggers(c rule.Cond, minted map[string]string) rule.Cond {
	if c.Leaf != nil {
		l := *c.Leaf
		if l.By != nil {
			by := rule.ByLeaf{Triggers: make([]string, len(l.By.Triggers))}
			for i, id := range l.By.Triggers {
				by.Triggers[i] = cmp.Or(minted[id], id)
			}
			l.By = &by
		}
		if l.Event != nil && minted[l.Event.Trigger] != "" {
			ev := *l.Event
			ev.Trigger = minted[ev.Trigger]
			l.Event = &ev
		}
		c.Leaf = &l
		return c
	}
	children := make([]rule.Cond, len(c.Children))
	for i := range c.Children {
		children[i] = rewriteTriggers(c.Children[i], minted)
	}
	c.Children = children
	return c
}

// rulesArmed reports whether b's enabled rules run: it is started, not paused and not closed.
func rulesArmed(b *marotte.Bot) bool {
	return b.StartedAt != 0 && !b.Paused && b.ClosedAt == 0
}

// ownRules gives b a deep enough copy of its rules for a write to change their status without touching the record
// the store handed out.
func ownRules(b *marotte.Bot) {
	rs := slices.Clone(b.Rules)
	for i := range rs {
		rs[i].Status.Triggers = maps.Clone(rs[i].Status.Triggers)
		rs[i].Status.Queue = slices.Clone(rs[i].Status.Queue)
	}
	b.Rules = rs
}

// armTrigger starts id's next generation at now: the anchors and cursor reset, so nothing fires for the time
// before it, and ArmedAt rises above the last generation's.
func armTrigger(st *rule.Status, id string, now int64) {
	if st.Triggers == nil {
		st.Triggers = map[string]rule.TriggerState{}
	}
	st.Triggers[id] = rule.TriggerState{ArmedAt: max(now, st.Triggers[id].ArmedAt+1)}
}

func armRule(r *rule.Rule, now int64) {
	for _, t := range r.Spec.Triggers {
		armTrigger(&r.Status, t.ID, now)
	}
}

// dropWaiting drops st's waiting events into Dropped, all but one whose prompt is already queued in the chat and,
// with keepPress, a Run now press, which a paused Bot still delivers. It reports whether it dropped any.
func dropWaiting(st *rule.Status, keepPress bool) bool {
	kept := st.Queue[:0:0]
	for i := range st.Queue {
		if ev := &st.Queue[i]; ev.Queued || (keepPress && ev.Trigger == "") {
			kept = append(kept, *ev)
		}
	}
	dropped := len(st.Queue) - len(kept)
	st.Queue, st.Dropped = kept, st.Dropped+dropped
	return dropped > 0
}

// armBot arms every enabled rule of b once its rules run, with one stamp, and reports whether it armed any.
func armBot(b *marotte.Bot, now time.Time) bool {
	if !rulesArmed(b) || !slices.ContainsFunc(b.Rules, func(r rule.Rule) bool { return r.Spec.Enabled }) {
		return false
	}
	ownRules(b)
	for i := range b.Rules {
		if b.Rules[i].Spec.Enabled {
			armRule(&b.Rules[i], now.UnixMilli())
		}
	}
	b.RulesVersion++
	return true
}

// disarmBot drops what every rule of b holds waiting (dropWaiting).
func disarmBot(b *marotte.Bot, keepPress bool) {
	if !slices.ContainsFunc(b.Rules, func(r rule.Rule) bool { return len(r.Status.Queue) > 0 }) {
		return
	}
	ownRules(b)
	changed := false
	for i := range b.Rules {
		changed = dropWaiting(&b.Rules[i].Status, keepPress) || changed
	}
	if changed {
		b.RulesVersion++
	}
}

// applyRules writes plan onto b, whose earlier state was was: a kept rule keeps its status, a changed or added
// trigger is armed afresh while the rule can run, a trigger gone takes its waiting events with it, a disabled rule
// drops what waits, and a Bot that starts arms every enabled rule. Every arming shares now.
func applyRules(b, was *marotte.Bot, plan *rulePlan, now time.Time) {
	stored := make(map[string]*rule.Rule, len(b.Rules))
	for i := range b.Rules {
		stored[b.Rules[i].ID] = &b.Rules[i]
	}
	starts := !rulesArmed(was) && rulesArmed(b)
	out := make([]rule.Rule, len(plan.rules))
	for i := range plan.rules {
		r := plan.rules[i]
		if old, ok := stored[r.ID]; ok {
			r.Status = keptStatus(old, &r.Spec, rulesArmed(b) && !starts, now.UnixMilli())
		} else if rulesArmed(b) && r.Spec.Enabled && !starts {
			armRule(&r, now.UnixMilli())
		}
		out[i] = r
	}
	b.Rules = out
	b.RulesVersion++
	if starts {
		armBot(b, now)
	}
}

// keptStatus is old's status carried onto spec: armed says the rule's triggers arm on a change.
func keptStatus(old *rule.Rule, spec *rule.Spec, armed bool, now int64) rule.Status {
	st := old.Status
	st.Queue = slices.Clone(st.Queue)
	was := make(map[string]*rule.Trigger, len(old.Spec.Triggers))
	for i := range old.Spec.Triggers {
		was[old.Spec.Triggers[i].ID] = &old.Spec.Triggers[i]
	}
	states := make(map[string]rule.TriggerState, len(spec.Triggers))
	for i := range spec.Triggers {
		t := &spec.Triggers[i]
		prev, existed := was[t.ID]
		state := old.Status.Triggers[t.ID]
		switch {
		case existed && !rule.Rearms(prev, t) && (old.Spec.Enabled || !spec.Enabled):
		case armed && spec.Enabled:
			state = rule.TriggerState{ArmedAt: max(now, state.ArmedAt+1)}
		default:
			state = rule.TriggerState{ArmedAt: state.ArmedAt}
		}
		states[t.ID] = state
	}
	st.Triggers = states
	kept := st.Queue[:0:0]
	for i := range st.Queue {
		ev := &st.Queue[i]
		if _, ok := states[ev.Trigger]; ok || ev.Trigger == "" || ev.Queued {
			kept = append(kept, *ev)
		}
	}
	st.Dropped += len(st.Queue) - len(kept)
	st.Queue = kept
	if old.Spec.Enabled && !spec.Enabled {
		dropWaiting(&st, false)
	}
	return st
}

// rulesReplay is what a bot_save's op_id answers again.
type rulesReplay struct {
	ruleIDs    []string
	triggerIDs [][]string
	version    int
}

// replayKey is s's key in botRoles.replays; "" for a save with no rules or no op_id, which records nothing.
func replayKey(chatID marotte.ChatID, s *botSave) string {
	if s.rules == nil || s.rules.op == "" {
		return ""
	}
	return string(chatID) + "\x00" + s.rules.op
}

// planBotRules checks s's rules against cur's and keeps the plan in s, refusing every bad field in one 400.
func planBotRules(cur *marotte.Chat, s *botSave) error {
	if s.rules == nil {
		return nil
	}
	plan, bad := planRules(s.rules, cur.Bot.Rules)
	if len(bad) > 0 {
		return statusErrorFields(http.StatusBadRequest, reasonBotInvalid, bad, errBotInvalid)
	}
	s.plan = plan
	return nil
}

// reindex tells rules, when wired, to read chatID again.
func reindex(rules RuleIndex, chatID marotte.ChatID) {
	if rules != nil {
		rules.Reindex(chatID)
	}
}

// cmdBotRuleRunNow fires one rule of a started, open Bot now, through the rule engine, which evaluates and writes.
func cmdBotRuleRunNow(ctx context.Context, roles *botRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.BotRuleRunNowCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.RuleID == "" || p.OpID == "" || !ids.ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	c, ok := roles.chats.Get(ctx, cmd.ChatID)
	if err := startedBotRefusal(c, ok); err != nil {
		return nil, err
	}
	if c.Bot.ClosedAt != 0 {
		return nil, StatusErrorReason(http.StatusConflict, reasonBotClosed, errBotClosed)
	}
	if !slices.ContainsFunc(c.Bot.Rules, func(r rule.Rule) bool { return r.ID == p.RuleID }) {
		return nil, StatusErrorReason(http.StatusNotFound, reasonRuleNotFound, errRuleNotFound)
	}
	if roles.rules == nil {
		return nil, StatusError(http.StatusServiceUnavailable, errNoRuleEngine)
	}
	ans, err := roles.rules.RunNow(ctx, cmd.ChatID, p.RuleID, p.OpID)
	if err != nil {
		return nil, err
	}
	slog.Info("bots: rule run now", "chat_id", cmd.ChatID, "rule_id", p.RuleID, keyOutcome, ans.Outcome)
	out := map[string]any{keyOutcome: ans.Outcome}
	if ans.Reason != "" {
		out["reason"] = ans.Reason
	}
	return responseWith(out), nil
}
