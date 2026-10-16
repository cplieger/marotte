package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// initWorkspaceHandlers builds the one table of workspace-global notifications. KAS sends each to every resident
// session, so a method handled on one lane and dropped on another would be lost or not by which session KAS
// addressed; none of these methods may sit in chatHandlers or noopMethods.
func (rt *Runtime) initWorkspaceHandlers() {
	rt.workspaceHandlers = map[string]chatHandler{
		methodKiroHooksDidChange: func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
			rt.config.broadcastHooksChanged()
		},
		methodV3Powers: func(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
			rt.powers.itemsChanged(ctx, msg.Params)
		},
		methodKiroWorkflowRecipesChanged: func(ctx context.Context, _ marotte.ChatID, _ *marotte.RPCResponse) {
			rt.Broadcast(ctx, marotte.NewEvent(marotte.EventRecipesChanged, "", marotte.RecipesChangedPayload{}))
		},
		methodV3Governance: func(_ context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
			rt.config.applyGovernance(msg.Params)
		},
		methodKiroConfigurationState: func(_ context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
			rt.config.cacheConfigurationState(msg.Params)
			rt.standing.refetchSoon()
		},
		methodV3PolicyChanged: rt.translator.HandlePolicyChanged,
		methodV3PolicyError:   rt.translator.HandlePolicyError,
		methodV3SteeringDocs:  rt.translator.HandleSteeringDocuments,
		// A run lane's run:<id> keeps naming the run in the toast.
		methodV3SystemNotify: rt.translator.HandleSystemNotify,
	}
}

// dispatchWorkspace handles a workspace-global notification wherever it landed and reports whether method is one.
// Every lane's consumer calls it once per frame, before its own handlers.
func (rt *Runtime) dispatchWorkspace(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) (claimed bool) {
	if msg.ID != nil {
		return false
	}
	fn, ok := rt.workspaceHandlers[msg.Method]
	if !ok {
		return false
	}
	fn(ctx, chatID, msg)
	return true
}
