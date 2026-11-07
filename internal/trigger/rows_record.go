package trigger

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// apply runs the record row of rec's state and ev, answering false for a cell no row names (A0).
func (s *stepper) apply(rec *record, ev *attemptEvent) bool {
	if rec == nil {
		return s.none(ev)
	}
	if rec.state.abandoned() {
		return s.abandoned(rec, ev)
	}
	return s.live(rec, ev)
}

// live runs the rows of a live record.
func (s *stepper) live(rec *record, ev *attemptEvent) bool {
	switch ev.kind {
	case evRunStart:
		s.confirm(rec)
	case evInvokeOK, evInvokeReplyLost, evInvokeError:
		return s.invokeReturned(rec, ev)
	case evEngineReady:
		if rec.state != stateBootHeld {
			return false
		}
		s.arm(rec) // A7
	case evBoundReached:
		if rec.state == stateBootHeld {
			s.bootBoundReached(rec)
		} else {
			s.boundReached(rec)
		}
	case evRetried:
		s.retriedLive(rec)
	case evCancelOK, evCancelError, evBusy:
		// A26 and A12: the bound decides a live record.
	default:
		return false
	}
	return true
}

// invokeReturned is A4 and A5, the return of an invoking record's call.
func (s *stepper) invokeReturned(rec *record, ev *attemptEvent) bool {
	if rec.state != stateInvoking {
		return false
	}
	if ev.kind == evInvokeError {
		s.invokeRefused(rec, ev.err)
		return true
	}
	rec.state, rec.inFlight = stateInvokedUnconfirmed, false // A4: the bound decides
	return true
}

// abandoned runs the rows of an abandoned record: it never confirms and is never abandoned again.
func (s *stepper) abandoned(rec *record, ev *attemptEvent) bool {
	switch ev.kind {
	case evCancelOK:
		if rec.state == stateAbandonedCancelling { // A13
			rec.state, rec.cancelLandedAt = stateAbandonedCancelled, s.now
			s.res.write = !rec.unpersisted
		}
		s.cancelAnswered(rec) // A13, or A22 for a second cancel's answer
	case evCancelError:
		s.cancelAnswered(rec) // A14, or A22 for a second cancel's answer
	case evInvokeOK, evInvokeError, evInvokeReplyLost:
		rec.inFlight = false // A15
		s.settle(rec)
	case evRunStart, evBoundReached:
		// A22: a late run_start or a stale timer changes nothing.
	case evRetried:
		s.handOver(rec)
	case evBusy:
		s.busyAbandoned(rec)
	default:
		return false
	}
	return true
}

// none runs the rows of an id the rule holds no record for.
func (s *stepper) none(ev *attemptEvent) bool {
	switch ev.kind {
	case evNamed:
		s.named(ev)
		return true
	case evRunStart, evInvokeOK, evInvokeError, evInvokeReplyLost, evBoundReached, evCancelOK, evCancelError,
		evRetried:
		return true // A23
	case evBusy:
		if ev.launchingRule != "" {
			return ev.launchingRule != s.ruleID // A23: the record left between the lookup and this call
		}
		s.busyUnheld(ev)
		return true
	}
	return false
}

// named is A1 for a key queued with no live record, else A2.
func (s *stepper) named(ev *attemptEvent) {
	es := &s.rv.es
	i := slices.IndexFunc(es.queue, func(e queueEntry) bool { return e.Key == ev.key })
	if i < 0 || slices.ContainsFunc(s.rv.records, func(r *record) bool { return r.key == ev.key && r.state.live() }) {
		rec := &record{workflowID: ev.workflowID, key: ev.key, unpersisted: !s.held || s.rv.unreadable != ""}
		s.rv.records = append(s.rv.records, rec)
		s.abandon(rec, causeNameFailed)
		s.res.err = errAttemptGone
		return
	}
	e := &es.queue[i]
	rec := &record{
		workflowID: ev.workflowID, key: e.Key, coalesceKey: e.CoalesceKey, inputs: maps.Clone(e.Inputs),
		state: stateInvoking, at: s.now, inFlight: true,
	}
	s.rv.records = append(s.rv.records, rec)
	e.Live = rec.workflowID
	s.arm(rec)
	s.nameRec = rec
	s.status().RowText = "starting run " + rec.workflowID
	s.res.write = true
}

// confirm is A3: the run's run_start confirms its event, a success that resets the rule's failure counts.
func (s *stepper) confirm(rec *record) {
	s.drop(rec)
	es := &s.rv.es
	launched := launchedEntry{Key: rec.key, CoalesceKey: rec.coalesceKey, Inputs: rec.inputs, WorkflowID: rec.workflowID}
	if i := slices.IndexFunc(es.queue, func(e queueEntry) bool { return e.Key == rec.key }); i >= 0 {
		launched.CoalesceKey, launched.Inputs = es.queue[i].CoalesceKey, es.queue[i].Inputs
		es.queue = slices.Delete(es.queue, i, i+1)
	}
	es.launched = append(es.launched, launched)
	if n := len(es.launched) - maxLaunched; n > 0 {
		es.launched = slices.Delete(es.launched, 0, n)
	}
	for i := range es.superseded {
		if es.superseded[i].By == rec.key {
			es.superseded[i].WorkflowID = rec.workflowID
		}
	}
	st := s.status()
	st.LastRunID, st.LastStatus, st.LastReason, st.LastRunAt = rec.workflowID, launchrule.StatusStarted, "", s.now
	st.BoundAbandons, st.FailingSince, st.RowText = 0, time.Time{}, ""
	s.rv.retries, s.rv.retryAt = 0, time.Time{}
	slog.Info(logMsgRunStarted, "rule_id", s.ruleID, "workflow_id", rec.workflowID, "key", rec.key)
	s.res.write = true
}

// invokeRefused is A5: an error proving KAS did not take the invoke abandons the attempt at once, and the error
// table's class decides what else it costs the rule, as for E9.
func (s *stepper) invokeRefused(rec *record, err error) {
	rec.inFlight = false
	cause := causeInvokeError
	if errors.Is(err, ErrLeaseNotGranted) {
		cause = causeLeaseNotGranted
	}
	s.abandon(rec, cause)
	s.requeue(rec.key)
	s.backOff()
	text := reasonText(err)
	switch classify(err) {
	case classBlocked:
		s.status().RowText = fmt.Sprintf("the launch of run %s failed: %s", rec.workflowID, text)
		s.block(rec.workflowID, text)
	case classCounted:
		s.status().RowText = retryingText(rec.workflowID, text, s.rv.retryAt)
		s.failedAttempt(rec.workflowID, text)
	case classRetryable:
		s.status().RowText = retryingText(rec.workflowID, text, s.rv.retryAt)
	}
	if s.status().Blocked == "" {
		warnRetryable(s.ruleID, rec.workflowID, text)
	}
	s.wakeLauncher()
}

// retriedLive is A35: the retried run re-runs the event under its lease, so it gets a full bound from the retry. A
// boot-held record A7 has not armed yet, the one live record with no bound, keeps waiting for A7.
func (s *stepper) retriedLive(rec *record) {
	if rec.bound != nil {
		rec.bound.Stop()
		s.arm(rec)
	}
	s.res.retry.Event = &RetriedEvent{
		RuleID: s.ruleID, EventKey: rec.key, CoalesceKey: rec.coalesceKey, Label: EventLabel(rec.inputs),
	}
}

// handOver is A33: the person's retry takes the abandoned run, committed only once the record's removal is written
// (commit restores it on a failed write).
func (s *stepper) handOver(rec *record) {
	s.rv.records = slices.DeleteFunc(s.rv.records, func(r *record) bool { return r == rec })
	s.res.retry.HandedBack = true
	if !rec.unpersisted {
		s.handBack = rec
		s.res.write = true
	}
}

// boundReached is A6: no run_start within the bound abandons the attempt, and two such abandons block the rule.
func (s *stepper) boundReached(rec *record) {
	rec.owesRelease = rec.inFlight
	s.abandon(rec, causeBound)
	s.requeue(rec.key)
	s.backOff()
	st := s.status()
	st.BoundAbandons++
	if st.BoundAbandons >= maxBoundAbandons {
		st.RowText = twiceUnreported(rec.workflowID)
		s.block(rec.workflowID, st.RowText)
	} else {
		st.RowText = notConfirmedText(rec.workflowID)
	}
	s.failedAttempt(rec.workflowID, st.RowText)
	s.wakeLauncher()
}

// maxBoundAbandons is the counted bound abandons that block a rule: a run_start that never reaches marotte then
// costs at most two cancelled runs, whatever the events.
const maxBoundAbandons = 2

func retryingText(workflowID, text string, retryAt time.Time) string {
	return fmt.Sprintf("the launch of run %s failed: %s; retrying at %s", workflowID, text,
		retryAt.Local().Format("15:04"))
}

func notConfirmedText(workflowID string) string {
	return fmt.Sprintf("run %s was not confirmed within 5 minutes; cancelled it and launching the event again",
		workflowID)
}

func twiceUnreported(workflowID string) string {
	return fmt.Sprintf("run %s did not report its start twice; marotte stopped retrying this event. "+
		"Check the engine, then resume the rule", workflowID)
}

// abandon moves rec to abandoned-cancelling and queues its cancel. A live record stops being its entry's attempt.
func (s *stepper) abandon(rec *record, cause string) {
	if rec.state.live() {
		s.leaveLive(rec)
	}
	rec.state, rec.cause, rec.at = stateAbandonedCancelling, cause, s.now
	slog.Info(logMsgLaunchAbandoned, "workflow_id", rec.workflowID, "rule_id", s.ruleID, "key", rec.key, "cause", cause)
	s.cancel(rec)
	if !rec.unpersisted {
		s.res.write = true
	}
}

// cancel queues a cancel of rec's run for the effect goroutine.
func (s *stepper) cancel(rec *record) {
	rec.cancelsOut++
	s.res.effects = append(s.res.effects, attemptEffect{kind: effectCancel, workflowID: rec.workflowID})
}

// drop removes a confirmed record: a confirmed attempt has no record.
func (s *stepper) drop(rec *record) {
	s.leaveLive(rec)
	s.rv.records = slices.DeleteFunc(s.rv.records, func(r *record) bool { return r == rec })
}

// leaveLive ends rec's term as its entry's attempt: the bound stops, the id leaves the boot-held set and the
// entry names no live record.
func (s *stepper) leaveLive(rec *record) {
	if rec.bound != nil {
		rec.bound.Stop()
		rec.bound = nil
	}
	s.c.releaseBootHeld(rec.workflowID)
	es := &s.rv.es
	for i := range es.queue {
		if es.queue[i].Live == rec.workflowID {
			es.queue[i].Live = ""
		}
	}
}

func (s *stepper) status() *launchrule.Status { return &s.rv.es.raw.Status }

func (s *stepper) wakeLauncher() {
	s.res.effects = append(s.res.effects, attemptEffect{kind: effectWakeLauncher})
}

// maxLaunched is how many confirmed keys a rule keeps, newest last.
const maxLaunched = 200
