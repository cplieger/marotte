package agent

import (
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// signInGrace is how long renewals must have failed before an unreachable identity provider reads failing.
var signInGrace = 2 * time.Minute

// SignInHealth is the Kiro sign-in's current state.
func (rt *Runtime) SignInHealth() marotte.SignInHealth {
	return marotte.SignInHealth{State: marotte.SignInOK}
}

// SignInRefused reports whether Kiro refused a credential marotte served and no turn has been accepted since.
func (rt *Runtime) SignInRefused() bool { return false }

// NoteEngineRefusal records a turn the engine failed on a refused credential while the identity provider answers.
func (rt *Runtime) NoteEngineRefusal() {}

// NoteEngineAccepted records a turn the engine accepted.
func (rt *Runtime) NoteEngineAccepted() {}
