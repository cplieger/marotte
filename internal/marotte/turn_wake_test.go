package marotte

import (
	"encoding/json"
	"testing"
)

const (
	wakeChat = "sess_chat"
	wakeStep = "sess_step"
)

// wakeLog is a log's workflow-message lines in file order; a line's index is its offset.
type wakeLog struct {
	t     *testing.T
	facts []PositionedFact
}

func (l *wakeLog) add(kind EntryKind, id string, payload any) {
	l.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		l.t.Fatal(err)
	}
	f, ok, err := WorkflowMessageFactOf(&Entry{ID: id, Kind: kind, Payload: raw})
	if err != nil || !ok {
		l.t.Fatalf("fact of %s %s: ok=%v err=%v", kind, id, ok, err)
	}
	l.facts = append(l.facts, PositionedFact{Fact: f, At: int64(len(l.facts))})
}

func (l *wakeLog) row(id, to, severity, text string) {
	l.add(EntryKindSteer, id, EntrySteer{Text: text, Origin: SteerOriginStep, Severity: severity, ToSession: to, OriginRun: "wf_1"})
}

func (l *wakeLog) delivered(id string) {
	l.add(EntryKindSteerDelivered, SteerDeliveredID(id), EntrySteerDelivered{SteerID: id, ReadTs: 1})
}

func (l *wakeLog) clear(cause SteerClearCause, ids ...string) {
	l.add(EntryKindSteerCleared, SteerClearedIDPrefix+string(cause), EntrySteerCleared{Cause: cause, Session: wakeChat, IDs: ids})
}

// here is the offset the next line would take: where a turn opens.
func (l *wakeLog) here() int64 { return int64(len(l.facts)) }

func ids(ms []WorkflowMessage) []string {
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = ms[i].ID
	}
	return out
}

func sameIDs(got []WorkflowMessage, want ...string) bool {
	g := ids(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func TestWakeCausesOf_WaitingIsWhatReachedTheSessionUnreadBeforeTheTurnOpened(t *testing.T) {
	l := &wakeLog{t: t}
	prev := l.here()
	l.row("wfmsg-a", wakeChat, "warning", "filed in the previous turn after KAS ended it")
	l.row("wfmsg-read", wakeChat, "info", "taken up by the previous turn")
	l.delivered("wfmsg-read")
	l.row("wfmsg-to-step", wakeStep, "info", "the chat's own message to a step")
	l.row("wfmsg-b", wakeChat, "info", "filed between the turns")
	opened := l.here()
	l.row("wfmsg-late", wakeChat, "warning", "buffered behind the wake")

	got := WakeCausesOf(l.facts, wakeChat, opened, prev)
	if !sameIDs(got.Waiting, "wfmsg-a", "wfmsg-b") {
		t.Errorf("Waiting = %v, want the two unread rows sent to the chat before the turn opened, oldest first", ids(got.Waiting))
	}
	if len(got.Cleared) != 0 {
		t.Errorf("Cleared = %v, want none: no reader's cancel", ids(got.Cleared))
	}
	if none := WakeCausesOf(l.facts, "", opened, prev); len(none.Waiting)+len(none.Cleared) != 0 {
		t.Errorf("an empty session answered %+v, want nothing", none)
	}
}

func TestWakeCausesOf_ClearedIsWhatTheNewestCancelOfTheLastTurnEmptied(t *testing.T) {
	l := &wakeLog{t: t}
	l.row("wfmsg-old", wakeChat, "info", "cleared by a cancel two turns ago")
	l.clear(SteerClearCauseCancel, "notify-old")
	prev := l.here()
	l.row("wfmsg-q", wakeChat, "warning", "the step's question")
	l.row("wfmsg-r", wakeChat, "success", "a result")
	l.clear(SteerClearCauseCancel, "notify-q", "notify-r")
	opened := l.here()

	got := WakeCausesOf(l.facts, wakeChat, opened, prev)
	if !sameIDs(got.Cleared, "wfmsg-q", "wfmsg-r") {
		t.Errorf("Cleared = %v, want the rows the previous turn's cancel emptied", ids(got.Cleared))
	}
	if len(got.Waiting) != 0 {
		t.Errorf("Waiting = %v, want none: the clear settled every row", ids(got.Waiting))
	}
}

func TestWakeCausesOf_ACancelBeforeThePreviousTurnIsNoRewake(t *testing.T) {
	l := &wakeLog{t: t}
	l.row("wfmsg-old", wakeChat, "warning", "cleared two turns ago")
	l.clear(SteerClearCauseCancel, "notify-old")
	prev := l.here()
	l.row("wfmsg-a", wakeChat, "info", "the waker")

	got := WakeCausesOf(l.facts, wakeChat, l.here(), prev)
	if len(got.Cleared) != 0 {
		t.Errorf("Cleared = %v, want none: that cancel's re-delivery was an earlier turn", ids(got.Cleared))
	}
	if !sameIDs(got.Waiting, "wfmsg-a") {
		t.Errorf("Waiting = %v, want the waker", ids(got.Waiting))
	}
}

func TestWakeCausesOf_OnlyACancelNamingAWorkflowMessageIsARewake(t *testing.T) {
	cases := []struct {
		name  string
		cause SteerClearCause
		ids   []string
	}{
		{name: "a turn-end clear", cause: SteerClearCauseBoundary, ids: []string{"notify-q"}},
		{name: "a cancel naming only a run notice", cause: SteerClearCauseCancel, ids: []string{"notify-wf-1"}},
		{name: "a cancel naming only a user steer", cause: SteerClearCauseCancel, ids: []string{"steer-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &wakeLog{t: t}
			prev := l.here()
			l.row("wfmsg-q", wakeChat, "warning", "the step's question")
			l.clear(c.cause, c.ids...)
			got := WakeCausesOf(l.facts, wakeChat, l.here(), prev)
			if len(got.Cleared) != 0 {
				t.Errorf("Cleared = %v, want none: KAS re-delivers nothing after it", ids(got.Cleared))
			}
		})
	}
}

func TestWakeCausesOf_ARowLeftUnreadBeforeThePreviousTurnIsNotWaiting(t *testing.T) {
	l := &wakeLog{t: t}
	l.row("wfmsg-stale", wakeChat, "warning", "its wake never started")
	prev := l.here()
	l.row("wfmsg-a", wakeChat, "info", "the waker")

	got := WakeCausesOf(l.facts, wakeChat, l.here(), prev)
	if !sameIDs(got.Waiting, "wfmsg-a") {
		t.Errorf("Waiting = %v, want only the row filed since the previous turn opened", ids(got.Waiting))
	}
}

func TestWakeCausesOf_RacingIsWhatWaitsFiledAfterTheRewakingCancel(t *testing.T) {
	l := &wakeLog{t: t}
	prev := l.here()
	l.row("wfmsg-before", wakeChat, "success", "a result the cancel cleared")
	l.clear(SteerClearCauseCancel, "notify-before")
	l.row("wfmsg-after", wakeChat, "warning", "a question filed after the cancel")

	got := WakeCausesOf(l.facts, wakeChat, l.here(), prev)
	if !sameIDs(got.Cleared, "wfmsg-before") {
		t.Errorf("Cleared = %v, want the row the cancel cleared", ids(got.Cleared))
	}
	if !sameIDs(got.Racing, "wfmsg-after") {
		t.Errorf("Racing = %v, want the question filed after the cancel", ids(got.Racing))
	}
	if none := WakeCausesOf(l.facts[:2], wakeChat, 2, prev); len(none.Racing) != 0 {
		t.Errorf("with nothing filed after the cancel Racing = %v, want none", ids(none.Racing))
	}
}

func TestWakeDrawnSteers_AreAStepMessagesWakerAndAReDeliverysRows(t *testing.T) {
	steers := []WakeSteer{{ID: "wfmsg-a"}, {ID: "wfmsg-b"}}
	cases := []struct {
		reason WakeReason
		want   []string
	}{
		{WakeReasonStepMessage, []string{"wfmsg-a", "wfmsg-b"}},
		{WakeReasonRewake, []string{"wfmsg-a", "wfmsg-b"}},
		{WakeReasonRunFinished, nil},
		{WakeReasonUnknown, nil},
	}
	for _, c := range cases {
		t.Run(string(c.reason), func(t *testing.T) {
			got := WakeDrawnSteers(&EntryTurnWake{Reason: c.reason, Steers: steers})
			if len(got) != len(c.want) || (len(got) > 0 && (got[0] != c.want[0] || got[1] != c.want[1])) {
				t.Errorf("WakeDrawnSteers = %v, want %v", got, c.want)
			}
		})
	}
}
