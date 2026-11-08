package trigger

import (
	"time"
)

// launchConfirmBound is how long a named attempt waits for its run_start before it is abandoned. It covers the
// invoke's 120 s launch timeout with three minutes to spare.
var launchConfirmBound = 5 * time.Minute

// failingBlockAfter is how long a rule may keep failing its launches before it is blocked.
const failingBlockAfter = 24 * time.Hour

// arm starts rec's bound: when it passes, the timer delivers bound reached through step like any other event.
func (s *stepper) arm(rec *record) {
	c, ruleID, wf := s.c, s.ruleID, rec.workflowID
	rec.bound = time.AfterFunc(launchConfirmBound, func() {
		c.deliver(ruleID, &attemptEvent{kind: evBoundReached, workflowID: wf})
	})
}

// failedAttempt counts a failed attempt on the rule's 24-hour clock, which a restart does not restart: the clock
// starts at the first failure, and once it has run 24 hours the rule is blocked with the last error. It writes
// only when the clock starts or the block changes.
func (s *stepper) failedAttempt(workflowID, lastErr string) {
	st := s.status()
	if st.FailingSince.IsZero() {
		s.startFailing(lastErr)
		st.FailingSince = s.now
		s.res.write = true
	}
	if s.now.Sub(st.FailingSince) >= failingBlockAfter {
		s.block(workflowID, lastErr)
	}
}

// block stops the rule's launches with sentence until the operator edits or resumes it, warning once as it starts.
// workflowID is the run the sentence is about, empty for a launch that created none.
func (s *stepper) block(workflowID, sentence string) {
	st := s.status()
	if st.Blocked == sentence {
		return
	}
	if st.Blocked == "" {
		logRunNotStarted(s.ruleID, workflowID, notStartedBlocked, sentence)
		s.startFailing(sentence)
	}
	st.Blocked = sentence
	s.res.write = true
}

// startFailing sends a failing streak's one notice, as the rule goes from launching to failing: the streak lasts
// until resetFailures, which a confirmed launch, an edit or a resume runs, so a rule that keeps failing notifies
// once. It runs before the caller sets the clock or the block.
func (s *stepper) startFailing(reason string) {
	if st := s.status(); st.FailingSince.IsZero() && st.Blocked == "" {
		s.res.effects = append(s.res.effects, attemptEffect{kind: effectNotify, reason: reason})
	}
}
