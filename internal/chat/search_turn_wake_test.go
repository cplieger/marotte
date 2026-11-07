package chat

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func wakeTurnFixture(id string, n uint64, w marotte.EntryTurnWake) *turnFixture {
	tf := &turnFixture{id: id}
	tf.add("", id, marotte.EntryKindTurnOpen, marotte.EntryTurnOpen{Source: marotte.TurnOpenNameWireTurnStart, N: n})
	tf.add("", id+":wake", marotte.EntryKindTurnWake, w)
	return tf.text(id+":say", "On it.")
}

func TestSearch_AWakesHeaderTextIsFoundOnTheWake(t *testing.T) {
	const question = "Should I rewrite the old-domain links?"
	prev := openTurn("t-1", 1, prompt("m-1", "publish the docs")).text("a1", "Launched.").
		add("", "wfmsg-q", marotte.EntryKindSteer, wakeRow(question, "warning"))
	wake := wakeTurnFixture("t-2", 2, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonStepMessage,
		Steers: []marotte.WakeSteer{{ID: "wfmsg-q", Run: wakeRunID, Step: "draft", Severity: "warning", Text: question}},
	})
	finished := wakeTurnFixture("t-3", 3, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonRunFinished, Run: wakeRunID, RunName: "publish-docs", RunStatus: marotte.RunStatusCompleted,
	})
	later := wakeTurnFixture("t-4", 4, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonRunFinished, Run: "wf_7c3a9e1f0b2d4a58", RunStatus: marotte.RunStatusFailed,
	}).add("", "named-1", marotte.EntryKindRunNamed, marotte.EntryRunNamed{Run: "wf_7c3a9e1f0b2d4a58", Name: "nightly-report"})
	entries, drawn := chatOf(prev, wake, finished, later)

	cases := []struct {
		query, turn string
	}{
		{"old-domain links", "t-2"},
		{"draft asks you", "t-2"},
		{"publish-docs completed", "t-3"},
		{"nightly-report failed", "t-4"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			hits := searchHits(entries, drawn, c.query)
			if len(hits) != 1 {
				t.Fatalf("hits = %+v, want one, on the wake: its row is drawn there", hits)
			}
			if h := hits[0]; h.TurnID != c.turn || h.EntryID != c.turn || h.Kind != itemHeader || !slices.Equal(h.Segments, []SegmentKind{segmentWake}) {
				t.Errorf("hit = %+v, want the %s wake's header", h, c.turn)
			}
		})
	}
}

func TestSearch_AnUnclaimedWorkflowMessageIsFoundOnItsRow(t *testing.T) {
	const note = "The link report is written to build/links.txt."
	entries, drawn := chatOf(openTurn("t-1", 1, prompt("m-1", "publish the docs")).
		add("", "wfmsg-r", marotte.EntryKindSteer, wakeRow(note, "success")))

	hits := searchHits(entries, drawn, "link report")
	if len(hits) != 1 || hits[0].EntryID != "wfmsg-r" || hits[0].Kind != itemSteer {
		t.Errorf("hits = %+v, want the row's own steer segment", hits)
	}
}

func TestSearch_ARepeatCapWakeFindsOnlyTheLabelItDraws(t *testing.T) {
	const note = "A workflow you launched paused: repeat fix-loop reached its iteration limit (6) without meeting its stop condition."
	prev := openTurn("t-1", 1, prompt("m-1", "publish the docs")).text("a1", "Launched.").
		add("", "wfmsg-cap", marotte.EntryKindSteer, wakeRow(note, "info"))
	wake := wakeTurnFixture("t-2", 2, marotte.EntryTurnWake{
		Reason: marotte.WakeReasonStepMessage, Note: marotte.WakeNoteRepeatCap, Loop: "fix-loop", MaxIterations: 6,
		Steers: []marotte.WakeSteer{{ID: "wfmsg-cap", Run: wakeRunID, RunName: "publish-docs", Step: "fix-loop", Severity: "info", Text: note}},
	})
	entries, drawn := chatOf(prev, wake)

	if hits := searchHits(entries, drawn, "stop condition"); len(hits) != 0 {
		t.Errorf("hits = %+v, want none: the header draws the label alone and the row draws no card", hits)
	}
	if hits := searchHits(entries, drawn, "round limit"); len(hits) != 1 || hits[0].EntryID != "t-2" || hits[0].Kind != itemHeader {
		t.Errorf("hits = %+v, want the wake's label", hits)
	}
}
