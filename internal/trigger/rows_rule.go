package trigger

import (
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// The causes of an abandon a rule event makes.
const (
	causeEdit   = "edit"
	causeDelete = "delete"
)

// abandonedTTL is how long a record whose cancel landed outlives it: twice the 24-hour clock, so a cancelled run
// that still blocks its recipe has had the rule blocked and paged before its record goes.
const abandonedTTL = 48 * time.Hour

// ruleRow runs the rule row of ev over every record the rule holds, then its own changes.
func (s *stepper) ruleRow(ev *attemptEvent) bool {
	switch ev.kind {
	case evEditFilter:
		s.editFilter()
	case evEditRebaseline:
		s.rebaseline()
	case evDelete:
		s.deleteRule()
	case evResume:
		s.resetFailures() // E4, A25 per record
		s.res.write = true
		s.wakeLauncher()
	case evPause:
		// E8, A25 per record: the count, the block and the clock are kept.
	case evRunGone:
		s.dropAbandoned(func(r *record) bool { return !r.waitedOn() && !listedActive(ev.statuses, r.workflowID) }) // E5
		s.clearGoneBlocker(ev.statuses)
	case evTTL:
		s.dropAbandoned(s.aged) // E6
	case evWriteRetry:
		s.res.write, s.wakeOnWrite = true, true // E10
	case evLaunchEnded:
		s.launchEnded(ev.key, ev.err)
	case evBoot:
		s.boot(ev.boot)
	default:
		return false
	}
	return true
}

// editFilter is E1: live records keep their bound (A11), abandoned ones stay (A20). A filter.label change resets
// the labeled cursor, so the next poll takes a baseline under the new label instead of reading every item that
// already carries it as a gain.
func (s *stepper) editFilter() {
	s.rv.generation++
	if label := labelOf(&s.def); label != s.rv.label {
		for _, rc := range s.rv.es.cursor {
			if rc != nil {
				rc.Labeled = nil
			}
		}
		s.rv.label = label
	}
	s.resetFailures()
	if newest := isNewest(&s.def); newest && !s.rv.newest {
		for _, e := range slices.Clone(s.rv.es.queue) {
			if e.Live == "" {
				s.requeue(e.Key)
			}
		}
	}
	s.rv.newest = isNewest(&s.def)
	s.res.write = true
	s.wakeLauncher()
}

// rebaseline is E2: the rule watches a new population, so its live attempts are abandoned (A9), its abandoned ones
// kept (A20), and its cursor and queues start over.
func (s *stepper) rebaseline() {
	for _, r := range slices.Clone(s.rv.records) {
		if r.state.live() {
			r.owesRelease = r.inFlight
			s.abandon(r, causeEdit)
		}
	}
	s.rv.generation++
	es := &s.rv.es
	es.cursor, es.queue, es.launched, es.superseded = cursor{}, nil, nil, nil
	es.checkedAt, es.baselineStop, es.pollFailures, es.queueFull, es.fullLogged = time.Time{}, "", 0, false, false
	s.rv.unreadable = nil
	s.resetFailures()
	es.raw.Population = launchrule.Population(&s.def)
	s.rv.newest, s.rv.label = isNewest(&s.def), labelOf(&s.def)
	s.res.write = true
}

// deleteRule is E3: every record is cancelled and leaves once nothing waits on it (A10, A21), and the state file
// goes.
func (s *stepper) deleteRule() {
	for _, r := range slices.Clone(s.rv.records) {
		switch {
		case r.state.live():
			r.owesRelease, r.unpersisted = r.inFlight, true
			s.abandon(r, causeDelete)
		case r.state == stateAbandonedCancelling:
			r.unpersisted = true
			s.cancel(r)
		case r.inFlight || r.owesRelease || r.cancelsOut > 0:
			r.unpersisted = true
		default:
			s.removeRecord(r)
		}
	}
	s.clearWriteBlock()
	if err := s.c.writer.RemoveState(s.ruleID); err != nil {
		logNotWritten(s.ruleID, opRemove, err)
	}
}

// resetFailures sets the rule's failure counts to zero and lifts its block, with the row text the block wrote,
// unless the block is an unreadable state file's.
func (s *stepper) resetFailures() {
	st := s.status()
	st.BoundAbandons, st.FailingSince = 0, time.Time{}
	s.rv.retries, s.rv.retryAt, s.rv.retryReason = 0, time.Time{}, ""
	if st.Blocked != "" && s.rv.unreadable == nil {
		st.Blocked, st.RowText = "", ""
	}
}

// aged is A19: nothing waits on the record and its cancel landed abandonedTTL ago. A record whose cancel never
// landed is never dropped by age, since it is what names a live run nothing else does; only a run list showing
// that run gone drops it (A18).
func (s *stepper) aged(r *record) bool {
	return r.state == stateAbandonedCancelled && !r.waitedOn() && s.now.Sub(r.cancelLandedAt) > abandonedTTL
}

// waitedOn reports a call or a lease release still owed on the record, which keeps it on E5 and E6 alike (A37).
func (r *record) waitedOn() bool { return r.inFlight || r.owesRelease }

// dropAbandoned drops each abandoned record gone answers true for (A18, A19) and keeps the rest (A26, A37), writing
// only when it dropped one.
func (s *stepper) dropAbandoned(gone func(*record) bool) {
	for _, r := range slices.Clone(s.rv.records) {
		if r.state.abandoned() && gone(r) {
			s.removeRecord(r)
			s.res.write = true
		}
	}
}

func (s *stepper) removeRecord(rec *record) {
	s.rv.records = slices.DeleteFunc(s.rv.records, func(r *record) bool { return r == rec })
}

// listedActive reports a run the list shows still live; a run the list does not hold is gone.
func listedActive(statuses map[string]marotte.RunStatus, workflowID string) bool {
	st, ok := statuses[workflowID]
	return ok && st.Active()
}

func labelOf(r *launchrule.Rule) string {
	if ev := r.When.Event; ev != nil {
		return ev.Filter.Label
	}
	return ""
}

func isNewest(r *launchrule.Rule) bool {
	ev := r.When.Event
	return ev != nil && ev.Hold != nil && ev.Hold.Policy == launchrule.HoldNewest
}
