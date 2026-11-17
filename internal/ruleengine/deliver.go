package ruleengine

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/rule"
)

// A delivery that may pass later is tried again after each wait in turn, then every retryEvery, maxTries in all.
var retryLadder = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

const (
	retryEvery = 30 * time.Minute
	maxTries   = 10
	// waitedFor is how long a fired event may wait before its rule's conditions are evaluated again for delivery.
	waitedFor = 30 * time.Second
)

// The failure mark's sentences; the reason after them is the refusal's.
const (
	failedPrefix      = "Could not deliver: "
	failedTriesPrefix = "Could not deliver after 10 tries: "
)

func retryDelay(tries int) time.Duration {
	if tries >= 1 && tries <= len(retryLadder) {
		return retryLadder[tries-1]
	}
	return retryEvery
}

func eventKey(ruleID, eventID string) string { return ruleID + "\x00" + eventID }

// retryAfter holds chatID's event key back from delivery until at.
func (e *Engine) retryAfter(chatID marotte.ChatID, key string, at time.Time) {
	if e.retryAt[chatID] == nil {
		e.retryAt[chatID] = map[string]time.Time{}
	}
	e.retryAt[chatID][key] = at
}

// candidate is a fired event waiting to be delivered and the earliest it may go.
type candidate struct {
	until   time.Time
	firedAt int64
	ruleIx  int
	queueIx int
	limit   rule.Limit
}

// candidates answers c's fired events not yet in the chat, earliest fired first and then in rule order, each with
// the time its paces and its retry wait allow it, and the retry waits still owed.
func candidates(c *marotte.Chat, retries map[string]time.Time, now time.Time) (out []candidate, kept map[string]time.Time) {
	kept = map[string]time.Time{}
	rules := c.Bot.Rules
	for i := range rules {
		for j := range rules[i].Status.Queue {
			q := &rules[i].Status.Queue[j]
			if q.Stage != rule.StageFired || q.Queued {
				continue
			}
			until, limit := rule.NextDelivery(rules, i, q.Trigger, now)
			if at, ok := retries[eventKey(rules[i].ID, q.EventID)]; ok {
				kept[eventKey(rules[i].ID, q.EventID)] = at
				if at.After(until) {
					until = at
				}
			}
			out = append(out, candidate{until: until, limit: limit, firedAt: q.FiredAt, ruleIx: i, queueIx: j})
		}
	}
	slices.SortStableFunc(out, func(a, b candidate) int { return cmp.Compare(a.firedAt, b.firedAt) })
	return out, kept
}

func (e *Engine) setCeiling(chatID marotte.ChatID, cands []candidate) {
	var until time.Time
	for _, c := range cands {
		if c.limit == rule.LimitBot {
			until = c.until
			break
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if until.IsZero() {
		delete(e.ceiling, chatID)
		return
	}
	e.ceiling[chatID] = until
}

// deliver sends c's next fired event when the Bot is free and its paces allow, and answers when it next needs a
// pass for delivery, zero when nothing waits.
func (e *Engine) deliver(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat, now time.Time) (time.Time, error) {
	cands, retries := candidates(c, e.retryAt[chatID], now)
	e.retryAt[chatID] = retries
	e.setCeiling(chatID, cands)
	if len(cands) == 0 || c.Bot.StartedAt == 0 || c.Bot.ClosedAt != 0 {
		return time.Time{}, nil
	}
	if e.deps.TurnLive(chatID) || chatBusy(c) {
		return now.Add(tick), nil
	}
	ready := slices.IndexFunc(cands, func(cd candidate) bool { return !cd.until.After(now) })
	if ready < 0 {
		return slices.MinFunc(cands, func(a, b candidate) int { return a.until.Compare(b.until) }).until, nil
	}
	if !e.deps.Ready() {
		return now.Add(tick), nil
	}
	release, ok := e.deps.Enter()
	if !ok {
		return now.Add(tick), nil
	}
	defer release()
	r := c.Bot.Rules[cands[ready].ruleIx]
	ev := r.Status.Queue[cands[ready].queueIx]
	if now.UnixMilli()-ev.FiredAt > waitedFor.Milliseconds() {
		still, err := e.recheck(ctx, chatID, &r, &ev, now)
		if err != nil || !still {
			return now, err
		}
	}
	return now, e.send(ctx, chatID, &r, &ev, now)
}

// onEvent is a write applying f to rule ruleID and its queued event eventID; i is the event's index, -1 when it
// left the queue. A rule gone writes nothing.
func onEvent(ruleID, eventID string, f func(r *rule.Rule, i int) bool) func(c *marotte.Chat) bool {
	return func(c *marotte.Chat) bool {
		k := slices.IndexFunc(c.Bot.Rules, func(r rule.Rule) bool { return r.ID == ruleID })
		if k < 0 {
			return false
		}
		r := &c.Bot.Rules[k]
		i := slices.IndexFunc(r.Status.Queue, func(q rule.QueuedEvent) bool { return q.EventID == eventID })
		return f(r, i)
	}
}

func removeAt(st *rule.Status, i int) { st.Queue = slices.Delete(st.Queue, i, i+1) }

// recheck evaluates again the conditions of an event that waited, with what holds now, and drops it with its skip
// mark when they no longer hold. It answers whether the event is still to be sent.
func (e *Engine) recheck(ctx context.Context, chatID marotte.ChatID, r *rule.Rule, ev *rule.QueuedEvent, now time.Time) (bool, error) {
	waiting := e.deps.Waiting(chatID)
	still := false
	var done evaluation
	_, err := e.write(ctx, chatID, onEvent(r.ID, ev.EventID, func(r *rule.Rule, i int) bool {
		still = false
		if i < 0 {
			return false
		}
		q := &r.Status.Queue[i]
		s, err := snapshot(r, q, now, false, waiting)
		if err != nil {
			return false
		}
		res, err := safeEval(r.Spec.Condition, &s)
		if err == nil && res.Value == rule.True {
			still = true
			return false
		}
		done = evaluation{rule: r.ID, kind: kindOf(&r.Spec, q.Trigger), event: q.EventID, reason: res.Reason, failed: err}
		if err != nil {
			r.Status.LastFailure = mark(r, q, failedPrefix+err.Error(), now)
		} else {
			r.Status.LastSkip = mark(r, q, skippedAfterPrefix+res.Reason, now)
		}
		removeAt(&r.Status, i)
		return true
	}))
	if err == nil && !still && done.event != "" {
		e.record(chatID, []evaluation{done})
	}
	return still, err
}

// send renders ev and hands it to the Bot's chat, then marks it queued under the run entry holding its prompt; the
// turn-open tap counts it sent (settle).
func (e *Engine) send(ctx context.Context, chatID marotte.ChatID, r *rule.Rule, ev *rule.QueuedEvent, now time.Time) error {
	logArgs := eventArgs(chatID, r.ID, kindOf(&r.Spec, ev.Trigger), ev.EventID)
	text, err := rule.Render(r.Spec, ev)
	if err != nil {
		e.log.Error("rule delivery failed", append(logArgs, "error", err)...)
		obs.RuleDelivery(obs.RuleDeliveryFailed)
		_, werr := e.write(ctx, chatID, dropWith(r.ID, ev.EventID, failedPrefix+err.Error(), now))
		return werr
	}
	caller := &marotte.Caller{Kind: marotte.CallerKindRule, Label: r.Spec.Name, Rule: r.ID, Event: ev.EventID, Depth: max(ev.Depth, 1)}
	messageID, err := e.deps.Notifier.Notify(ctx, chatID, text, caller, command.NotifyOptions{DisplayText: caller.SentBy()})
	if err != nil {
		return e.refused(ctx, chatID, r, ev, err, now)
	}
	return e.writeAfterSend(ctx, chatID, onEvent(r.ID, ev.EventID, func(r *rule.Rule, i int) bool {
		if i < 0 {
			return false
		}
		q := &r.Status.Queue[i]
		q.Queued, q.MessageID = true, messageID
		r.Status.LastFailure = nil
		return true
	}))
}

// dropWith drops an event no try can deliver, with reason as the rule's failure.
func dropWith(ruleID, eventID, reason string, now time.Time) func(c *marotte.Chat) bool {
	return onEvent(ruleID, eventID, func(r *rule.Rule, i int) bool {
		if i < 0 {
			return false
		}
		r.Status.LastFailure = mark(r, &r.Status.Queue[i], reason, now)
		removeAt(&r.Status, i)
		return true
	})
}

// refused writes what a Notify refusal means for ev, by command.ClassifyNotifyError's class.
func (e *Engine) refused(ctx context.Context, chatID marotte.ChatID, r *rule.Rule, ev *rule.QueuedEvent, err error, now time.Time) error {
	logArgs := eventArgs(chatID, r.ID, kindOf(&r.Spec, ev.Trigger), ev.EventID, "error", err)
	switch command.ClassifyNotifyError(err) {
	case command.NotifyUnknownChat, command.NotifyBotClosed:
		closed := command.ClassifyNotifyError(err) == command.NotifyBotClosed
		obs.RuleDelivery(obs.RuleDeliveryDropped)
		e.log.Info("rule prompt dropped", logArgs...)
		_, werr := e.write(ctx, chatID, onEvent(r.ID, ev.EventID, func(r *rule.Rule, i int) bool {
			if i < 0 {
				return false
			}
			removeAt(&r.Status, i)
			if closed {
				r.Status.Dropped++
			}
			return true
		}))
		e.Reindex(chatID)
		return werr
	case command.NotifyBadText:
		e.log.Error("rule delivery failed", logArgs...)
		obs.RuleDelivery(obs.RuleDeliveryFailed)
		_, werr := e.write(ctx, chatID, dropWith(r.ID, ev.EventID, failedPrefix+err.Error(), now))
		return werr
	case command.NotifyRetryLater, command.NotifyFailed:
	}
	tries := ev.Tries + 1
	if tries >= maxTries {
		e.log.Warn("rule delivery failed", logArgs...)
		obs.RuleDelivery(obs.RuleDeliveryFailed)
		_, werr := e.write(ctx, chatID, dropWith(r.ID, ev.EventID, failedTriesPrefix+err.Error(), now))
		return werr
	}
	e.log.Warn("rule delivery retry", append(logArgs, "tries", tries)...)
	obs.RuleDelivery(obs.RuleDeliveryRetry)
	_, werr := e.write(ctx, chatID, onEvent(r.ID, ev.EventID, func(r *rule.Rule, i int) bool {
		if i < 0 {
			return false
		}
		r.Status.Queue[i].Tries = tries
		return true
	}))
	if werr == nil {
		e.retryAfter(chatID, eventKey(r.ID, ev.EventID), now.Add(retryDelay(tries)))
	}
	return werr
}
