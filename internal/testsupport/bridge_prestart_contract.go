package testsupport

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// ACPPreStartBridge is the subject of ACPBridgePreStartContractTest: the 5 bridge methods
// this suite reads.
type ACPPreStartBridge interface {
	NotifCh() <-chan marotte.Notification
	Stop()
	CurrentMode() string
	Modes() []marotte.SessionMode
	Models() []marotte.SessionModel
}

// ACPBridgePreStartContractTest verifies contracts any ACP bridge must satisfy before Start,
// without a real kiro-cli subprocess. Run against the real Bridge and the fakes. It lives here
// so no production package imports "testing".
func ACPBridgePreStartContractTest(t *testing.T, newBridge func() ACPPreStartBridge) {
	t.Helper()

	t.Run("NotifCh_non_nil", func(t *testing.T) {
		b := newBridge()
		if b.NotifCh() == nil {
			t.Error("NotifCh() returned nil, want non-nil channel")
		}
	})

	t.Run("Stop_idempotent", func(_ *testing.T) {
		b := newBridge()
		// Stop must not panic when called twice.
		b.Stop()
		b.Stop()
	})

	t.Run("CurrentMode_empty_before_Start", func(t *testing.T) {
		b := newBridge()
		if got := b.CurrentMode(); got != "" {
			t.Errorf("CurrentMode() = %q before Start, want empty", got)
		}
	})

	t.Run("Modes_nil_before_Start", func(t *testing.T) {
		b := newBridge()
		if got := b.Modes(); got != nil {
			t.Errorf("Modes() = %v before Start, want nil", got)
		}
	})

	t.Run("Models_nil_before_Start", func(t *testing.T) {
		b := newBridge()
		if got := b.Models(); got != nil {
			t.Errorf("Models() = %v before Start, want nil", got)
		}
	})
}
