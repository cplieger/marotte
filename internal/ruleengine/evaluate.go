package ruleengine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/rule"
)

// evalCond is rule.Eval, a variable so a test can make an evaluation panic.
var evalCond = rule.Eval

// recentIDs is how many evaluated event ids a rule keeps, so a source reporting an event twice fires it once.
const recentIDs = 100

// evalPanicked is the failure of an evaluation that panicked.
const evalPanicked = "the evaluation panicked"

// The skip mark's sentences; the reason after them is Eval's.
const (
	skippedPrefix      = "Skipped: "
	skippedAfterPrefix = "Skipped after waiting: "
)

// manualKind is a Run now event's trigger label in metrics and logs: it belongs to no trigger.
const manualKind = "manual"

// The log keys every line about one rule's event carries.
const (
	keyChatID  = "chat_id"
	keyRuleID  = "rule_id"
	keyTrigger = "trigger"
	keyEventID = "event_id"
)

// eventArgs is a log line's keys for one event of a rule, then more.
func eventArgs(chatID marotte.ChatID, ruleID, kind, eventID string, more ...any) []any {
	return append([]any{keyChatID, chatID, keyRuleID, ruleID, keyTrigger, kind, keyEventID, eventID}, more...)
}

// rulesArmed reports whether b's time triggers run: started, not paused and not closed. A paused Bot still delivers
// a Run now press.
func rulesArmed(b *marotte.Bot) bool {
	return b.StartedAt != 0 && !b.Paused && b.ClosedAt == 0
}

// chatBusy reports c's run list holding a live or pending entry, which a delivery would queue behind, or a rule's
// prompt handed to the chat and not yet counted sent, so a Bot holds at most one queued event.
func chatBusy(c *marotte.Chat) bool {
	return slices.ContainsFunc(c.Run, func(e marotte.RunEntry) bool {
		return e.State.Live() || e.State == marotte.RunPending
	}) || slices.ContainsFunc(c.Bot.Rules, func(r rule.Rule) bool {
		return slices.ContainsFunc(r.Status.Queue, func(q rule.QueuedEvent) bool { return q.Queued })
	})
}

// kindOf is trigger's kind in spec, manualKind for a Run now event.
func kindOf(spec *rule.Spec, trigger string) string {
	for i := range spec.Triggers {
		if spec.Triggers[i].ID == trigger {
			return string(spec.Triggers[i].Kind)
		}
	}
	return manualKind
}

// evaluation is one evaluation a write made, logged and counted once the write landed.
type evaluation struct {
	// failed is an evaluation that panicked: its event is dropped with the rule's failure.
	failed                    error
	rule, kind, event, reason string
	fired                     bool
}

func (e *Engine) record(chatID marotte.ChatID, evs []evaluation) {
	for _, ev := range evs {
		if ev.failed != nil {
			obs.RuleDelivery(obs.RuleDeliveryFailed)
			e.log.Error("rule evaluation failed", eventArgs(chatID, ev.rule, ev.kind, ev.event, "error", ev.failed)...)
			continue
		}
		outcome, msg := obs.RuleSkipped, "rule skipped"
		if ev.fired {
			outcome, msg = obs.RuleFired, "rule fired"
		}
		obs.RuleEvaluated(ev.kind, outcome)
		if ev.fired {
			e.log.Info(msg, eventArgs(chatID, ev.rule, ev.kind, ev.event)...)
		} else {
			e.log.Info(msg, eventArgs(chatID, ev.rule, ev.kind, ev.event, "reason", ev.reason)...)
		}
	}
}

// botState is the Bot's state an evaluation reads.
type botState struct {
	busy, waiting bool
}

// snapshot is what an evaluation of ev reads at now, with lastFire as the rule's last fire.
func snapshot(ev *rule.QueuedEvent, lastFire int64, now time.Time, st botState) (rule.Snapshot, error) {
	data, err := rule.DecodeEvent(ev.Data)
	if err != nil {
		return rule.Snapshot{}, err
	}
	return rule.Snapshot{Now: now, Trigger: ev.Trigger, Event: data, LastFire: lastFire, BotBusy: st.busy, BotWaiting: st.waiting}, nil
}

// judge evaluates r's condition over ev; an event whose stored data does not decode fails as a panic does, so it is
// dropped with the rule's failure rather than held for ever.
func judge(r *rule.Rule, ev *rule.QueuedEvent, lastFire int64, now time.Time, st botState) (rule.Result, error) {
	s, err := snapshot(ev, lastFire, now, st)
	if err != nil {
		return rule.Result{}, err
	}
	return safeEval(r.Spec.Condition, &s)
}

func lastFireAt(r *rule.Rule) int64 {
	if r.Status.LastFire == nil {
		return 0
	}
	return r.Status.LastFire.At
}

func mark(r *rule.Rule, ev *rule.QueuedEvent, reason string, now time.Time) *rule.Mark {
	return &rule.Mark{
		Trigger: ev.Trigger, EventID: ev.EventID, Summary: rule.EventPhrase(&r.Spec, ev.Trigger),
		Reason: reason, At: now.UnixMilli(),
	}
}

// safeEval is evalCond with a panic turned into an error, so one poisoned event cannot stop every Bot's rules.
func safeEval(c *rule.Cond, s *rule.Snapshot) (res rule.Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s: %v", evalPanicked, p)
		}
	}()
	return evalCond(c, s), nil
}

// evaluate runs r's condition over ev and fires it into the queue or marks the skip; a failed evaluation marks the
// rule's failure. kept is false for a fired event Enqueue refuses, which changes nothing.
func evaluate(r *rule.Rule, ev *rule.QueuedEvent, st botState, now time.Time) (out evaluation, kept bool) {
	res, err := judge(r, ev, lastFireAt(r), now, st)
	out = evaluation{rule: r.ID, kind: kindOf(&r.Spec, ev.Trigger), event: ev.EventID, reason: res.Reason, failed: err}
	switch {
	case err != nil:
		r.Status.LastFailure = mark(r, ev, failedPrefix+err.Error(), now)
	case res.Value != rule.True:
		r.Status.LastSkip = mark(r, ev, skippedPrefix+res.Reason, now)
	default:
		ev.Stage, ev.FiredAt, ev.PrevFire = rule.StageFired, now.UnixMilli(), lastFireAt(r)
		if !rule.Enqueue(&r.Status, &r.Spec, ev) {
			return out, false
		}
		out.fired = true
		r.Status.LastFire = mark(r, ev, "", now)
	}
	return out, true
}

// evaluateDue evaluates every armed time trigger of c whose slot is due at now, in one write, and answers the record
// after it.
func (e *Engine) evaluateDue(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat, now time.Time) (*marotte.Chat, error) {
	live := e.deps.TurnLive(chatID)
	if !rulesArmed(c.Bot) || !anyDue(c, live, now) {
		return c, nil
	}
	waiting := e.deps.Waiting(chatID)
	var done []evaluation
	after, err := e.write(ctx, chatID, func(c *marotte.Chat) bool {
		done = done[:0]
		if !rulesArmed(c.Bot) {
			return false
		}
		st := botState{busy: live || chatBusy(c), waiting: waiting}
		moved := false
		for i := range c.Bot.Rules {
			evs, m := evaluateRule(&c.Bot.Rules[i], c.LastTurnEndedAt, live, st, now)
			done = append(done, evs...)
			moved = moved || m
		}
		return moved
	})
	if err != nil {
		return nil, err
	}
	e.record(chatID, done)
	return after, nil
}

func anyDue(c *marotte.Chat, live bool, now time.Time) bool {
	for i := range c.Bot.Rules {
		r := &c.Bot.Rules[i]
		if !r.Spec.Enabled {
			continue
		}
		for _, t := range r.Spec.Triggers {
			st, ok := r.Status.Triggers[t.ID]
			if !ok || st.ArmedAt == 0 {
				continue
			}
			if _, due := rule.DueSlot(t, &st, c.LastTurnEndedAt, live, now); due {
				return true
			}
		}
	}
	return false
}

// evaluateRule evaluates each of r's armed time triggers due at now, moving its anchor whatever the outcome, and
// reports whether it moved any: a slot already in Recent evaluates nothing but its anchor still moves.
func evaluateRule(r *rule.Rule, lastTurnEndedAt int64, live bool, bs botState, now time.Time) (out []evaluation, moved bool) {
	if !r.Spec.Enabled {
		return nil, false
	}
	for _, t := range r.Spec.Triggers {
		st, ok := r.Status.Triggers[t.ID]
		if !ok || st.ArmedAt == 0 {
			continue
		}
		slot, due := rule.DueSlot(t, &st, lastTurnEndedAt, live, now)
		if !due {
			continue
		}
		rule.Evaluated(&st, now)
		r.Status.Triggers[t.ID] = st
		moved = true
		ev := rule.TimeEvent(r.Spec, t, slot, now)
		if slices.Contains(r.Status.Recent, ev.EventID) {
			continue
		}
		if done, kept := evaluate(r, &ev, bs, now); kept {
			r.Status.Recent = append(r.Status.Recent, ev.EventID)
			r.Status.Recent = r.Status.Recent[max(0, len(r.Status.Recent)-recentIDs):]
			out = append(out, done)
		}
	}
	return out, moved
}

// RunNow evaluates ruleID's manual event manual:<opID> now, in the caller, and wakes the loop to deliver it. A
// replayed opID evaluates nothing and answers what the first press answered, stored with the rule in the press's
// own write.
func (e *Engine) RunNow(ctx context.Context, chatID marotte.ChatID, ruleID, opID string) (command.RunNowAnswer, error) {
	p := runNowPress{
		rule: ruleID, op: opID, now: time.Now(),
		live: e.deps.TurnLive(chatID), waiting: e.deps.Waiting(chatID),
	}
	if _, err := e.deps.Store.Mutate(ctx, chatID, p.apply); err != nil {
		return command.RunNowAnswer{}, err
	}
	if !p.found {
		return command.RunNowAnswer{}, command.StatusError(http.StatusNotFound, errRuleGone)
	}
	e.record(chatID, p.done)
	if p.failed != nil {
		return command.RunNowAnswer{}, command.StatusError(http.StatusInternalServerError, p.failed)
	}
	e.Reindex(chatID)
	return p.ans, nil
}

// runNowPress is one Run now press's write and what it found.
type runNowPress struct {
	now time.Time
	// failed is the press's evaluation failure, or a replayed press's.
	failed        error
	ans           command.RunNowAnswer
	rule, op      string
	done          []evaluation
	live, waiting bool
	found         bool
}

func (p *runNowPress) apply(c *marotte.Chat, exists bool) bool {
	p.done, p.found, p.ans, p.failed = nil, false, command.RunNowAnswer{}, nil
	if !exists || c.Bot == nil {
		return false
	}
	i := slices.IndexFunc(c.Bot.Rules, func(r rule.Rule) bool { return r.ID == p.rule })
	if i < 0 {
		return false
	}
	p.found = true
	r := &c.Bot.Rules[i]
	ev := rule.ManualEvent(r.Spec, p.op, p.now)
	if first, ok := rule.PressOf(&r.Status, ev.EventID); ok {
		p.ans, p.failed = answerOf(first)
		return false
	}
	busy := p.live || chatBusy(c)
	eval, kept := evaluate(r, &ev, botState{busy: busy, waiting: p.waiting}, p.now)
	if kept {
		p.done = []evaluation{eval}
	}
	p.ans, p.failed = answerOf(rule.RecordPress(&r.Status, pressOf(ev.EventID, &eval, kept, busy)))
	c.Bot.RulesVersion++
	return true
}

// pressOf is the answer a press decided as eval: queued when its Bot is busy or Enqueue refused it beside a waiting
// press.
func pressOf(eventID string, eval *evaluation, kept, busy bool) rule.Press {
	switch {
	case eval.failed != nil:
		return rule.Press{EventID: eventID, Outcome: rule.PressFailed, Reason: eval.failed.Error()}
	case !kept || (eval.fired && busy):
		return rule.Press{EventID: eventID, Outcome: rule.PressQueued}
	case eval.fired:
		return rule.Press{EventID: eventID, Outcome: rule.PressFired}
	default:
		return rule.Press{EventID: eventID, Outcome: rule.PressSkipped, Reason: eval.reason}
	}
}

// answerOf is Run now's answer for a stored press, the error of one that failed.
func answerOf(p rule.Press) (command.RunNowAnswer, error) {
	switch p.Outcome {
	case rule.PressFired:
		return command.RunNowAnswer{Outcome: command.RunNowFired}, nil
	case rule.PressQueued:
		return command.RunNowAnswer{Outcome: command.RunNowQueued}, nil
	case rule.PressSkipped:
		return command.RunNowAnswer{Outcome: command.RunNowSkipped, Reason: p.Reason}, nil
	case rule.PressFailed:
		return command.RunNowAnswer{}, errors.New(p.Reason)
	}
	return command.RunNowAnswer{}, fmt.Errorf("a stored press has no outcome %q", p.Outcome)
}

// nextDue is the earliest due time of chatID's armed time triggers, zero for none; an idle trigger has none while
// the Bot works, since the turn's end reindexes it.
func (e *Engine) nextDue(chatID marotte.ChatID, c *marotte.Chat, now time.Time) time.Time {
	if !rulesArmed(c.Bot) {
		return time.Time{}
	}
	live := e.deps.TurnLive(chatID)
	var out time.Time
	for i := range c.Bot.Rules {
		r := &c.Bot.Rules[i]
		if !r.Spec.Enabled {
			continue
		}
		for _, t := range r.Spec.Triggers {
			st, ok := r.Status.Triggers[t.ID]
			if !ok || st.ArmedAt == 0 {
				continue
			}
			if d := rule.NextDue(t, &st, c.LastTurnEndedAt, live, now); !d.IsZero() {
				out = earliest(out, d)
			}
		}
	}
	return out
}
