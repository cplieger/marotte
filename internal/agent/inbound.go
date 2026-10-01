package agent

import (
	"github.com/cplieger/marotte/internal/secretstore"
	"github.com/cplieger/marotte/internal/spec"
)

// inbound answers the requests the agent makes of marotte over ACP.
type inbound struct {
	lifetime *lifetime
	coord    *BridgeCoordinator
	chats    runChatReader
	bus      *bus
	// specs is the workspace-global spec_changed coalescer every write and
	// delete under a spec directory marks.
	specs   *spec.Notifier
	secrets *secretstore.Store `wiring:"optional"`
}
