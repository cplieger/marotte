package bridge

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// TestBridge_SharedContractSuite runs the shared contract suite's pre-Start assertions against the real Bridge.
func TestBridge_SharedContractSuite(t *testing.T) {
	testsupport.ACPBridgePreStartContractTest(t, func() testsupport.ACPPreStartBridge {
		return New("/nonexistent/kiro", t.TempDir())
	})
}
