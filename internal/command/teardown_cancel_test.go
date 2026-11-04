package command

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A teardown that meets a session still opening, closing or closed has no turn on the wire: it writes no
// session/cancel the session would refuse anyway, logs no cancel failure, and still answers the chat's asks.
func TestCloseChatTeardown_ASessionNotOpenIsNothingToCancel(t *testing.T) {
	for name, refusal := range map[string]error{
		"opening": marotte.ErrSessionOpening,
		"closing": marotte.ErrSessionClosing,
		"closed":  marotte.ErrSessionClosed,
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			b := &cancelBridge{notifyErr: refusal}
			d := newCancelDeps(b)

			closeChatTeardown(t.Context(), d, d, d, "c1")

			if !slices.Equal(d.scopes, []CancelScope{ScopeChatAndRuns}) {
				t.Errorf("permission scopes = %v, want [ScopeChatAndRuns]: the asks are answered whatever the session's state", d.scopes)
			}
			if strings.Contains(logs.String(), "close: turn cancel failed") {
				t.Errorf("a session with nothing on the wire logged a cancel failure: %s", logs.String())
			}
			if len(d.closed) != 1 {
				t.Errorf("closed state for %v, want exactly [c1]", d.closed)
			}
		})
	}
}

// A real cancel failure still warns: only a session with no turn on the wire is quiet.
func TestCloseChatTeardown_AnUndeliveredCancelStillWarns(t *testing.T) {
	logs := captureLogs(t)
	b := &cancelBridge{notifyErr: ErrCancelNotDelivered}
	d := newCancelDeps(b)

	closeChatTeardown(t.Context(), d, d, d, "c1")

	if !strings.Contains(logs.String(), "close: turn cancel failed") {
		t.Errorf("an undelivered cancel logged no warning: %s", logs.String())
	}
}

// A stop pressed while the chat's session door is still answering (session/new or session/load unanswered)
// finds a session that refuses the call with ErrSessionOpening: nothing of the chat is on the wire yet, so the
// stop is not a failure and arms its grace against the admitted prompt, which the door hands on to.
func TestCancel_DuringTheSessionDoorArmsTheStop(t *testing.T) {
	logs := captureLogs(t)
	b := &cancelBridge{notifyErr: marotte.ErrSessionOpening}
	d := newCancelDeps(b)

	if err := runCancel(t, t.Context(), d); err != nil {
		t.Fatalf("cmdCancel: %v", err)
	}
	if !slices.Contains(d.stopSteps, "arm") {
		t.Errorf("stop steps = %v, want the grace armed against the admitted prompt", d.stopSteps)
	}
	if strings.Contains(logs.String(), "cancel failed") {
		t.Errorf("a stop during the session door logged a failure: %s", logs.String())
	}
}
