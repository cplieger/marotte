package agent

import (
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestAwaitTurnBound_AnswersWhetherKASBoundThePrompt(t *testing.T) {
	cases := map[string]struct {
		bind, end, want bool
	}{
		"boundWhileRunning": {bind: true, want: true},
		"boundThenEnded":    {bind: true, end: true, want: true},
		"endedUnbound":      {end: true, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			id, log := h.stagePromptTurn(t, "c1")
			defer h.coord.ReleaseTurn("c1", id)
			if tc.bind {
				if _, err := log.TurnBind(t.Context(), marotte.EntryTurnBind{KASMessageID: "kas-1", SessionID: "s-1"}); err != nil {
					t.Fatalf("TurnBind: %v", err)
				}
			}
			if tc.end {
				endTurn(t, h, "c1", id)
			}
			got, err := h.coord.AwaitTurnBound(t.Context(), "c1", id)
			if err != nil || got != tc.want {
				t.Errorf("AwaitTurnBound(bind %v, ended %v) = %v, %v; want %v, nil", tc.bind, tc.end, got, err, tc.want)
			}
		})
	}
}

func TestAwaitTurnBound_ARunningUnboundTurnWaitsForItsContext(t *testing.T) {
	h, _, _ := newTestHub()
	id, _ := h.stagePromptTurn(t, "c1")
	defer h.coord.ReleaseTurn("c1", id)

	if got, err := h.coord.AwaitTurnBound(deadContext(t), "c1", id); got || err == nil {
		t.Errorf("AwaitTurnBound on a running unbound turn with a dead context = %v, %v; want false and the context's error", got, err)
	}
	if _, err := h.coord.AwaitTurnBound(t.Context(), "c1", "t-never-opened"); !errors.Is(err, marotte.ErrNoSuchTurn) {
		t.Errorf("AwaitTurnBound on an unknown turn = %v, want ErrNoSuchTurn", err)
	}
}
