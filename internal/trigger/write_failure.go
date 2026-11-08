package trigger

import (
	"fmt"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// commit writes the rule's whole value when the row asked for a write, launch-rules.json holds the rule and its
// state file is not unreadable. A failed write is W1 for A1's record, else W2; a landed one lifts the write-failure
// block and wakes the launcher.
func (s *stepper) commit() {
	if !s.res.write || !s.held || s.rv.unreadable != nil {
		return
	}
	if err := s.c.writeLocked(s.ruleID, s.rv); err != nil {
		logNotWritten(s.ruleID, opWrite, err)
		s.writeFailed(err)
		return
	}
	if s.rv.writeBlocked != "" {
		s.clearWriteBlock()
		s.wakeOnWrite = true
	}
	if s.wakeOnWrite {
		s.wakeLauncher()
	}
}

// writeFailed blocks the rule until a write of its whole value lands, retried at each poll interval. The row's
// next state stays in memory (W2), except A1's record, which W1 abandons instead: a run its rule never recorded
// must not be invoked, since after a restart nothing would name it; and A33's, which stays the rule's, since a
// restart would load it from the file and cancel the person's retry.
func (s *stepper) writeFailed(err error) {
	if rec := s.handBack; rec != nil {
		s.rv.records = append(s.rv.records, rec)
		s.res.retry.HandedBack, s.res.err = false, fmt.Errorf("%w: %w", errAttemptNotWritten, err)
	}
	if rec := s.nameRec; rec != nil {
		rec.inFlight = false
		s.abandon(rec, causeNameFailed)
		s.requeue(rec.key)
		s.backOff()
		s.status().RowText = ""
		s.res.err = fmt.Errorf("%w: %w", errAttemptNotWritten, err)
	}
	s.rv.writeBlocked = fmt.Sprintf("could not write %s: %v", stateFile(s.ruleID), err)
	if s.rv.writeRetry == nil {
		c, ruleID := s.c, s.ruleID
		s.rv.writeRetry = time.AfterFunc(pollInterval(&s.def), func() { c.writeTick(ruleID) })
	}
}

// clearWriteBlock lifts the write-failure block and stops its tick; rv.mu is held.
func (s *stepper) clearWriteBlock() {
	s.rv.writeBlocked = ""
	if s.rv.writeRetry != nil {
		s.rv.writeRetry.Stop()
		s.rv.writeRetry = nil
	}
}

// writeTick is the write-failure tick: E10 for a rule still write-blocked. A failed retry arms the next tick.
func (c *Confirmer) writeTick(ruleID string) {
	c.mu.Lock()
	rv := c.rules[ruleID]
	c.mu.Unlock()
	if rv == nil {
		return
	}
	rv.mu.Lock()
	rv.writeRetry = nil
	blocked := rv.writeBlocked != "" && !rv.dropped
	rv.mu.Unlock()
	if blocked {
		c.deliver(ruleID, &attemptEvent{kind: evWriteRetry})
	}
}

// pollInterval is the rule's poll interval, the write-failure tick's period.
func pollInterval(r *launchrule.Rule) time.Duration {
	if ev := r.When.Event; ev != nil && ev.IntervalMin > 0 {
		return time.Duration(ev.IntervalMin) * time.Minute
	}
	return 5 * time.Minute
}

func stateFile(ruleID string) string { return "launch-state/" + ruleID + ".json" }
