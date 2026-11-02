package chat

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	wakeSession = "sess_0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	wakeRunID   = "wf_5e1f0c2a9b7d4e36"
)

func wakeRow(text, severity string) marotte.EntrySteer {
	return marotte.EntrySteer{
		Text: text, Origin: marotte.SteerOriginStep, Severity: severity, ToSession: wakeSession,
		OriginRun: wakeRunID, Step: "build/check-links", ProducedTs: 1792349041432,
	}
}

func (f *logFixture) wakeTurn() string {
	f.t.Helper()
	return f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
}

func (f *logFixture) phases(check func(phase string)) {
	f.t.Helper()
	for _, phase := range []string{"live", "reopened", "rewritten"} {
		switch phase {
		case "reopened":
			f.reopen()
		case "rewritten":
			all, _, err := f.log.AllWithReverted()
			if err != nil {
				f.t.Fatalf("Setup: read for the rewrite: %v", err)
			}
			if err := f.log.Rewrite(f.t.Context(), all); err != nil {
				f.t.Fatalf("Setup: rewrite: %v", err)
			}
		}
		check(phase)
	}
}

func TestEntryLog_WakeCausesAreTheRowsWaitingWhenTheWakeTurnOpened(t *testing.T) {
	f := newLogFixture(t)
	prev := f.prompt("publish the docs")
	f.append(prev, "", "wfmsg-a", marotte.EntryKindSteer, wakeRow("filed before marotte closed the turn", "warning"))
	f.append(prev, "", "wfmsg-read", marotte.EntryKindSteer, wakeRow("taken up", "info"))
	f.append(prev, "", marotte.SteerDeliveredID("wfmsg-read"), marotte.EntryKindSteerDelivered,
		marotte.EntrySteerDelivered{SteerID: "wfmsg-read", ReadTs: 2})
	f.closeTurn(prev, marotte.TurnOutcomeCompleted)
	f.append(prev, "", "wfmsg-b", marotte.EntryKindSteer, wakeRow("Link check done: 412 links, none broken.", "info"))
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
		Cause: marotte.SteerClearCauseCancel, Session: wakeSession, IDs: []string{"notify-5b7c1d2e"},
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
			ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning",
			Text: "Which branch should I base on?\nmain or dev?",
		}},
	})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Basing on dev."})
	if rows := f.log.RailRows(); len(rows) != 1 || rows[0].FirstLine != "A workflow \u00b7 draft asks you: Which branch should I base on?" {
		t.Errorf("before the run is named: RailRows() = %+v, want the placeholder's line", rows)
	}
	f.append(wake, "", "named-1", marotte.EntryKindRunNamed, marotte.EntryRunNamed{Run: wakeRunID, Name: "publish-docs"})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)

	const want = "publish-docs \u00b7 draft asks you: Which branch should I base on?"
	f.phases(func(phase string) {
		rows := f.log.RailRows()
		if len(rows) != 1 {
			t.Fatalf("%s: RailRows() = %+v, want the wake turn's row", phase, rows)
		}
		if rows[0].FirstLine != want || !rows[0].AgentInitiated {
			t.Errorf("%s: row = %+v, want first_line %q on an agent-initiated row", phase, rows[0], want)
		}
	})
}

func TestEntryLog_ARunNamedInALaterTurnNamesTheEarlierWake(t *testing.T) {
	f := newLogFixture(t)
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonRunFinished, Run: wakeRunID, RunStatus: marotte.RunStatusCompleted,
	})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Reading the results."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)
	next := f.prompt("thanks")
	f.append(next, "", "named-1", marotte.EntryKindRunNamed, marotte.EntryRunNamed{Run: wakeRunID, Name: "publish-docs"})

	f.phases(func(phase string) {
		rows := f.log.RailRows()
		if len(rows) != 2 || rows[0].FirstLine != "publish-docs completed" {
			t.Errorf("%s: RailRows() = %+v, want the wake's row named by the later run_named", phase, rows)
		}
		if len(rows) == 2 && rows[1].FirstLine != "thanks" {
			t.Errorf("%s: the prompt turn's row = %+v, want its prompt: run_named draws nothing", phase, rows[1])
		}
	})
}

func TestEntryLog_RewindingTheTurnARunNamedWasFiledInUnnamesTheWakeEverywhere(t *testing.T) {
	f := newLogFixture(t)
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonRunFinished, Run: wakeRunID, RunStatus: marotte.RunStatusCompleted,
	})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Reading the results."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)
	next := f.prompt("thanks")
	f.append(next, "", "named-1", marotte.EntryKindRunNamed, marotte.EntryRunNamed{Run: wakeRunID, Name: "publish-docs"})
	f.closeTurn(next, marotte.TurnOutcomeCompleted)
	if f.log.AwaitsRunName(wakeRunID) {
		t.Errorf("before the rewind: AwaitsRunName = true, want false: the run_named names the wake")
	}
	revertRecord(f, wake, next, next, 2)

	f.phases(func(phase string) {
		rows := f.log.RailRows()
		if len(rows) != 1 || rows[0].FirstLine != "A workflow completed" {
			t.Errorf("%s: RailRows() = %+v, want the wake's placeholder line: its name was rewound", phase, rows)
		}
		for _, e := range mustAll(t, f.log) {
			if e.Kind == marotte.EntryKindRunNamed {
				t.Errorf("%s: All() holds %s, want the rewound run_named gone", phase, e.ID)
			}
		}
		if !f.log.AwaitsRunName(wakeRunID) {
			t.Errorf("%s: AwaitsRunName = false, want true: the wake is unnamed again", phase)
		}
	})
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

func TestEntryLog_ATurnWhoseOnlyRowAWakeDrawsHoldsNoRailSlot(t *testing.T) {
	for _, reason := range []marotte.WakeReason{marotte.WakeReasonStepMessage, marotte.WakeReasonRewake} {
		t.Run(string(reason), func(t *testing.T) {
			f := newLogFixture(t)
			empty := f.wakeTurn()
			f.closeTurn(empty, marotte.TurnOutcomeCompleted)
			f.append(empty, "", "wfmsg-q", marotte.EntryKindSteer, wakeRow("Should I rewrite the links?", "warning"))
			if got := railIDs(f.log); !slices.Equal(got, []string{empty}) {
				t.Fatalf("Setup: RailRows() = %v, want the row's turn drawn by the row", got)
			}
			wake := f.wakeTurn()
			f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
				Reason: reason,
				Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning", Text: "Should I rewrite the links?"}},
			})
			f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Rewriting them."})
			f.closeTurn(wake, marotte.TurnOutcomeCompleted)

			f.phases(func(phase string) {
				if got := railIDs(f.log); !slices.Equal(got, []string{wake}) {
					t.Errorf("%s: RailRows() = %v, want only the wake turn: the first turn no longer draws anything", phase, got)
				}
			})
		})
	}
}

func TestEntryLog_ATurnKeepsItsRailSlotWhenAWakeTakesOneOfItsRows(t *testing.T) {
	f := newLogFixture(t)
	prev := f.wakeTurn()
	f.append(prev, "", "say-0", marotte.EntryKindText, marotte.EntryText{Text: "Launched publish-docs."})
	f.closeTurn(prev, marotte.TurnOutcomeCompleted)
	f.append(prev, "", "wfmsg-q", marotte.EntryKindSteer, wakeRow("Should I rewrite the links?", "warning"))
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonStepMessage,
		Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning", Text: "Should I rewrite the links?"}},
	})

	if got := railIDs(f.log); !slices.Equal(got, []string{prev}) {
		t.Errorf("RailRows() = %v, want the first turn, still drawn by its reply (the open wake turn draws nothing yet)", got)
	}
}

func TestRenderChatMarkdown_AWakeIsTheTurnsTriggerAndItsMessageRendersOnce(t *testing.T) {
	const question = "Two pages still link to the old domain. Should I rewrite those links now?"
	f := newLogFixture(t)
	prev := f.prompt("publish the docs")
	f.append(prev, "", "say-0", marotte.EntryKindText, marotte.EntryText{Text: "Launched."})
	f.closeTurn(prev, marotte.TurnOutcomeCompleted)
	f.append(prev, "", "wfmsg-q", marotte.EntryKindSteer, wakeRow(question, "warning"))
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonStepMessage,
		Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning", Text: question}},
	})
	f.append(wake, "", "named-1", marotte.EntryKindRunNamed, marotte.EntryRunNamed{Run: wakeRunID, Name: "publish-docs"})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Rewrite them now."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)
	entries, err := f.log.All()
	if err != nil {
		t.Fatalf("Setup: read the log: %v", err)
	}

	md := renderChatMarkdown(&marotte.Chat{ID: "c-abcdef01", Name: "wake"}, entries)
	if n := strings.Count(md, question); n != 1 {
		t.Errorf("the question renders %d times, want once, under the wake:\n%s", n, md)
	}
	if !strings.Contains(md, "**publish-docs \u00b7 draft asks you**\n\n"+question) {
		t.Errorf("the wake turn does not open with its label and the question:\n%s", md)
	}
}

func TestRenderChatMarkdown_AReDeliveryRendersEachMessageOnceUnderTheWake(t *testing.T) {
	const question = "Which branch should I base the release notes on?"
	f := newLogFixture(t)
	stopped := f.prompt("keep going")
	f.append(stopped, "", "wfmsg-q", marotte.EntryKindSteer, wakeRow(question, "warning"))
	f.append(stopped, "", "cleared-1", marotte.EntryKindSteerCleared, marotte.EntrySteerCleared{
		Cause: marotte.SteerClearCauseCancel, Session: wakeSession, IDs: []string{"notify-5b7c1d2e"},
	})
	f.closeTurn(stopped, marotte.TurnOutcomeCancelled)
	wake := f.wakeTurn()
	f.append(wake, "", "wake-1", marotte.EntryKindTurnWake, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonRewake,
		Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning", Text: question}},
	})
	f.append(wake, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "Basing on dev."})
	f.closeTurn(wake, marotte.TurnOutcomeCompleted)
	entries, err := f.log.All()
	if err != nil {
		t.Fatalf("Setup: read the log: %v", err)
	}

	md := renderChatMarkdown(&marotte.Chat{ID: "c-abcdef01", Name: "wake"}, entries)
	if n := strings.Count(md, question); n != 1 {
		t.Errorf("the question renders %d times, want once, under the re-delivery:\n%s", n, md)
	}
}
