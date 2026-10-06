package agent

// A turn opened after the process decided to stop has no closer left, so it reached the wire with no terminal frame.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// OpenTurn refuses a dead context before it appends, so the log is untouched and the registry holds nothing.
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
			id, err := h.coord.OpenTurn(ctx, "c1", command.TurnOpen{Source: tc.source, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hi"}})
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

// StartTurn answers false for a dead ctx after a successful OpenTurn and the turn stays open for the caller's
// turn end rule.
func TestStartTurn_RefusesADeadContextAfterTheOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")

	id, err := h.coord.OpenTurn(t.Context(), "c1", command.TurnOpen{Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hi"}})
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

// A prompt parked in its bridge spawn when Shutdown lands still reaches a terminal frame. Unlike
// TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn, which passes through a closer race, this pins that no turn
// opens and the prompt takes its zero-epoch branch.
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

	if open := h.coord.turns.openTurnIDs("c1"); len(open) != 0 {
		t.Errorf("%d turn(s) still open after shutdown, want none", len(open))
	}
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(marotte.EventError)); missing != nil {
		t.Errorf("events = %v, want the prompt's terminal error frame", types)
	}
}
