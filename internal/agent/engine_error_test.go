package agent

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// An engine fault reaches marotte as display_error, then turn_end{error}: the card
// carries the engine's own sentence instead of the outcome's default.
func TestWireTurnEnd_ABrokenTurnCarriesTheEngineSentence(t *testing.T) {
	h, cs, _ := newTestHub()
	_, log := streamingPromptTurn(t, h, "c1", "half an answer")
	log.SetEngineError(marotte.EngineError{Message: "Your connection was interrupted. Please try again in a moment.", ErrorType: "ge"})

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if want := "Your connection was interrupted. Please try again in a moment."; closes[0].FailureReason != want {
		t.Errorf("failure_reason = %q, want the engine's sentence %q", closes[0].FailureReason, want)
	}
}

// A display_error is not a verdict: a recovered retry or a sub-agent fault leaves a clean close clean.
func TestWireTurnEnd_ACleanCloseIgnoresTheLatchedEngineError(t *testing.T) {
	h, cs, _ := newTestHub()
	_, log := streamingPromptTurn(t, h, "c1", "the answer")
	log.SetEngineError(marotte.EngineError{Message: "Sub-agent stalled."})

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeCompleted || closes[0].FailureReason != "" {
		t.Errorf("turn_close = %+v, want completed with no reason", closes)
	}
}

// The context overflow keeps its Compact remedy when the engine's own turn_end is
// the closer that wins.
func TestWireTurnEnd_ALatchedContextOverflowKeepsItsRemedy(t *testing.T) {
	h, cs, _ := newTestHub()
	_, log := streamingPromptTurn(t, h, "c1", "half an answer")
	log.SetEngineError(marotte.EngineError{Message: "Context limit exceeded unexpectedly. Please try again.", ErrorType: "ContextWindowExceededError"})

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].FailureKind != marotte.FailureKindContextLimit {
		t.Errorf("failure_kind = %q, want %q", closes[0].FailureKind, marotte.FailureKindContextLimit)
	}
	if want := "This chat reached the model's context limit. Compact the context, then send the prompt again."; closes[0].FailureReason != want {
		t.Errorf("failure_reason = %q, want %q", closes[0].FailureReason, want)
	}
}

// The model service's byte-cap refusal, which KAS types GenericValidationError, gets the same Compact remedy
// with its own sentence and the request id KAS wrote into the message.
func TestCloseTurn_RequestSizeCapIsAContextLimit(t *testing.T) {
	h, cs, _ := newTestHub()
	_, log := streamingPromptTurn(t, h, "c1", "half an answer")
	log.SetEngineError(marotte.EngineError{
		Message:        "Request exceeds the maximum allowed size (Request ID: req-413)",
		ErrorType:      "GenericValidationError",
		RetryErrorType: "CLIENT_ERROR",
	})

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].FailureKind != marotte.FailureKindContextLimit {
		t.Errorf("failure_kind = %q, want %q", closes[0].FailureKind, marotte.FailureKindContextLimit)
	}
	want := "This chat is larger than the model service accepts in one request, usually because of earlier images. Compact the context, then send the prompt again. (request req-413)"
	if closes[0].FailureReason != want {
		t.Errorf("failure_reason = %q, want %q", closes[0].FailureReason, want)
	}
}

// A model-call-limit stop names its own cause, so an earlier recovered display_error does not replace it.
func TestWireTurnEnd_AModelCallLimitStopOutranksALatchedEngineError(t *testing.T) {
	h, cs, _ := newTestHub()
	_, log := streamingPromptTurn(t, h, "c1", "half an answer")
	log.SetEngineError(marotte.EngineError{Message: "Your connection was interrupted. Please try again in a moment.", ErrorType: "ge"})

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonToolUse)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].FailureKind != marotte.FailureKindModelCallLimit || closes[0].FailureReason != marotte.ModelCallLimitTurnReason {
		t.Errorf("turn_close = {%q %q}, want {%q %q}", closes[0].FailureKind, closes[0].FailureReason,
			marotte.FailureKindModelCallLimit, marotte.ModelCallLimitTurnReason)
	}
}

// The failure closer waits on its reply's position only to follow a queued turn_end; a dead ctx must still close.
func TestAbandonInFlightTurn_ClosesOnACancelledContextWithASeq(t *testing.T) {
	h, cs, _ := newTestHub()
	id, _ := streamingPromptTurn(t, h, "c1", "half an answer")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	h.coord.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, "the prompt failed", "", 7)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].FailureReason != "the prompt failed" {
		t.Errorf("turn_close = %+v, want the failure closer's close", closes)
	}
}

// A prompt failing inside the execution gets a reply with no class, so the awaited result carries the class the
// turn's display_error named, whichever closer wins.
func TestCloseTurn_ResultCarriesTheLatchedEngineClass(t *testing.T) {
	closers := map[string]func(h *Runtime, id string){
		"wire turn_end": func(h *Runtime, _ string) {
			h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)
		},
		"prompt failure": func(h *Runtime, id string) {
			h.coord.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the prompt failed", "", 0)
		},
	}
	for name, closeIt := range closers {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			id, log := streamingPromptTurn(t, h, "c1", "half an answer")
			log.SetEngineError(marotte.EngineError{Message: "Authentication failed. Please sign in again.", ErrorType: "CredentialAbsentError"})

			closeIt(h, id)

			result, err := h.coord.AwaitTurn(t.Context(), "c1", id)
			if err != nil {
				t.Fatalf("AwaitTurn = %v, want the closed turn's result", err)
			}
			if result.EngineErrorType != "CredentialAbsentError" {
				t.Errorf("EngineErrorType = %q, want CredentialAbsentError", result.EngineErrorType)
			}
		})
	}
}
