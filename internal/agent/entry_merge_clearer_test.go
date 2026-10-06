package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// TestSwapMerged_ABindForTheHeadersSessionFilesNoSessionRecord pins clearerAdopts' turn_bind
// arm on the rewrite branch: a bind naming the header's session files no session record; one
// naming another session does.
func TestSwapMerged_ABindForTheHeadersSessionFilesNoSessionRecord(t *testing.T) {
	cases := []struct {
		name         string
		boundSession string
		wantSession  []string
	}{
		{name: "bind names the header's session", boundSession: "sess-1"},
		{name: "bind names another session", boundSession: "sess-0", wantSession: []string{"sess-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwapFixture(t)
			if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
				c.ACPSessionID = "sess-1"
				return true
			}); err != nil {
				t.Fatalf("bind the header's session: %v", err)
			}
			bound := f.promptTurn("first")
			f.append(bound, marotte.EntryKindTurnBind, bound+":bind",
				marotte.EntryTurnBind{KASMessageID: "kas-1", SessionID: tc.boundSession})
			f.append(bound, marotte.EntryKindText, "S-bound", marotte.EntryText{Text: "an answer"})
			f.closeTurn(bound, marotte.TurnOutcomeCompleted, "end_turn")
			orphan := f.promptTurn("second")
			f.append(orphan, marotte.EntryKindText, "S-orphan", marotte.EntryText{Text: "half an answer"})
			f.closeTurn(orphan, marotte.TurnOutcomeInterrupted, marotte.StopReasonUnterminated)
			projected := []translate.ProjectedTurn{projTurn(t, "P9",
				openRow("P9", 0, nil),
				textRow("S-kas", "", "a turn the record never held"),
				closeRow("P9:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
			)}

			changed, err := SwapMerged(t.Context(), &Swap{
				Log: f.log, Header: f.header, Record: f.record(),
				Projected: projected, SessionID: "sess-1", Snapshot: f.snapshot(),
			})
			if err != nil {
				t.Fatalf("SwapMerged: %v", err)
			}
			if !changed {
				t.Fatal("changed = false, so the rewrite branch and insertReconciled never ran")
			}
			var got []string
			for _, e := range f.everythingEntries() {
				if e.Kind != marotte.EntryKindReconciled {
					continue
				}
				var rec marotte.EntryReconciled
				if err := json.Unmarshal(e.Payload, &rec); err != nil {
					t.Fatalf("parse the reconciled of %q: %v", e.ID, err)
				}
				if rec.Session != "" {
					got = append(got, rec.Session)
				}
			}
			if !slices.Equal(got, tc.wantSession) {
				t.Errorf("record bound to %q, header session sess-1: session-form reconciled records = %q, want %q",
					tc.boundSession, got, tc.wantSession)
			}
			if f.log.NeedsReconcile() {
				t.Error("the swap left a reconcile signal standing, so every later resume re-merges this chat")
			}
		})
	}
}
