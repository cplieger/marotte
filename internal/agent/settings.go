package agent

import (
	"context"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// Settings owns the KAS configuration surface (knowledge, hooks, governance, Cedar policy queries) and its
// routes: engine calls, and observer passes for the reads KAS answers only on a resident session.
type Settings struct {
	governance *governanceCache
	engine     *engineOwner
	// observe runs fn on an observer pass (Runtime.observe).
	observe func(ctx context.Context, fn func(ctx context.Context, s engineSession) error) error
	// sessionRoots is the workspace roots a chat session door opens over (Runtime.sessionRoots).
	sessionRoots func(ctx context.Context) []string
	// lifecycle supplies the workspace dir and the process lifetime.
	lifecycle *lifetime
	broadcast func(context.Context, marotte.ServerEvent)
	// onLocksChanged runs when the lock map moves; it reads the locks itself, since two runs can finish in either order.
	onLocksChanged func(context.Context)
	// onAdminResolved runs once, when the administrator rules first become known; publishGovernance calls it inline, so it must not block.
	onAdminResolved func()
	// kiroAccount reports whether a Kiro account, not an API key, signs the engine in now.
	kiroAccount  func() bool
	kiroDefaults kiroDefaultsCache
	adminRefresh adminRefresh
	// seedMu runs one fallback seeding pass at a time, so two views never write or record one model twice.
	seedMu sync.Mutex
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
