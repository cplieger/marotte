package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// The fake session meets the contract the real one does before its door.
func TestFakeBridge_SharedContract(t *testing.T) {
	testsupport.ACPSessionPreOpenContractTest(t, func() testsupport.ACPPreOpenSession {
		return &fakeBridge{notifCh: make(chan marotte.Notification, 16)}
	})
}

func TestTestChatStore_Contract(t *testing.T) {
	testsupport.ChatStoreContractTest(t, func(t *testing.T) testsupport.ChatStoreContract {
		t.Helper()
		return newTestChatStore()
	})
}

func TestFakeMCPConfig_Contract(t *testing.T) {
	testsupport.MCPConfigContractTest(t, func(t *testing.T) testsupport.MCPNameSets {
		t.Helper()
		return &fakeMCPConfig{
			enabled:    map[string]struct{}{},
			configured: map[string]struct{}{},
		}
	})
}
