package chat

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const wakeSession = "sess_66362e75-badd-490c-ad48-593859625c29"

func wakeRow(text, severity string) marotte.EntrySteer {
	return marotte.EntrySteer{
		Text: text, Origin: marotte.SteerOriginStep, Severity: severity, ToSession: wakeSession,
		OriginRun: "wf_1a6d245a6e99e18e", Step: "prep/wait-apps", ProducedTs: 1792349041432,
	}
}

func (f *logFixture) wakeTurn() string {
	f.t.Helper()
	return f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
}

func TestEntryLog_WakeCausesAreTheRowsWaitingWhenTheWakeTurnOpened(t *testing.T) {
	f := newLogFixture(t)
	prev := f.prompt("deploy it")
	f.append(prev, "", "wfmsg-a", marotte.EntryKindSteer, wakeRow("filed before marotte closed the turn", "warning"))
	f.append(prev, "", "wfmsg-read", marotte.EntryKindSteer, wakeRow("taken up", "info"))
	f.append(prev, "", marotte.SteerDeliveredID("wfmsg-read"), marotte.EntryKindSteerDelivered,
		marotte.EntrySteerDelivered{SteerID: "wfmsg-read", ReadTs: 2})
	f.closeTurn(prev, marotte.TurnOutcomeCompleted)
	f.append(prev, "", "wfmsg-b", marotte.EntryKindSteer, wakeRow("wait-apps 9.7 h (cap now 24 h)", "info"))
	if got := f.log.WakeCauses(wakeSession); len(got.Waiting)+len(got.Cleared) != 0 {
		t.Errorf("with no turn open WakeCauses = %+v, want nothing: no wake to pair", got)
	}

	wake := f.wakeTurn()
	f.append(wake, "", "wfmsg-c", marotte.EntryKindSteer, wakeRow("buffered behind the wake", "warning"))

	got := f.log.WakeCauses(wakeSession)
	var waiting []string
	for _, m := range got.Waiting {
		waiting = append(waiting, m.ID)
	}
	if !slices.Equal(waiting, []string{"wfmsg-a", "wfmsg-b"}) {
		t.Errorf("Waiting = %v, want wfmsg-a then wfmsg-b: the unread rows filed before the wake turn opened", waiting)
	}
}

func TestEntryLog_WakeCausesNameWhatTheStoppedTurnsCancelCleared(t *testing.T) {
	f := newLogFixture(t)
	stopped := f.prompt("keep going")
	f.append(stopped, "", "wfmsg-q", marotte.EntryKindSteer, wakeRow("Which branch should I base on?", "warning"))
	f.append(stopped, "", "cleared-1", marotte.EntryKindSteerCleared, marotte.EntrySteerCleared{
		Cause: marotte.SteerClearCauseCancel, Session: wakeSession, IDs: []string{"notify-347a6dd1"},
	})
	f.closeTurn(stopped, marotte.TurnOutcomeCancelled)
	f.wakeTurn()

	got := f.log.WakeCauses(wakeSession)
	if len(got.Cleared) != 1 || got.Cleared[0].ID != "wfmsg-q" || got.Cleared[0].Steer.Text != "Which branch should I base on?" {
		t.Errorf("Cleared = %+v, want the question the cancel cleared", got.Cleared)
	}
}

func TestEntryLog_ATurnWakeGivesItsTurnTheWakesRailLine(t *testing.T) {
	f := newLogFixture(t)
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonStepMessage,
		Steers: []marotte.WakeSteer{{
			ID: "wfmsg-q", Run: "wf_1a6d245a6e99e18e", RunLabel: "turn-wake", Step: "build", Severity: "warning",
			Text: "Which branch should I base on?\nmain or dev?",
		}},
	})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Basing on dev."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)

	const want = "turn-wake \u00b7 build asks you: Which branch should I base on?"
	for _, phase := range []string{"live", "reopened", "rewritten"} {
		switch phase {
		case "reopened":
			f.reopen()
		case "rewritten":
			all, _, err := f.log.AllWithReverted()
			if err != nil {
				t.Fatalf("Setup: read for the rewrite: %v", err)
			}
			if err := f.log.Rewrite(t.Context(), all); err != nil {
				t.Fatalf("Setup: rewrite: %v", err)
			}
		}
		rows := f.log.RailRows()
		if len(rows) != 1 {
			t.Fatalf("%s: RailRows() = %+v, want the wake turn's row", phase, rows)
		}
		if rows[0].FirstLine != want || !rows[0].AgentInitiated {
			t.Errorf("%s: row = %+v, want first_line %q on an agent-initiated row", phase, rows[0], want)
		}
	}
}

func TestEntryLog_AWakeThatNamesNothingLeavesTheRailLineEmpty(t *testing.T) {
	f := newLogFixture(t)
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{Reason: marotte.WakeReasonUnknown})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Done."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)

	rows := f.log.RailRows()
	if len(rows) != 1 {
		t.Fatalf("RailRows() = %+v, want the wake turn's row", rows)
	}
	if rows[0].FirstLine != "" || !rows[0].AgentInitiated {
		t.Errorf("row = %+v, want no first_line on an agent-initiated row: the rail words it as it does a turn with no wake", rows[0])
	}
}

func TestRenderChatMarkdown_AWakeIsTheTurnsTriggerAndItsMessageRendersOnce(t *testing.T) {
	const question = "Should I apply D22 (one bullet in crew.md), or leave it as a follow-up?"
	f := newLogFixture(t)
	prev := f.prompt("run the release-impact leg")
	f.append(prev, "", "say-0", marotte.EntryKindText, marotte.EntryText{Text: "Launched."})
	f.closeTurn(prev, marotte.TurnOutcomeCompleted)
	f.append(prev, "", "wfmsg-q", marotte.EntryKindSteer, wakeRow(question, "warning"))
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonStepMessage,
		Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: "wf_1a6d245a6e99e18e", RunLabel: "release-impact", Step: "apply", Severity: "warning", Text: question}},
	})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Leave it as a follow-up."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)
	entries, err := f.log.All()
	if err != nil {
		t.Fatalf("Setup: read the log: %v", err)
	}

	md := renderChatMarkdown(&marotte.Chat{ID: "c-abcdef01", Name: "wake"}, entries)
	if n := strings.Count(md, question); n != 1 {
		t.Errorf("the question renders %d times, want once, under the wake:\n%s", n, md)
	}
	if !strings.Contains(md, "**release-impact \u00b7 apply asks you**\n\n"+question) {
		t.Errorf("the wake turn does not open with its label and the question:\n%s", md)
	}
}
