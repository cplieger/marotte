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

// chatEngine is the bridge/SSE hub as this package uses it (/api/events, /api/command, the
// settings broadcast, the shutdown drain). *agent.Runtime satisfies it.
type chatEngine interface {
	routeHandler

	// Broadcast fans one event out to every connected SSE client.
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
	// Shutdown drains in-flight prompts and closes all bridges, bounded by ctx,
	// and reports which wait ran out of budget.
	Shutdown(ctx context.Context) error
	// Epoch is the SSE hub's current epoch, stamped on the tabs envelope.
	Epoch() string
	// PushAgentIgnoreFiles tells every live bridge which ignore files kiro-cli should enforce;
	// kiro-cli scopes the list per connection, so an open chat learns of an edit no other way.
	PushAgentIgnoreFiles(ctx context.Context)
	// PushTerminalSettings sends the shell-tool default timeout to every live
	// bridge; kiro-cli holds it per process.
	PushTerminalSettings(ctx context.Context)
	// PushContentCollection sets the resolved content-collection value on every
	// live bridge; kiro-cli holds it per process and persists none of it.
	PushContentCollection(ctx context.Context)
}

// governanceLocks is the administrator's lock map as this package reads it, to
// refuse a write to a locked kiro-cli setting. *agent.Settings satisfies it.
type governanceLocks interface {
	GovernanceLocks() map[string]marotte.GovernanceLock
}

// pushService is the push surface this package serves: the subscription endpoints and the
// toggles a PATCH /api/settings changed. *push.Service satisfies it.
type pushService interface {
	routeHandler

	// SetPreferences replaces the per-kind notification toggles.
	SetPreferences(prefs map[marotte.PushKind]bool)
}

// SteeringGenerator generates steering files for kiro-cli. *steering.Generator satisfies it.
// Exported because the composition root names it in server.WithSteering.
type SteeringGenerator interface {
	Generate(ctx context.Context)
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

// policyReloader recycles the utility session whose policy presets a profile change
// invalidated. Separate from the read-only policyProvider because it mutates process state.
// A chat picks up a new profile when its session next starts or loads.
type policyReloader interface {
	RestartUtilitySession()
}

// mcpRenderer re-renders KAS's MCP config file, so the MCP wait setting reaches
// the file KAS reads. *mcp.Store satisfies it. It carries no argument because the
// store reads the persisted setting itself.
type mcpRenderer interface {
	RenderKASConfig(ctx context.Context) error
}

// utilityPrompter is AI-backed text generation for explain-an-error and explain-a-diff.
// *agent.Runtime satisfies it over the utility bridge.
type utilityPrompter interface {
	UtilityPrompt(ctx context.Context, prompt string, effort marotte.EffortLevel) (string, error)
}
