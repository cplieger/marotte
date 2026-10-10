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

// WorkflowMessageFact is one workflow-message line: a message's row, or a take-up settling one.
type WorkflowMessageFact struct {
	message   *WorkflowMessage
	delivered *EntrySteerDelivered
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
	}
	return WorkflowMessageFact{}, false, nil
}

// WorkflowMessageFold is a history's workflow-message rows joined with their take-ups. It holds
// only what it was given: a caller with a rewind feeds it the surviving history alone.
type WorkflowMessageFold struct {
	settled map[string]EntrySteerDelivered
	rows    []WorkflowMessage
}

// FoldWorkflowMessages folds facts given in file order.
func FoldWorkflowMessages(facts []WorkflowMessageFact) WorkflowMessageFold {
	var w WorkflowMessageFold
	for _, f := range facts {
		switch {
		case f.message != nil:
			w.rows = append(w.rows, *f.message)
		case f.delivered != nil:
			if w.settled == nil {
				w.settled = make(map[string]EntrySteerDelivered)
			}
			w.settled[f.delivered.SteerID] = *f.delivered
		}
	}
	return w
}

// Settle writes a folded take-up onto its row, so a reader of the row never needs the later entry.
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
	if d.Dropped {
		steer.State, steer.Reason = SteerStateDropped, SteerReasonBoundary
	} else {
		steer.State, steer.ReadTs = SteerStateRead, d.ReadTs
	}
	if raw, err := json.Marshal(steer); err == nil {
		e.Payload = raw
	}
}

// Waiting answers the rows no take-up has settled and none was delivered at its send, oldest first.
func (w *WorkflowMessageFold) Waiting() []WorkflowMessage {
	var out []WorkflowMessage
	for i := range w.rows {
		m := &w.rows[i]
		if _, settled := w.settled[m.ID]; settled || m.Steer.ReadTs != 0 || m.Steer.State == SteerStateDropped {
			continue
		}
		out = append(out, *m)
	}
	return out
}
