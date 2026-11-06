package launchrule

import (
	"context"
	"time"

	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/sanitize"
)

// OutcomeStatus is how a clock rule's last slot ended, as far as the server knows.
type OutcomeStatus string

const (
	// StatusStarted means the launch took and nothing has reported an ending yet.
	StatusStarted OutcomeStatus = "started"
	// StatusCompleted means the run finished on its own terms.
	StatusCompleted OutcomeStatus = "completed"
	// StatusFailed means the launch or the run failed; the reason says why.
	StatusFailed OutcomeStatus = "failed"
	// StatusUnknown means the run went away without a terminal signal.
	StatusUnknown OutcomeStatus = "unknown"
	// StatusSkipped means the slot never started and no failure caused it; the reason says why.
	StatusSkipped OutcomeStatus = "skipped"
)

// MaxReasonBytes bounds a reason quoted on a row, one line persisted at every fire. KAS's pause and stop sentences run
// under 200 bytes; a step's own failure text or a launch error can be any length.
const MaxReasonBytes = 512

// ReasonText defuses text marotte did not write for a row's reason: one line, no escapes, capped at MaxReasonBytes.
func ReasonText(s string) string {
	text, _ := textsafe.SanitizeSingleLineCapped(sanitize.Output(s), MaxReasonBytes, "...")
	return text
}

// Outcome is one slot's ending. Reason is plain text a person reads, never a prefix-coded value: Status alone
// carries the classification.
type Outcome struct {
	Status OutcomeStatus
	Reason string
}

// Row addresses one clock rule's row. A minted id never names a second rule, so a writer holding the Row of a
// deleted rule changes nothing.
type Row struct {
	ID string
}

// Row is the row r's runs report to.
func (r *Rule) Row() Row { return Row{ID: r.ID} }

// Run names one launch of a clock rule: the rule and the workflow run its fire started.
type Run struct {
	// StartedAt is when the run was created, which RecordStart writes as LastRunAt; zero leaves LastRunAt as it was.
	StartedAt  time.Time
	ScheduleID string
	// RuleID is an event rule's run, whose outcome the trigger confirmer writes; the store's row writers take
	// ScheduleID only.
	RuleID     string
	WorkflowID string
}

// Clock is a clock rule with its row. Err is what State answers for a blocked rule (refused at load, or its state
// file would not load), and Status is then zero: such a rule must not fire.
type Clock struct {
	Err error
	Rule
	Status
}

// Clocks returns every clock rule with its row, in creation order.
func (s *Store) Clocks() []Clock {
	s.mu.Lock()
	rules := make([]Rule, 0, len(s.rules))
	states := make([]*ruleState, 0, len(s.rules))
	for i := range s.rules {
		if s.rules[i].When.Clock != nil {
			rules = append(rules, s.rules[i].clone())
			states = append(states, s.states[s.rules[i].ID])
		}
	}
	s.mu.Unlock()
	out := make([]Clock, len(rules))
	for i := range rules {
		out[i] = clockOf(&rules[i], states[i])
	}
	return out
}

// Clock returns the clock rule id names with its row; false when no clock rule has that id.
func (s *Store) Clock(id string) (Clock, bool) {
	s.mu.Lock()
	i := s.indexLocked(id)
	if i < 0 || s.rules[i].When.Clock == nil {
		s.mu.Unlock()
		return Clock{}, false
	}
	r, rs := s.rules[i].clone(), s.states[id]
	s.mu.Unlock()
	return clockOf(&r, rs), true
}

func clockOf(r *Rule, rs *ruleState) Clock {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if err := rs.blocked(); err != nil {
		return Clock{Rule: *r, Err: err}
	}
	return Clock{Rule: *r, Status: rs.state.Status}
}

// UpdateClock applies set to the clock rule row names and writes that rule's state file when set reports a
// change. ErrNotFound when no clock rule has the id, what State answers when the rule is blocked.
func (s *Store) UpdateClock(ctx context.Context, row Row, set func(st *Status) bool) error {
	s.mu.Lock()
	i := s.indexLocked(row.ID)
	if i < 0 || s.rules[i].When.Clock == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	rs := s.states[row.ID]
	s.mu.Unlock()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.gone {
		return ErrNotFound
	}
	if err := rs.blocked(); err != nil {
		return err
	}
	st := rs.state.clone()
	if !set(&st.Status) {
		return nil
	}
	return s.writeStateLocked(ctx, row.ID, rs, &st)
}

// RecordStart marks run as the row's live run, before the run can execute, so its ending has a row to land on
// whenever it comes.
func (s *Store) RecordStart(ctx context.Context, run Run) error {
	return s.UpdateClock(ctx, Row{ID: run.ScheduleID}, func(st *Status) bool {
		st.LastRunID, st.LastStatus, st.LastReason = run.WorkflowID, StatusStarted, ""
		if !run.StartedAt.IsZero() {
			st.LastRunAt = run.StartedAt
		}
		return true
	})
}

// RecordOutcome overwrites the outcome of run, the row's live run, after it started (a late failure such as an
// unattended denial, or a stop marotte made). An outcome of any other run is dropped: a later fire owns the row,
// with its own run or a refusal. It leaves the anchor: moving it would shift the next run by however long the
// failure took.
func (s *Store) RecordOutcome(ctx context.Context, run Run, o Outcome) error {
	return s.UpdateClock(ctx, Row{ID: run.ScheduleID}, func(st *Status) bool {
		if st.LastRunID != run.WorkflowID {
			return false
		}
		st.LastStatus, st.LastReason = o.Status, o.Reason
		return true
	})
}

// RecordEnding records how run ended on its own, only while the row still reads it started: an outcome
// RecordOutcome wrote first (a denial, a bound stop) names the cause, and an ending says less.
func (s *Store) RecordEnding(ctx context.Context, run Run, o Outcome) error {
	return s.UpdateClock(ctx, Row{ID: run.ScheduleID}, func(st *Status) bool {
		if st.LastRunID != run.WorkflowID || st.LastStatus != StatusStarted {
			return false
		}
		st.LastStatus, st.LastReason = o.Status, o.Reason
		return true
	})
}
