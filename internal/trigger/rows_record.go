package trigger

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
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
	switch {
	case ev.kind == evRunStart:
		s.confirm(rec)
	case rec.state == stateInvoking && (ev.kind == evInvokeOK || ev.kind == evInvokeReplyLost):
		rec.state, rec.inFlight = stateInvokedUnconfirmed, false // A4: the bound decides
	case rec.state == stateInvoking && ev.kind == evInvokeError:
		s.invokeRefused(rec, ev.err)
	case rec.state == stateBootHeld && ev.kind == evEngineReady:
		s.arm(rec) // A7
	case rec.state == stateBootHeld && ev.kind == evBoundReached:
		s.bootBoundReached(rec)
	case ev.kind == evBoundReached:
		s.boundReached(rec)
	case ev.kind == evCancelOK || ev.kind == evCancelError:
		// A26: the bound decides a live record.
	default:
		return false
	}
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
	case evRunStart, evInvokeOK, evInvokeError, evInvokeReplyLost, evBoundReached, evCancelOK, evCancelError:
		return true // A23
	case evBusy:
		return ev.launchingRule != "" && ev.launchingRule != s.ruleID // A23; the launching rule's own busy is A24
	}
	return false
}

// named is A1 for a key queued with no live record, else A2.
func (s *stepper) named(ev *attemptEvent) {
	es := &s.rv.es
	i := slices.IndexFunc(es.queue, func(e queueEntry) bool { return e.Key == ev.key })
	if i < 0 || slices.ContainsFunc(s.rv.records, func(r *record) bool { return r.key == ev.key && r.state.live() }) {
		rec := &record{workflowID: ev.workflowID, key: ev.key, unpersisted: !s.held}
		s.rv.records = append(s.rv.records, rec)
		s.abandon(rec, causeNameFailed)
		s.res.err = ErrAttemptGone
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
	slog.Info(logMsgRunStarted, "rule_id", s.ruleID, "workflow_id", rec.workflowID, "key", rec.key)
	s.res.write = true
}

// invokeRefused is A5: an error proving KAS did not take the invoke abandons the attempt at once.
func (s *stepper) invokeRefused(rec *record, err error) {
	rec.inFlight = false
	cause := causeInvokeError
	if errors.Is(err, ErrLeaseNotGranted) {
		cause = causeLeaseNotGranted
	}
	s.abandon(rec, cause)
	s.requeue(rec.key)
	text := launchrule.ReasonText(rpcerr.Text(err))
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok && rpc.Code == codeInvalidParams {
		s.status().RowText = fmt.Sprintf("the launch of run %s failed: %s", rec.workflowID, text)
		s.block(rec, text)
	} else {
		s.status().RowText = fmt.Sprintf("the launch of run %s failed: %s; retrying", rec.workflowID, text)
	}
	s.failedAttempt(rec, text)
	s.wakeLauncher()
}

// boundReached is A6: no run_start within the bound abandons the attempt, and two such abandons block the rule.
func (s *stepper) boundReached(rec *record) {
	rec.owesRelease = rec.inFlight
	s.abandon(rec, causeBound)
	s.requeue(rec.key)
	st := s.status()
	st.BoundAbandons++
	if st.BoundAbandons >= maxBoundAbandons {
		st.RowText = twiceUnreported(rec.workflowID)
		s.block(rec, st.RowText)
	} else {
		st.RowText = notConfirmedText(rec.workflowID)
	}
	s.failedAttempt(rec, st.RowText)
	s.wakeLauncher()
}

// maxBoundAbandons is the counted bound abandons that block a rule: a run_start that never reaches marotte then
// costs at most two cancelled runs, whatever the events.
const maxBoundAbandons = 2

// codeInvalidParams is JSON-RPC's invalid params, which KAS answers with its own sentence.
const codeInvalidParams = -32602

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
	rec.cancelsOut++
	s.res.effects = append(s.res.effects, attemptEffect{kind: effectCancel, workflowID: rec.workflowID})
	if !rec.unpersisted {
		s.res.write = true
	}
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
