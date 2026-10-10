package agent

import (
	"sync"

	"github.com/cplieger/marotte/internal/secretstore"
	"github.com/cplieger/marotte/internal/spec"
)

type inbound struct {
	lifetime *lifetime
	coord    *bridgeCoordinator
	bus      *bus
	// specs is the workspace-global spec_changed coalescer every spec-directory write marks.
	specs   *spec.Notifier
	secrets *secretstore.Store `wiring:"optional"`

	writeHooks   map[string]WriteHook
	writeHooksMu sync.Mutex
}
