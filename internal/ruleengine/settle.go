package ruleengine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/rule"
)

// logTail is how many of the Bot's newest log entries the settle reads for a prompt a rule's event sent.
const logTail = 200

// verdict is what the settle found of a fired event's prompt.
type verdict uint8

const (
	// keepEvent leaves the event as it is: its prompt waits on the run list, or never reached the chat.
	keepEvent verdict = iota
	// deliveredEvent is a prompt whose turn opened: the event leaves the queue and counts as sent.
	deliveredEvent
	// droppedEvent is a queued prompt the user discarded before its turn.
	droppedEvent
	// resendEvent is a prompt the last process left held on the run list, taken back to be sent again.
	resendEvent
	// relaunchEvent is a queued prompt whose turn failed to open: its paused row is taken back and the event waits
	// out its retry wait, as a refused send does.
	relaunchEvent
	// launchFailedEvent is a queued prompt whose turn failed to open on its last try: its row is taken back and the
	// event dropped with the rule's failure.
	launchFailedEvent
)

// errLaunchFailed is the failure a queued prompt whose turn failed to open records.
var errLaunchFailed = errors.New("the Bot's reply did not start")

// settle settles what chatID's fired events handed to the chat: a queued prompt whose turn opened counts as sent,
// one the user discarded is dropped. A Bot's first settle in a process checks every fired event against the log
// tail and the run list, since the last process may have died between any two writes. A queued prompt whose turn
// failed to open goes back on the retry ladder. It answers the record after.
func (e *Engine) settle(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat, now time.Time) (*marotte.Chat, error) {
	taps := e.takeOpened(chatID)
	plan := classify(c, taps, !e.settled[chatID])
	if err := e.resolve(ctx, chatID, c, &plan); err != nil {
		e.restoreOpened(chatID, taps)
		return nil, err
	}
	if len(plan.verdicts) == 0 {
		e.settled[chatID] = true
		return c, nil
	}
	after, err := e.write(ctx, chatID, applyVerdicts(plan.verdicts, now))
	if err != nil {
		e.restoreOpened(chatID, taps)
		return nil, err
	}
	for k, v := range plan.verdicts {
		args := []any{keyChatID, chatID, keyRuleID, k.rule, keyEventID, k.event}
		switch v {
		case deliveredEvent:
			obs.RuleDelivery(obs.RuleDeliveryDelivered)
			e.log.Info("rule prompt delivered", args...)
		case droppedEvent:
			obs.RuleDelivery(obs.RuleDeliveryDropped)
			e.log.Info("rule prompt dropped", args...)
		case relaunchEvent:
			tries := plan.tries[k]
			obs.RuleDelivery(obs.RuleDeliveryRetry)
			e.log.Warn("rule delivery retry", append(args, "error", errLaunchFailed, "tries", tries)...)
			e.retryAfter(chatID, eventKey(k.rule, k.event), now.Add(retryDelay(tries)))
		case launchFailedEvent:
			obs.RuleDelivery(obs.RuleDeliveryFailed)
			e.log.Warn("rule delivery failed", append(args, "error", errLaunchFailed)...)
		case keepEvent, resendEvent:
		}
	}
	e.settled[chatID] = true
	return after, nil
}

// settlePlan is what the settle decided from the record alone, and what it must still read or do.
type settlePlan struct {
	verdicts map[openedPrompt]verdict
	// resend are the rows the settle takes back, by their row id: the boot's held rows and the failed launches.
	resend map[openedPrompt]string
	// tries is each relaunched or failed event's try count after this failed launch.
	tries map[openedPrompt]int
	// unsure are the events neither the taps nor the run list place: the log tail decides.
	unsure []openedPrompt
}

// classify places each fired event the settle looks at: every one on boot, else the queued and the tapped.
func classify(c *marotte.Chat, taps []openedPrompt, boot bool) settlePlan {
	tapped := make(map[openedPrompt]bool, len(taps))
	for _, t := range taps {
		tapped[t] = true
	}
	plan := settlePlan{verdicts: map[openedPrompt]verdict{}, resend: map[openedPrompt]string{}, tries: map[openedPrompt]int{}}
	for i := range c.Bot.Rules {
		r := &c.Bot.Rules[i]
		for j := range r.Status.Queue {
			q := &r.Status.Queue[j]
			k := openedPrompt{rule: r.ID, event: q.EventID}
			if q.Stage == rule.StageFired && (boot || q.Queued || tapped[k]) {
				plan.place(k, q, rowFor(c.Run, k), tapped[k], boot)
			}
		}
	}
	return plan
}

// place decides fired event q by what the taps and its row on the run list, nil for none, say of its prompt.
func (plan *settlePlan) place(k openedPrompt, q *rule.QueuedEvent, row *marotte.RunEntry, tapped, boot bool) {
	switch {
	case tapped, row != nil && row.Turn != "":
		plan.verdicts[k] = deliveredEvent
	case row != nil && boot:
		plan.resend[k] = row.ID
	case row != nil && row.FailedLaunch && row.State == marotte.RunPaused:
		plan.resend[k], plan.tries[k], plan.verdicts[k] = row.ID, q.Tries+1, relaunchEvent
		if plan.tries[k] >= maxTries {
			plan.verdicts[k] = launchFailedEvent
		}
	case row == nil:
		plan.unsure = append(plan.unsure, k)
	}
}

// resolve reads the log tail for the unsure events and takes the held rows back off the run list.
func (e *Engine) resolve(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat, plan *settlePlan) error {
	if len(plan.unsure) > 0 {
		sent, err := e.loggedPrompts(ctx, chatID)
		if err != nil {
			return err
		}
		for _, k := range plan.unsure {
			switch {
			case sent[k]:
				plan.verdicts[k] = deliveredEvent
			case queuedIn(c, k):
				plan.verdicts[k] = droppedEvent
			}
		}
	}
	for k, rowID := range plan.resend {
		if err := e.deps.Notifier.Unqueue(ctx, chatID, rowID); err != nil {
			e.log.Warn("rule engine could not take back a held prompt", keyChatID, chatID, keyRuleID, k.rule,
				keyEventID, k.event, "error", err)
			return err
		}
		if _, failed := plan.verdicts[k]; !failed {
			plan.verdicts[k] = resendEvent
		}
	}
	return nil
}

func (e *Engine) restoreOpened(chatID marotte.ChatID, taps []openedPrompt) {
	if len(taps) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opened[chatID] = append(taps, e.opened[chatID]...)
}

// applyVerdicts is the settle's one write.
func applyVerdicts(verdicts map[openedPrompt]verdict, now time.Time) func(c *marotte.Chat) bool {
	return func(c *marotte.Chat) bool {
		at := time.Now()
		changed := false
		for i := range c.Bot.Rules {
			r := &c.Bot.Rules[i]
			kept := r.Status.Queue[:0:0]
			for j := range r.Status.Queue {
				q := r.Status.Queue[j]
				v, ok := verdicts[openedPrompt{rule: r.ID, event: q.EventID}]
				if !ok || q.Stage != rule.StageFired {
					kept = append(kept, q)
					continue
				}
				switch v {
				case deliveredEvent:
					rule.RecordSent(&r.Status, q.Trigger, at)
					changed = true
					continue
				case droppedEvent:
					r.Status.Dropped++
					changed = true
					continue
				case resendEvent:
					changed = changed || q.Queued || q.MessageID != ""
					q.Queued, q.MessageID = false, ""
				case relaunchEvent:
					q.Queued, q.MessageID = false, ""
					q.Tries++
					changed = true
				case launchFailedEvent:
					r.Status.LastFailure = mark(r, &q, failedTriesPrefix+errLaunchFailed.Error(), now)
					changed = true
					continue
				case keepEvent:
				}
				kept = append(kept, q)
			}
			r.Status.Queue = kept
		}
		return changed
	}
}

func queuedIn(c *marotte.Chat, k openedPrompt) bool {
	for i := range c.Bot.Rules {
		if c.Bot.Rules[i].ID != k.rule {
			continue
		}
		for j := range c.Bot.Rules[i].Status.Queue {
			if q := &c.Bot.Rules[i].Status.Queue[j]; q.EventID == k.event {
				return q.Queued
			}
		}
	}
	return false
}

// rowFor is the run list's row whose caller is k's rule and event, nil when none.
func rowFor(run []marotte.RunEntry, k openedPrompt) *marotte.RunEntry {
	for i := range run {
		if row := run[i].Row; row != nil && matches(row.Caller, k) {
			return &run[i]
		}
	}
	return nil
}

func matches(c *marotte.Caller, k openedPrompt) bool {
	return c != nil && c.Kind == marotte.CallerKindRule && c.Rule == k.rule && c.Event == k.event
}

// loggedPrompts answers the rule events whose prompt opened a turn or was steered, among the log's newest entries.
func (e *Engine) loggedPrompts(ctx context.Context, chatID marotte.ChatID) (map[openedPrompt]bool, error) {
	entries, err := e.deps.Store.All(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := map[openedPrompt]bool{}
	for _, en := range entries[max(0, len(entries)-logTail):] {
		var caller *marotte.Caller
		switch en.Kind {
		case marotte.EntryKindTurnOpen:
			var p marotte.EntryTurnOpen
			if json.Unmarshal(en.Payload, &p) == nil && p.Prompt != nil {
				caller = p.Prompt.Caller
			}
		case marotte.EntryKindSteer:
			var p marotte.EntrySteer
			if json.Unmarshal(en.Payload, &p) == nil {
				caller = p.Caller
			}
		default:
		}
		if caller != nil && caller.Kind == marotte.CallerKindRule {
			out[openedPrompt{rule: caller.Rule, event: caller.Event}] = true
		}
	}
	return out, nil
}

// nextSettle is when c's queued prompts are looked at again, for a row that left the run list unseen.
func nextSettle(c *marotte.Chat, now time.Time) time.Time {
	for i := range c.Bot.Rules {
		for j := range c.Bot.Rules[i].Status.Queue {
			if c.Bot.Rules[i].Status.Queue[j].Queued {
				return now.Add(tick)
			}
		}
	}
	return time.Time{}
}
