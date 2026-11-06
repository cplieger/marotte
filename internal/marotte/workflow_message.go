package marotte

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// WorkflowMessageIDPrefix marks a workflow message's row, minted at its `_kiro/session/notify`.
const WorkflowMessageIDPrefix = "wfmsg-"

// RunNoticeIDPrefix is the id shape KAS mints for a finished run's completion notice
// (`notify-wf-<uuid>`; autoWakeParentOnComplete, parent_busy_queued). The uuid is random, so
// the run it reports is not in the id: the notice's words name it.
const RunNoticeIDPrefix = "notify-wf-"

// IsWorkflowMessageNotifyID reports a steering id of the shape KAS mints for a `_kiro/session/notify`
// message (`notify-<uuid>`); a run notice's is not one.
func IsWorkflowMessageNotifyID(id string) bool {
	return strings.HasPrefix(id, "notify-") && !strings.HasPrefix(id, RunNoticeIDPrefix)
}

// WorkflowMessage is a workflow message's row as its log holds it. Rewaking marks an unsettled row
// a reader's cancel named, which KAS re-prompts in a turn of its own (rewakeClearedNotifications).
type WorkflowMessage struct {
	ID       string
	Steer    EntrySteer
	Rewaking bool
}

// Settlement is what c settles m as, false when it settles nothing. A row KAS gave no id, or one
// an earlier cancel named that no re-wake turn delivered, is settled cleared by any clear of its
// session: nothing proves it read or dropped. A row with an id is settled only by a clear that
// names it, dropped at a turn boundary, which clears steering KAS never injected. A cancel naming
// it settles nothing (Rewakes).
func (c *EntrySteerCleared) Settlement(m *WorkflowMessage, ts int64) (EntrySteerDelivered, bool) {
	if m.Steer.ToSession == "" || m.Steer.ToSession != c.Session {
		return EntrySteerDelivered{}, false
	}
	if m.Steer.KASID == "" || m.Rewaking {
		return EntrySteerDelivered{SteerID: m.ID, ReadTs: ts, Cleared: true}, true
	}
	if c.Cause != SteerClearCauseBoundary || !slices.Contains(c.IDs, m.Steer.KASID) {
		return EntrySteerDelivered{}, false
	}
	return EntrySteerDelivered{SteerID: m.ID, ReadTs: ts, Dropped: true}, true
}

// Rewakes reports a row c leaves for KAS's re-wake: a reader's cancel naming it by id.
func (c *EntrySteerCleared) Rewakes(m *WorkflowMessage) bool {
	return c.Cause == SteerClearCauseCancel && m.Steer.ToSession != "" && m.Steer.ToSession == c.Session &&
		m.Steer.KASID != "" && slices.Contains(c.IDs, m.Steer.KASID)
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

// row is the message row f files, false for a settlement or a clear.
func (f *WorkflowMessageFact) row() (WorkflowMessage, bool) {
	if f.message == nil {
		return WorkflowMessage{}, false
	}
	return *f.message, true
}

// clear is the steering clear f files, false for a row or a settlement.
func (f *WorkflowMessageFact) clear() (EntrySteerCleared, bool) {
	if f.cleared == nil {
		return EntrySteerCleared{}, false
	}
	return *f.cleared, true
}

// WorkflowMessageFold is a history's workflow-message rows joined with their settlements and clears.
// It holds only what it was given: a caller with a rewind feeds it the surviving history alone.
type WorkflowMessageFold struct {
	settled  map[string]EntrySteerDelivered
	rewaking map[string]struct{}
	rows     []WorkflowMessage
}

// FoldWorkflowMessages folds facts given in file order. A clear settles its rows as it is read
// (EntrySteerCleared.Settlement), so a log cut after the clear's fact still reads them settled; a
// later steer_delivered for the same row replaces that settlement.
func FoldWorkflowMessages(facts []WorkflowMessageFact) WorkflowMessageFold {
	w := WorkflowMessageFold{settled: map[string]EntrySteerDelivered{}, rewaking: map[string]struct{}{}}
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

func (w *WorkflowMessageFold) clear(c *EntrySteerCleared, ts int64) {
	for i := range w.rows {
		if _, settled := w.settled[w.rows[i].ID]; settled {
			continue
		}
		m := w.view(&w.rows[i])
		if d, ok := c.Settlement(&m, ts); ok {
			w.settled[m.ID] = d
		} else if c.Rewakes(&m) {
			w.rewaking[m.ID] = struct{}{}
		}
	}
}

func (w *WorkflowMessageFold) view(m *WorkflowMessage) WorkflowMessage {
	out := *m
	_, out.Rewaking = w.rewaking[m.ID]
	return out
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
	switch {
	case d.Cleared:
		steer.Cleared = true
	case d.Dropped:
		steer.State, steer.Reason = SteerStateDropped, SteerReasonBoundary
	default:
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
			out = append(out, w.view(m))
		}
	}
	return out
}
