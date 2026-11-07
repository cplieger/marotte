package trigger

import "slices"

// cancelAnswered takes one of rec's cancels off its count, then settles it.
func (s *stepper) cancelAnswered(rec *record) {
	rec.cancelsOut--
	s.settle(rec)
}

// settle runs R and then A32 once rec is neither in flight nor waiting on a cancel's answer.
//
// R: a record abandoned while its launch was in flight may have had its cancel land before the lease was written,
// so that cancel's terminal frame released nothing; once the cancel is known to have landed, the release is owed
// here. A cancel that did not land leaves the lease as the run's floor.
// A32: a deleted rule's record has nothing left to wait for and goes.
func (s *stepper) settle(rec *record) {
	if rec.inFlight || rec.cancelsOut > 0 {
		return
	}
	if rec.owesRelease {
		if rec.state == stateAbandonedCancelled {
			s.res.effects = append(s.res.effects, attemptEffect{kind: effectRelease, workflowID: rec.workflowID})
		}
		rec.owesRelease = false
	}
	if rec.unpersisted {
		s.rv.records = slices.DeleteFunc(s.rv.records, func(r *record) bool { return r == rec })
	}
}
