package marotte

import (
	"encoding/json"
	"testing"
)

func (l *wakeLog) rowWithID(id, kasID string) {
	l.add(EntryKindSteer, id, EntrySteer{Text: id, Origin: SteerOriginStep, Severity: "info", ToSession: wakeChat, KASID: kasID})
}

// settledRow is the row id as a reader of the fold sees it.
func settledRow(t *testing.T, w *WorkflowMessageFold, id string) EntrySteer {
	t.Helper()
	raw, err := json.Marshal(EntrySteer{Text: id, Origin: SteerOriginStep, ToSession: wakeChat})
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{ID: id, Kind: EntryKindSteer, Payload: raw}
	w.Settle(&e)
	var out EntrySteer
	if err := json.Unmarshal(e.Payload, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func foldOf(l *wakeLog) WorkflowMessageFold {
	facts := make([]WorkflowMessageFact, len(l.facts))
	for i := range l.facts {
		facts[i] = l.facts[i].Fact
	}
	return FoldWorkflowMessages(facts)
}

// A clear settles a row KAS gave an id only when it names that id: a turn boundary's drops it unread, a reader's
// cancel leaves it for KAS's re-wake. A row with no id, or one a cancel left that no re-wake delivered, settles
// cleared at its session's next clear.
func TestFoldWorkflowMessages_AClearSettlesRowsByKASID(t *testing.T) {
	l := &wakeLog{t: t}
	l.rowWithID("wfmsg-named", "notify-a")
	l.rowWithID("wfmsg-unnamed", "notify-b")
	l.rowWithID("wfmsg-noid", "")
	l.rowWithID("wfmsg-stopped", "notify-c")
	l.clear(SteerClearCauseBoundary, "notify-a")
	l.clear(SteerClearCauseCancel, "notify-c")

	w := foldOf(l)
	if s := settledRow(t, &w, "wfmsg-named"); s.State != SteerStateDropped || s.Reason != SteerReasonBoundary || s.Cleared {
		t.Errorf("named row = %+v, want dropped at the boundary", s)
	}
	if s := settledRow(t, &w, "wfmsg-noid"); !s.Cleared || s.State != "" {
		t.Errorf("id-less row = %+v, want cleared", s)
	}
	waiting := w.Unsettled()
	if !sameIDs(waiting, "wfmsg-unnamed", "wfmsg-stopped") {
		t.Fatalf("unsettled = %v, want the unnamed row and the row the stop left", messageIDs(waiting))
	}
	if waiting[0].Rewaking || !waiting[1].Rewaking {
		t.Errorf("rewaking = %v, %v; want only the row the stop named", waiting[0].Rewaking, waiting[1].Rewaking)
	}

	l.clear(SteerClearCauseBoundary, "notify-z")
	w = foldOf(l)
	if s := settledRow(t, &w, "wfmsg-stopped"); !s.Cleared {
		t.Errorf("row the stop left = %+v, want cleared by the next clear", s)
	}
	if !sameIDs(w.Unsettled(), "wfmsg-unnamed") {
		t.Errorf("unsettled = %v, want only the row no clear named", messageIDs(w.Unsettled()))
	}
}
