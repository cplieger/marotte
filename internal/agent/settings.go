package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// Settings owns the KAS configuration surface (knowledge, hooks, governance, Cedar policy queries) and its
// routes: engine calls, and observer passes for the reads KAS answers only on a resident session.
type Settings struct {
	governance *governanceCache
	engine     *engineOwner
	// observe runs fn on an observer pass (Runtime.observe).
	observe func(ctx context.Context, fn func(ctx context.Context, s engineSession) error) error
	// lifecycle supplies the workspace dir and the process lifetime.
	lifecycle *lifetime
	broadcast func(context.Context, marotte.ServerEvent)
	// onLocksChanged runs when the lock map moves; it reads the locks itself, since two runs can finish in either order.
	onLocksChanged func(context.Context)
	// onAdminResolved runs once, when the administrator rules first become known; publishGovernance calls it inline, so it must not block.
	onAdminResolved func()
	kiroDefaults    kiroDefaultsCache
	adminRefresh    adminRefresh
}

func newSettings(lc *lifetime, broadcast func(context.Context, marotte.ServerEvent)) *Settings {
	return &Settings{
		governance: newGovernanceCache(),
		lifecycle:  lc,
		broadcast:  broadcast,
	}
}

// Config exposes the settings surface to the composition root as the server's policyProvider.
func (rt *Runtime) Config() *Settings { return rt.config }
