package marotte

import (
	"encoding/json"
	"fmt"
	"strings"
)

// WorkflowMessageIDPrefix marks a workflow message's row, minted at its `_kiro/session/notify`.
const WorkflowMessageIDPrefix = "wfmsg-"

// WorkflowMessage is a workflow message's row as its log holds it.
type WorkflowMessage struct {
	ID    string
	Steer EntrySteer
}

// WorkflowMessageFact is one workflow-message line: a message's row, a settlement of one, or a
// clear ending an epoch (filed at clearTs).
type WorkflowMessageFact struct {
	message   *WorkflowMessage
	delivered *EntrySteerDelivered
	cleared   *EntrySteerCleared
	clearTs   int64
}

// WorkflowMessageFactOf decodes e's workflow-message fact; false for any other entry.
func WorkflowMessageFactOf(e *Entry) (WorkflowMessageFact, bool, error) {
	switch {
	case e.Kind == EntryKindSteer && strings.HasPrefix(e.ID, WorkflowMessageIDPrefix):
		var steer EntrySteer
		if err := json.Unmarshal(e.Payload, &steer); err != nil {
			return WorkflowMessageFact{}, false, fmt.Errorf("workflow message %q: %w", e.ID, err)
		}
		return WorkflowMessageFact{message: &WorkflowMessage{ID: e.ID, Steer: steer}}, true, nil
	case e.Kind == EntryKindSteerDelivered:
		var d EntrySteerDelivered
		if err := json.Unmarshal(e.Payload, &d); err != nil {
			return WorkflowMessageFact{}, false, fmt.Errorf("workflow message take-up %q: %w", e.ID, err)
		}
		return WorkflowMessageFact{delivered: &d}, true, nil
	case e.Kind == EntryKindSteerCleared:
		var c EntrySteerCleared
		if err := json.Unmarshal(e.Payload, &c); err != nil {
			return WorkflowMessageFact{}, false, fmt.Errorf("steering clear %q: %w", e.ID, err)
		}
		return WorkflowMessageFact{cleared: &c, clearTs: e.Ts}, true, nil
	}
	return WorkflowMessageFact{}, false, nil
}

// WorkflowMessageFold is a history's workflow-message rows joined with their settlements and clears.
// It holds only what it was given: a caller with a rewind feeds it the surviving history alone.
type WorkflowMessageFold struct {
	settled map[string]EntrySteerDelivered
	rows    []WorkflowMessage
}

// FoldWorkflowMessages folds facts given in file order. A clear settles every unsettled row of its
// epoch cleared as it is read, so a log cut after the clear's fact still reads the epoch settled; a
// later steer_delivered for the same row replaces that settlement.
func FoldWorkflowMessages(facts []WorkflowMessageFact) WorkflowMessageFold {
	w := WorkflowMessageFold{settled: map[string]EntrySteerDelivered{}}
	for _, f := range facts {
		switch {
		case f.message != nil:
			w.rows = append(w.rows, *f.message)
		case f.delivered != nil:
			w.settled[f.delivered.SteerID] = *f.delivered
		case f.cleared != nil:
			w.clear(f.cleared, f.clearTs)
		}
	}
	return w
}

// clear settles cleared every unsettled row sent to the session whose buffer c emptied, which is
// that session's whole epoch: a row an earlier clear ended is settled already.
func (w *WorkflowMessageFold) clear(c *EntrySteerCleared, ts int64) {
	for i := range w.rows {
		m := &w.rows[i]
		if m.Steer.ToSession == "" || m.Steer.ToSession != c.Session {
			continue
		}
		if _, settled := w.settled[m.ID]; !settled {
			w.settled[m.ID] = EntrySteerDelivered{SteerID: m.ID, ReadTs: ts, Cleared: true}
		}
	}
}

// Settle writes a folded settlement onto its row, so a reader of the row never needs the later entry.
func (w *WorkflowMessageFold) Settle(e *Entry) {
	if e.Kind != EntryKindSteer || !strings.HasPrefix(e.ID, WorkflowMessageIDPrefix) {
		return
	}
	d, ok := w.settled[e.ID]
	if !ok {
		return
	}
	var steer EntrySteer
	if json.Unmarshal(e.Payload, &steer) != nil {
		return
	}
	if d.Cleared {
		steer.Cleared = true
	} else {
		steer.State, steer.ReadTs = SteerStateRead, d.ReadTs
	}
	if raw, err := json.Marshal(steer); err == nil {
		e.Payload = raw
	}
}

// Unsettled answers the rows no take-up, wake or clear has settled, oldest first.
func (w *WorkflowMessageFold) Unsettled() []WorkflowMessage {
	var out []WorkflowMessage
	for i := range w.rows {
		m := &w.rows[i]
		if _, settled := w.settled[m.ID]; !settled {
			out = append(out, *m)
		}
	}
	return out
}
