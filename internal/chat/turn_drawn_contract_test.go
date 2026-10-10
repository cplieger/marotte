package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The fixture is the contract and static-src/turn-drawn-contract.node.test.ts is the other reader;
// each builds its own rows from the same cases, so a divergence between drawnBy and turnIsDrawn
// fails here or there rather than reaching a reader as a card with no rail row.
const turnDrawnFixture = "testdata/turn_drawn.json"

type drawnCase struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Drawn bool   `json:"drawn"`
	Open  struct {
		Source string               `json:"source"`
		Prompt *marotte.EntryPrompt `json:"prompt"`
	} `json:"open"`
	Body []struct {
		Kind           string `json:"kind"`
		Lane           string `json:"lane"`
		AgentSubtaskID string `json:"agent_subtask_id"`
	} `json:"body"`
}

func TestTurnDrawnContract_ServerHalf(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(turnDrawnFixture))
	if err != nil {
		t.Fatalf("read %s: %v", turnDrawnFixture, err)
	}
	var fx struct {
		Cases []drawnCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse %s: %v", turnDrawnFixture, err)
	}

	var ran, skipped, drawn int
	for _, c := range fx.Cases {
		switch c.Owner {
		case "client":
			// The open-entry clause has no server twin: an open entry never reaches the
			// log, so the case is skipped here and counted below.
			skipped++
			continue
		case "both":
		default:
			t.Fatalf("case %q has owner %q, want both or client", c.Name, c.Owner)
		}
		ran++
		if c.Drawn {
			drawn++
		}
		t.Run(c.Name, func(t *testing.T) {
			f := newLogFixture(t)
			turn := f.openTurn(&TurnSpec{
				Source: marotte.TurnOpenSourceName(c.Open.Source),
				Prompt: c.Open.Prompt,
			})
			for i, e := range c.Body {
				kind := marotte.EntryKind(e.Kind)
				id := string(kind) + "-" + string(rune('a'+i))
				f.append(turn, e.Lane, id, kind, drawnPayload(t, turn, kind, e.AgentSubtaskID))
			}
			rows := f.log.RailRows()
			got := len(rows) == 1
			if got != c.Drawn {
				t.Errorf("RailRows() holds %d rows, want the turn %s: drawn is %v, want %v",
					len(rows), drawnWord(c.Drawn), got, c.Drawn)
			}
		})
	}

	if skipped != 1 {
		t.Errorf("skipped %d client-only cases, want exactly 1 (the open-entry clause)", skipped)
	}
	if ran < 10 || drawn == 0 || drawn == ran {
		t.Errorf("ran %d cases of which %d drawn: the fixture must exercise both verdicts", ran, drawn)
	}
}

func drawnWord(drawn bool) string {
	if drawn {
		return "drawn"
	}
	return "undrawn"
}

// drawnPayload is the smallest payload each kind's case needs: the predicate reads the
// envelope, and the invocation case reads agent_subtask_id off the call. The revert names
// its OWN carrier, which is the one shape the scan's skip rule leaves surviving, so the
// case measures the drawn predicate rather than the surviving view.
func drawnPayload(t *testing.T, turn string, kind marotte.EntryKind, subtask string) any {
	t.Helper()
	switch kind {
	case marotte.EntryKindTurnRevert:
		return marotte.EntryTurnRevert{From: turn, FromN: 1, Through: turn}
	case marotte.EntryKindReconciled:
		return marotte.EntryReconciled{Turn: turn}
	case marotte.EntryKindTurnBind:
		return marotte.EntryTurnBind{KASMessageID: "k"}
	case marotte.EntryKindText:
		return marotte.EntryText{Text: "delegate"}
	case marotte.EntryKindToolCall:
		return marotte.EntryToolCall{ID: "act-1", AgentSubtaskID: subtask}
	case marotte.EntryKindToolResult:
		return marotte.EntryToolResult{}
	case marotte.EntryKindSteerAck:
		return marotte.EntrySteerAck{SteerID: "steer-1", Text: "reading your note"}
	case marotte.EntryKindPlan:
		return marotte.EntryPlan{}
	case marotte.EntryKindTurnClose:
		return marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}
	}
	t.Fatalf("fixture kind %q has no payload here: add it beside the others", kind)
	return nil
}
