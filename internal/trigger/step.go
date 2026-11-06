package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// attemptState is a record's state in the attempt table; none is a workflow id the rule holds no record for.
type attemptState int

const (
	stateNone attemptState = iota
	stateInvoking
	stateInvokedUnconfirmed
	stateBootHeld
	stateAbandonedCancelling
	stateAbandonedCancelled
)

var stateNames = [...]string{
	stateNone:                "none",
	stateInvoking:            "invoking",
	stateInvokedUnconfirmed:  "invoked-unconfirmed",
	stateBootHeld:            "boot-held",
	stateAbandonedCancelling: "abandoned-cancelling",
	stateAbandonedCancelled:  "abandoned-cancelled",
}

func (s attemptState) String() string {
	if s < 0 || int(s) >= len(stateNames) {
		return "unknown"
	}
	return stateNames[s]
}

// live reports a pending state: only a live record is its entry's current attempt.
func (s attemptState) live() bool {
	return s == stateInvoking || s == stateInvokedUnconfirmed || s == stateBootHeld
}

func (s attemptState) abandoned() bool {
	return s == stateAbandonedCancelling || s == stateAbandonedCancelled
}

// The causes of an abandon, as the record and the log line carry them.
const (
	causeNameFailed      = "name_failed"
	causeInvokeError     = "invoke_error"
	causeLeaseNotGranted = "lease_not_granted"
	causeBound           = "bound"
)

// record is one launch attempt of an event rule: the run marotte created for the event key.
type record struct {
	at             time.Time
	cancelLandedAt time.Time
	// inputs and coalesceKey are the entry's, carried so a confirmation writes them on its launched entry.
	inputs      map[string]string
	bound       *time.Timer
	workflowID  string
	key         string
	coalesceKey string
	cause       string
	state       attemptState
	// cancelsOut counts the cancels queued for the record whose answer has not come back.
	cancelsOut int
	// inFlight holds from A1 until the LaunchTriggered call that named the record returns.
	inFlight bool
	// owesRelease marks a record abandoned while in flight: its cancel may have landed before its lease existed.
	owesRelease bool
	// unpersisted marks a record of a rule the definitions no longer hold, which writes nothing.
	unpersisted bool
}

// The persisted states of a record: every live state is pending, every abandoned one abandoned.
const (
	persistedPending   = "pending"
	persistedAbandoned = "abandoned"
)

// attemptEntry is a record as the state file's attempts list holds it.
type attemptEntry struct {
	At             time.Time `json:"at"`
	CancelLandedAt time.Time `json:"cancel_landed_at,omitzero"`
	WorkflowID     string    `json:"workflow_id"`
	Key            string    `json:"key"`
	State          string    `json:"state"`
	Cause          string    `json:"cause,omitempty"`
}

func encodeAttempts(records []*record) (json.RawMessage, error) {
	out := make([]attemptEntry, 0, len(records))
	for _, r := range records {
		if r.unpersisted {
			continue
		}
		state := persistedAbandoned
		if r.state.live() {
			state = persistedPending
		}
		out = append(out, attemptEntry{
			WorkflowID: r.workflowID, Key: r.key, State: state, Cause: r.cause, At: r.at, CancelLandedAt: r.cancelLandedAt,
		})
	}
	return marshalUnlessEmpty(out, len(out))
}

// stepResult is what one step decided.
type stepResult struct {
	// err is NameAttempt's or Retried's answer.
	err error
	// retry is Retried's answer.
	retry   RetryAnswer
	effects []attemptEffect
	// write is set by a row whose change the state file must carry.
	write bool
}

// errImpossibleTransition answers a call whose cell no row of the attempt table names.
var errImpossibleTransition = errors.New(logMsgImpossible)

// stepper is one step call's view of its rule, under the rule's mutex.
type stepper struct {
	now time.Time
	c   *Confirmer
	rv  *ruleValue
	// nameRec is the record A1 named, which W1 abandons when its write fails.
	nameRec *record
	// handBack is the record A33 dropped, which a failed write restores.
	handBack *record
	ruleID   string
	// def is the rule's definition; held is false once launch-rules.json no longer holds it.
	def  launchrule.Rule
	res  stepResult
	held bool
	// wakeOnWrite wakes the launcher once the step's write lands.
	wakeOnWrite bool
}

// step applies the attempt table's row for ev to the rule and writes the rule's state file at most once. It is the
// only writer of an event rule's state; no caller holds another rule's mutex while it calls step.
func (c *Confirmer) step(ruleID string, ev *attemptEvent) stepResult {
	rv := c.lock(ruleID)
	defer rv.mu.Unlock()
	s := &stepper{c: c, rv: rv, ruleID: ruleID, now: time.Now()}
	s.def, s.held = c.store.Get(ruleID)
	var rec *record
	var named bool
	if ev.kind.isRecord() {
		rec = rv.find(ev.workflowID)
		if ev.kind == evEngineReady && (rec == nil || rec.state != stateBootHeld) {
			return s.res // engine ready is delivered only to a boot-held record
		}
		named = s.apply(rec, ev)
	} else {
		named = s.ruleRow(ev)
	}
	if !named {
		s.res = impossible(ruleID, rec, ev)
	}
	s.commit()
	if !s.held && len(rv.records) == 0 {
		c.forget(ruleID, rv)
	}
	return s.res
}

// impossible is row A0: a cell no other row names, where an implementation bug lands, loudly.
func impossible(ruleID string, rec *record, ev *attemptEvent) stepResult {
	state := stateNone
	if rec != nil {
		state = rec.state
	}
	slog.Error(logMsgImpossible, "rule_id", ruleID, "state", state.String(), "event", ev.kind.String(),
		"workflow_id", ev.workflowID)
	return stepResult{err: errImpossibleTransition}
}

// writeLocked writes the rule's whole value as its state file; rv.mu is held.
func (c *Confirmer) writeLocked(ruleID string, rv *ruleValue) error {
	st, err := rv.es.encode()
	if err == nil {
		st.Attempts, err = encodeAttempts(rv.records)
	}
	if err != nil {
		return fmt.Errorf("encode the state of launch rule %s: %w", ruleID, err)
	}
	// A local file write that must land whatever the caller's lifetime; it carries no caller context.
	return c.writer.WriteState(context.Background(), ruleID, &st)
}
