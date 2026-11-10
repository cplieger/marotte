package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The empty-turn re-send keeps today's guards: a retry-marked prompt's close inserts one retry entry only when the
// turn ended on the wire with end_turn and nothing emitted, no later turn opened and no stop was asked for after it.
func TestRunList_AnEmptyTurnIsResentOnlyWhenEveryClauseHolds(t *testing.T) {
	firing := marotte.TurnResult{Stop: marotte.StopReasonEndTurn, EmittedNothing: true, WireEnded: true}
	cases := []struct {
		name    string
		result  marotte.TurnResult
		nextSeq uint64
		stopSeq uint64
		marked  bool
		want    bool
	}{
		{name: "every clause holds", result: firing, marked: true, want: true},
		{name: "a verb kiro-cli answers itself is never re-sent", result: firing},
		{name: "closed locally", result: marotte.TurnResult{Stop: marotte.StopReasonEndTurn, EmittedNothing: true}, marked: true},
		{name: "it emitted content", result: marotte.TurnResult{Stop: marotte.StopReasonEndTurn, WireEnded: true}, marked: true},
		{
			name:   "another stop reason",
			result: marotte.TurnResult{Stop: marotte.StopReasonRefusal, EmittedNothing: true, WireEnded: true}, marked: true,
		},
		{name: "a later turn opened", result: firing, nextSeq: 8, marked: true},
		{name: "a stop was asked for after it opened", result: firing, stopSeq: 7, marked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := &activeTurn{ID: "t-1", Chat: "c1", Seq: 7}
			c := (&bridgeCoordinator{}).listCloseOf(t.Context(), at, closerWireEnd,
				&closeFacts{outcome: marotte.TurnOutcomeEmpty}, tc.result, false)
			l := &runList{}
			l.insert(0, &runEntry{
				id: "m-1", kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, state: marotte.RunRunning,
				turnID: "t-1", row: &marotte.QueuedPrompt{ID: "m-1", Text: "tidy"}, retry: tc.marked,
			})
			l.insert(1, &runEntry{
				id: "m-q1", kind: marotte.RunKindRow, origin: marotte.RunFromQueue, state: marotte.RunPending,
				row: &marotte.QueuedPrompt{ID: "m-q1", Text: "queued"},
			})
			c.turnID, c.seq = "t-1", 7
			nextSeq := max(tc.nextSeq, 7)
			l.turnClosed(*c, &orderFacts{nextSeq: nextSeq, stopSeq: tc.stopSeq})

			var retries []string
			for _, e := range l.entries {
				if e.kind == marotte.RunKindRetry {
					retries = append(retries, e.id)
				}
			}
			if got := len(retries) == 1; got != tc.want {
				t.Fatalf("retry entries = %v, want one: %v", retries, tc.want)
			}
			if tc.want && (l.entries[len(l.entries)-1].kind != marotte.RunKindRetry || l.entries[1].id != "m-q1") {
				t.Errorf("list = %v, want the retry after m-q1", entryIDs(l))
			}
			if !tc.want && l.entries[1].state != marotte.RunPaused {
				t.Errorf("m-q1 is %s after a close that re-sends nothing, want paused (the close is not clean)", l.entries[1].state)
			}
		})
	}
}

func entryIDs(l *runList) []string {
	out := make([]string, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e.id)
	}
	return out
}
