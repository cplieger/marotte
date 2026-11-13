package agent

import (
	"github.com/cplieger/marotte/internal/coalesce"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/secretstore"
)

type inbound struct {
	lifetime *lifetime
	coord    *bridgeCoordinator
	bus      *bus
	// specs is the workspace-global spec_changed coalescer every spec-directory write marks.
	specs   *coalesce.Notifier[string]
	secrets *secretstore.Store `wiring:"optional"`
	// signIns is the MCP registry, which owns every consent URL KAS opens.
	signIns consentAnchorer
	// stepRun names the run a step session belongs to (engineRouter.StepRun).
	stepRun func(marotte.SessionID) (string, bool)
}
