package bridge

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// TestSession_SharedContractSuite runs the shared contract suite's pre-open assertions against the real Session, as
// a door leaves it registered and not yet answered.
func TestSession_SharedContractSuite(t *testing.T) {
	testsupport.ACPSessionPreOpenContractTest(t, func() testsupport.ACPPreOpenSession {
		e := NewEngine("/nonexistent/kiro", t.TempDir())
		s, err := e.newSessionHandle(t.Context(), "sess_contract", &marotte.SessionOpts{})
		if err != nil {
			t.Fatalf("newSessionHandle: %v", err)
		}
		return s
	})
}
