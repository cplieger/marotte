package testsupport

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// ACPPreOpenSession is the subject of ACPSessionPreOpenContractTest: the session methods this suite reads.
type ACPPreOpenSession interface {
	NotifCh() <-chan marotte.Notification
	Open() bool
	CurrentMode() string
	Modes() []marotte.SessionMode
	Models() []marotte.SessionModel
}

// ACPSessionPreOpenContractTest verifies what any ACP session handle must satisfy before its door succeeded, without a
// real kiro-cli. Run against the real Session and the fakes. It lives here so no production package imports "testing".
func ACPSessionPreOpenContractTest(t *testing.T, newSession func() ACPPreOpenSession) {
	t.Helper()

	t.Run("NotifCh_non_nil", func(t *testing.T) {
		if newSession().NotifCh() == nil {
			t.Error("NotifCh() returned nil, want non-nil channel")
		}
	})

	t.Run("Open_false_before_the_door", func(t *testing.T) {
		if newSession().Open() {
			t.Error("Open() = true before the door succeeded")
		}
	})

	t.Run("CurrentMode_empty_before_the_door", func(t *testing.T) {
		if got := newSession().CurrentMode(); got != "" {
			t.Errorf("CurrentMode() = %q before the door, want empty", got)
		}
	})

	t.Run("Modes_nil_before_the_door", func(t *testing.T) {
		if got := newSession().Modes(); got != nil {
			t.Errorf("Modes() = %v before the door, want nil", got)
		}
	})

	t.Run("Models_nil_before_the_door", func(t *testing.T) {
		if got := newSession().Models(); got != nil {
			t.Errorf("Models() = %v before the door, want nil", got)
		}
	})
}
