package server

import (
	"context"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

// The interfaces below are declared at their consumer, each naming only the methods this
// package invokes. Unexported unless the composition root has to name one.

// routeHandler is a component that wires its own routes under a sub-tree of /api/*. This
// package, the router, is its only consumer.
type routeHandler interface {
	RegisterRoutes(mux *http.ServeMux)
}

type chatEngine interface {
	routeHandler

	// Broadcast fans one event out to every connected SSE client.
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
	// Shutdown drains in-flight prompts and closes all bridges, bounded by ctx,
	// and reports which wait ran out of budget.
	Shutdown(ctx context.Context) error
	// Epoch is the SSE hub's current epoch, stamped on the tabs envelope.
	Epoch() string
	// ReconcileSessionSettings carries a write to open chats: it reopens them when a setting a
	// session reads only at open moved, and pushes every live process each live setting it has
	// not confirmed. Call it once the write has landed.
	ReconcileSessionSettings(ctx context.Context)
}

// governanceLocks is the administrator's lock map as this package reads it, to
// refuse a write to a locked kiro-cli setting. *agent.Settings satisfies it.
type governanceLocks interface {
	GovernanceLocks() map[string]marotte.GovernanceLock
}

// kiroDefaultsReader answers what kiro-cli resolves each unset three-state setting to, keyed by
// the marotte setting key. *agent.Settings satisfies it.
type kiroDefaultsReader interface {
	KiroDefaults() map[string]marotte.KiroDefault
}

// SteeringGenerator generates steering files for kiro-cli. *steering.Generator satisfies it.
// Exported because the composition root names it in server.WithSteering.
type SteeringGenerator interface {
	CustomPath() string
}

// AccountUsageProvider fetches account usage (plan, credits, quota) via KAS's
// _kiro/account/getUsage for GET /api/account/usage. Exported for server.WithAccountUsage.
type AccountUsageProvider interface {
	AccountUsage(ctx context.Context) (*marotte.AccountUsage, error)
}

// policyProvider READS kiro-cli's native Cedar policy, backing GET /api/permissions and
// POST /api/permissions/explain. Read-only: policy/check is never called (it can raise a
// consent prompt), and the rule writer is a file write KAS hot-reloads.
type policyProvider interface {
	PolicyList(ctx context.Context, scope string) ([]marotte.PolicyRule, error)
	PolicyExplain(ctx context.Context, req marotte.PolicyExplainRequest) (*marotte.PolicyExplainResult, error)
}

// Separate from the read-only policyProvider because it mutates process state: it recycles the
// sessions whose presets a persisted change invalidated and tells every client.
type policyReloader interface {
	SecurityProfileChanged(ctx context.Context)
}

type utilityPrompter interface {
	UtilityPrompt(ctx context.Context, prompt string, effort marotte.EffortLevel) (string, error)
}
