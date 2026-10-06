package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// Settings owns the KAS configuration surface (knowledge, hooks, governance, Cedar policy queries) and its
// routes, all over the utility bridge. utility is a thunk: the runtime is built under a sync.Once whose
// hooks call back into agent surfaces.
type Settings struct {
	// governance caches KAS's last governance state, written by SetGovernance and the utility warm path.
	governance *governanceCache
	// utility is the bridgeless runtime every call here goes through.
	utility func() *utilityRuntime
	// lifecycle supplies the workspace dir and the process lifetime.
	lifecycle *lifetime
	// broadcast publishes hooks_changed and governance_state.
	broadcast func(context.Context, marotte.ServerEvent)
	// onLocksChanged runs when the lock map moves; it reads the locks itself, since two runs can finish in either order.
	onLocksChanged func(context.Context)
	// onAdminResolved runs once, when the administrator rules first become known; publishGovernance calls it inline, so it must not block.
	onAdminResolved func()
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
