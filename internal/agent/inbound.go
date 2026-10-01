package agent

import (
	"github.com/cplieger/marotte/internal/ignore"
	"github.com/cplieger/marotte/internal/secretstore"
)

// inbound answers the requests the agent makes of marotte over ACP.
type inbound struct {
	lifetime *lifetime
	coord    *BridgeCoordinator
	chats    runChatReader
	ignore   *ignore.Matcher `wiring:"optional"`
	bus      *bus
	secrets  *secretstore.Store `wiring:"optional"`
}
