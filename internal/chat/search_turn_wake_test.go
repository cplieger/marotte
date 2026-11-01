package chat

import (
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
		Reason: marotte.WakeReasonRunFinished, Run: wakeRunID, RunLabel: "publish-docs", RunStatus: marotte.RunStatusCompleted,
	})
	entries, drawn := chatOf(prev, wake, finished)

	cases := []struct {
		query, turn string
	}{
		{"old-domain links", "t-2"},
		{"draft asks you", "t-2"},
		{"completed", "t-3"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			hits := searchHits(entries, drawn, c.query)
			if len(hits) != 1 {
				t.Fatalf("hits = %+v, want one, on the wake: its row is drawn there", hits)
			}
			if h := hits[0]; h.TurnID != c.turn || h.EntryID != c.turn+":wake" || h.SegmentKind != SegmentWake {
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
	if len(hits) != 1 || hits[0].EntryID != "wfmsg-r" || hits[0].SegmentKind != SegmentSteer {
		t.Errorf("hits = %+v, want the row's own steer segment", hits)
	}
}
