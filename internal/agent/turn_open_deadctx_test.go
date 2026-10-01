package agent

// OpenTurn's and StartTurn's refusals on an already-dead context, and the shutdown
// window that made them load-bearing: a turn opened once the process has decided to
// stop has no closer left, so it reached the wire with no terminal frame.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A turn is never opened on a context that is already dead, whatever the chat's
// state: OpenTurn refuses BEFORE it appends, so the log stays byte-identical and the
// registry holds nothing. Asserted per source.
func TestOpenTurn_RefusesAnAlreadyDeadContext(t *testing.T) {
	cases := []struct {
		name   string
		source marotte.TurnOpenSource
	}{
		{name: "prompt", source: marotte.TurnSourcePrompt},
		{name: "empty_retry", source: marotte.TurnSourceEmptyRetry},
		{name: "local_shell", source: marotte.TurnSourceLocalShell},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			seedChat(t, cs, "c1")

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			id, err := h.coord.OpenTurn(ctx, "c1", tc.source, &marotte.EntryPrompt{ID: "m-1", Text: "hi"}, nil)
			if err == nil || id != "" {
				t.Errorf("OpenTurn(cancelled ctx, %v) = (%q, %v), want (\"\", ctx error): a dead context opens nothing", tc.source, id, err)
			}
			if got := h.coord.turns.openTurnIDs("c1"); len(got) != 0 {
				t.Errorf("OpenTurn(cancelled ctx, %v) left %d turn(s) open on the chat", tc.source, len(got))
			}
			if entries, err := cs.All(t.Context(), "c1"); err != nil || len(entries) != 0 {
				t.Errorf("the log holds %d entries (err %v) after a refused open, want none", len(entries), err)
			}
		})
	}
}

// StartTurn answers false for a dead ctx AFTER a successful OpenTurn, and the turn
// stays open: the caller runs the turn end rule on it rather than leaving it to a
// closer that no longer has a bridge behind it.
func TestStartTurn_RefusesADeadContextAfterTheOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")

	id, err := h.coord.OpenTurn(t.Context(), "c1", marotte.TurnSourcePrompt, &marotte.EntryPrompt{ID: "m-1", Text: "hi"}, nil)
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h.coord.StartTurn(ctx, "c1", id) {
		t.Errorf("StartTurn(cancelled ctx, %q) = true, want false", id)
	}
	if h.coord.StartTurn(t.Context(), "c1", "t-never-opened") {
		t.Error("StartTurn(live ctx, unknown id) = true, want false: the registry holds no such turn")
	}
	if got := h.coord.turns.openTurnIDs("c1"); len(got) != 1 || got[0].ID != id {
		t.Errorf("open turns after the refused start = %v, want the opened turn %q alone", got, id)
	}
}

// The shutdown window this closes end to end: a prompt parked in its bridge spawn when
// Shutdown lands must still reach a TERMINAL frame. Distinct from
// TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn, which asserts the same outcome
// but reaches it through whichever closer wins a race — so it passes most of the time
// with the defect present. This one pins the mechanism: no turn opens at all, so the
// prompt takes its own zero-epoch branch and the error frame is not a race's byproduct.
func TestPromptTurn_ShutdownBeforeTheTurnOpensStartsNoTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)
	defer close(gate)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}

	// No turn was ever minted, which is what makes the terminal frame below deterministic
	// rather than a closer race's byproduct.
	if open := h.coord.turns.openTurnIDs("c1"); len(open) != 0 {
		t.Errorf("%d turn(s) still open after shutdown, want none", len(open))
	}
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(marotte.EventError)); missing != nil {
		t.Errorf("events = %v, want the prompt's terminal error frame", types)
	}
}
