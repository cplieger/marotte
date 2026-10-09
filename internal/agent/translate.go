// ACP → domain event translation: translateACPEvent is the single dispatch point, handlers live in
// translate_*.go. An unhandled `_kiro/*` extension logs at Debug (an unstable namespace).

package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// chatHandler is the notification handler type; a global handler gets an empty chatID.
type chatHandler = func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)

// ignoreAttribution adapts a handler that needs no attribution to sessionUpdateHandler.
func ignoreAttribution(fn func(context.Context, marotte.ChatID, json.RawMessage)) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, _ translate.FrameAttribution) {
		fn(ctx, chatID, raw)
	}
}

// initDispatch builds the method → handler maps once, eagerly: several bridge goroutines read them.
func (rt *Runtime) initDispatch() {
	rt.chatHandlers = map[string]chatHandler{
		marotte.MethodSessionUpdate: rt.handleSessionUpdate,
		// Refused on a short budget for a scheduled run.
		marotte.MethodRequestPermission: rt.runs.permissionWithUnattendedFloor(rt.askReportsStepProgress(rt.turnApprovalExpectsRestores(rt.translator.HandlePermissionRequest))),
		marotte.MethodElicitationCreate: rt.askReportsStepProgress(rt.translator.HandleElicitationCreate),
		// Gated on the _meta.kiro.userInput initialize capability (bridge.go).
		marotte.MethodKiroUserInput: rt.askReportsStepProgress(rt.translator.HandleUserInput),
		// v3 _kiro/* notifications.
		methodV3RateLimit:            rt.translator.HandleRateLimit,
		methodV3CustomAgentNotFound:  rt.translator.HandleAgentNotFound,
		methodV3CustomAgentConfigErr: rt.translator.HandleAgentConfigError,
		methodV3MCPStatus:            rt.translator.HandleMCPStatus,
		methodV3SystemNotify:         rt.translator.HandleSystemNotify,
		// Cedar policy reload or parse error → SSE refetch of GET /api/permissions.
		methodV3PolicyChanged:  rt.translator.HandlePolicyChanged,
		methodV3PolicyError:    rt.translator.HandlePolicyError,
		methodV3CodeReferences: rt.translator.HandleCodeReferences,
		// Live once KAS installs the safety gate (translate/safety.go).
		methodV3SafetyStatusChanged: rt.translator.HandleSafetyStatusChanged,
		methodV3SafetyPropertiesChg: rt.translator.HandleSafetyPropertiesChanged,
		methodV3Governance:          rt.translator.HandleGovernanceState,
		// Wrapped so the run clock (run_bounds.go) sees start/pause/finish; run_start is an agent run's only frame.
		methodWFRunStart:    rt.runs.observeStart,
		methodWFRunComplete: rt.runs.observeComplete,
		// A step's question, its only carrier; also on the run bridge's door ((*Runtime).dispatch).
		methodKiroSessionNotify: rt.runs.handleSessionNotify,
		// An in-process spec write by a spec-mode session (spec_checkpoint.go).
		methodV3SpecPhaseCheckpoint: rt.handleSpecPhaseCheckpoint,
		// configIssues for the /docs steering badges.
		methodV3SteeringDocs: rt.translator.HandleSteeringDocuments,
		// The installed Powers moved; the legacy block and every Powers tab follow.
		methodV3Powers: func(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
			rt.powers.itemsChanged(ctx, msg.Params)
		},
		// A custom agent's own knowledge bases; the Settings list never shows them.
		methodKiroKnowledgeIndexingStarted:   rt.translator.HandleKnowledgeIndexing(marotte.KnowledgeIndexingStarted),
		methodKiroKnowledgeIndexingCompleted: rt.translator.HandleKnowledgeIndexing(marotte.KnowledgeIndexingCompleted),
	}
	for method, kind := range map[string]marotte.RunProgressKind{
		methodWFNodeStart:     marotte.RunProgressNodeStart,
		methodWFNodeComplete:  marotte.RunProgressNodeComplete,
		methodWFNodePaused:    marotte.RunProgressNodePaused,
		methodWFPaused:        marotte.RunProgressPaused,
		methodWFLoopIteration: marotte.RunProgressLoopIteration,
		methodWFWatchPoll:     marotte.RunProgressWatchPoll,
		methodWFStepsQueued:   marotte.RunProgressStepsQueued,
	} {
		rt.chatHandlers[method] = rt.translator.RunProgressHandler(kind)
	}
	// A run-level pause stops the clock; node pauses do not. Two wrappers over different collaborators, ordered:
	// the clock parks before anything can resume.
	rt.chatHandlers[methodWFPaused] = rt.runs.observePaused(
		rt.runs.healPaused(rt.chatHandlers[methodWFPaused]),
	)
	// A completed node is the only honest evidence a pause cleared.
	rt.chatHandlers[methodWFNodeComplete] = rt.runs.healProgress(rt.chatHandlers[methodWFNodeComplete])
	// Recognised and ignored, kept out of the Debug log.
	rt.noopMethods = map[string]struct{}{
		methodV3SessionsChanged:          {},
		methodV3ToolsDidChange:           {},
		methodV3ProgressiveContext:       {},
		methodKiroWorkflowRecipesChanged: {},
	}
	// Eager: several bridge goroutines read it.
	rt.sessUpdateHandlers = map[marotte.ACPUpdateKind]sessionUpdateHandler{
		marotte.ACPUpdateAgentChunk: func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
			rt.translator.HandleAssistantChunk(ctx, chatID, raw, false, attr)
		},
		marotte.ACPUpdateThoughtChunk: func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
			rt.translator.HandleAssistantChunk(ctx, chatID, raw, true, attr)
		},
		marotte.ACPUpdateToolCall:   rt.translator.HandleToolCall,
		marotte.ACPUpdateToolUpdate: rt.translator.HandleToolCallUpdate,
		marotte.ACPUpdatePlan:       rt.translator.HandlePlan,
		marotte.ACPUpdateModeChange: ignoreAttribution(rt.translator.HandleModeUpdate),
		// The metadata channels.
		marotte.ACPUpdateSessionInfo: rt.translator.HandleSessionInfoUpdate,
		marotte.ACPUpdateUsage:       ignoreAttribution(rt.translator.HandleUsageUpdate),
		// The only frame telling marotte that KAS moved the effort level itself.
		marotte.ACPUpdateConfigOption: rt.coord.healEffort(rt.translator.HandleConfigOptionUpdate),
		// The slash-menu catalog; only a chat's own frame feeds it.
		marotte.ACPUpdateAvailableCommands: rt.translator.HandleAvailableCommandsUpdate,
	}
	// A parentless run bridge folds content into the run record and reads step metering; chat-only kinds are
	// dropped, since `run:<id>` has no chat record.
	rt.runStepHandlers = map[marotte.ACPUpdateKind]sessionUpdateHandler{
		marotte.ACPUpdateAgentChunk:   rt.sessUpdateHandlers[marotte.ACPUpdateAgentChunk],
		marotte.ACPUpdateThoughtChunk: rt.sessUpdateHandlers[marotte.ACPUpdateThoughtChunk],
		marotte.ACPUpdateToolCall:     rt.translator.HandleToolCall,
		marotte.ACPUpdateToolUpdate:   rt.translator.HandleToolCallUpdate,
		marotte.ACPUpdateSessionInfo:  rt.translator.HandleStepInfoUpdate,
	}
}

// translateACPEvent is the sole entry point from the forward goroutine. Every branch returns promptly; long work belongs in a goroutine.
func (rt *Runtime) translateACPEvent(chatID marotte.ChatID, msg *marotte.RPCResponse) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()

	// A run bridge's frames take their own door.
	if isRunChat(chatID) {
		rt.dispatch(ctx, chatID, msg)
		return
	}

	if msg.ID != nil && rt.routeInboundRequest(ctx, chatID, msg) {
		return
	}

	// The id test keeps a future request-shaped method in this map from being swallowed.
	if fn, ok := rt.chatHandlers[msg.Method]; ok && msg.ID == nil {
		fn(ctx, chatID, msg)
		return
	}
	// Same reason: one landing in the noop table would return unanswered, and log nothing.
	if _, ok := rt.noopMethods[msg.Method]; ok && msg.ID == nil {
		return
	}
	// An unrecognised request must be answered: KAS's ext-method calls have no timeout, so silence wedges the turn
	// and every later prompt 409s. -32601, not -32603, so a deliberate refusal does not read as our fault.
	if msg.ID != nil {
		slog.Warn("chat bridge: refusing an unexpected peer request",
			"method", msg.Method, "chat_id", chatID, "id", *msg.ID)
		if err := rt.BridgeRespond(ctx, chatID, *msg.ID, nil,
			&marotte.RPCError{
				Code:    marotte.RPCCodeMethodNotFound,
				Message: "unsupported on the chat session: " + msg.Method,
			}); err != nil {
			slog.Error("chat bridge: refusal could not be delivered; the turn may be wedged",
				"method", msg.Method, "chat_id", chatID, "error", err)
		}
		return
	}
	// Debug rather than a silent drop; nothing is owed for a notification.
	if strings.HasPrefix(msg.Method, "_kiro/") {
		slog.Debug("unhandled kiro extension",
			"method", msg.Method, "chat_id", chatID)
	}
}

// routeInboundRequest dispatches an A→C request to its handler family, reporting whether one claimed it. Every arm owes a response.
func (rt *Runtime) routeInboundRequest(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) bool {
	if rt.inbound.handleFSRequest(ctx, chatID, msg) {
		return true
	}
	// Confined execution of KAS's own fs verbs.
	if rt.inbound.handleKiroFSRequest(ctx, chatID, msg) {
		return true
	}
	if rt.inbound.handleKiroClientRequest(ctx, chatID, msg) {
		return true
	}
	// Must be answered: KAS rethrows a store or delete failure into MCP connect.
	if rt.inbound.handleKiroSecretRequest(ctx, chatID, msg) {
		return true
	}
	if strings.HasPrefix(msg.Method, methodTermPrefix) {
		rt.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return true
	}
	// An explicit whitelist for the three request-shaped chat handlers, so double dispatch is unreachable.
	switch msg.Method {
	case marotte.MethodRequestPermission,
		marotte.MethodElicitationCreate,
		marotte.MethodKiroUserInput:
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, chatID, msg)
			return true
		}
	}
	return false
}

// askReportsStepProgress reports a step's ask as progress before the handler, attributed by session id (a
// request has no workflow meta), through the run bridge's key when it arrived on one.
func (rt *Runtime) askReportsStepProgress(inner chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		var params struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(msg.Params, &params) == nil {
			var attr translate.FrameAttribution
			if runID := workflowIDOf(chatID); runID != "" {
				attr = rt.translator.StepAttribution(runID, params.SessionID, nil)
			} else {
				attr = rt.translator.Attribute(chatID, params.SessionID, nil)
			}
			rt.translator.ReportStepProgress(attr)
		}
		inner(ctx, chatID, msg)
	}
}

// turnApprovalExpectsRestores marks a turn approval's session as restoring before the ask reaches
// anyone who could answer it, so no restore can arrive unmarked.
func (rt *Runtime) turnApprovalExpectsRestores(inner chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		var params struct {
			SessionID string `json:"sessionId"`
			Meta      struct {
				Kiro struct {
					Type string `json:"type"`
				} `json:"kiro"`
			} `json:"_meta"`
		}
		if json.Unmarshal(msg.Params, &params) == nil && params.Meta.Kiro.Type == approvalTypeTurn {
			rt.coord.turns.expectRestores(chatID, params.SessionID)
		}
		inner(ctx, chatID, msg)
	}
}

// handleSessionUpdate decodes the `update` envelope and fans out by sessionUpdate subtype with the frame's attribution.
func (rt *Runtime) handleSessionUpdate(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var env struct {
		Params translate.ACPSessionUpdateEnvelope `json:"params"`
	}
	if json.Unmarshal(msg.Params, &env.Params) != nil || env.Params.Update == nil {
		return
	}
	var base translate.ACPSessionUpdateBase
	if json.Unmarshal(env.Params.Update, &base) != nil {
		return
	}

	// The one shared classifier (also deriveSubSession), so a step frame classifies the same through either door.
	attr := rt.translator.Attribute(chatID, env.Params.SessionID, base.Meta.Kiro.Workflow)

	// A replayed frame is history: live handlers would open a phantom turn nothing ends. The gate is per frame, since current-state frames are untagged.
	if base.Meta.Kiro.Replay {
		// A load in flight consumes the frame; otherwise it is dropped.
		if !rt.replay.ingestReplayFrame(chatID, base.Kind, env.Params.Update) {
			slog.Debug("session/update: dropping replayed frame, no load in flight",
				"chat_id", chatID, "kind", base.Kind)
		}
		return
	}

	// After the replay gate: history is not progress.
	rt.translator.ReportStepProgress(attr)

	// Unhandled kinds fall through, user_message_chunk deliberately: marotte persists user messages itself.
	fn, ok := rt.sessionUpdateHandlers()[base.Kind]
	if !ok {
		return
	}
	fn(ctx, chatID, env.Params.Update, attr)
}

// handleRunStepFrame is a parentless run bridge's session/update door: every frame is a step's of the hosted
// run, folded into the run record. Replayed frames are dropped.
func (rt *Runtime) handleRunStepFrame(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var env struct {
		Params translate.ACPSessionUpdateEnvelope `json:"params"`
	}
	if json.Unmarshal(msg.Params, &env.Params) != nil || env.Params.Update == nil {
		return
	}
	var base translate.ACPSessionUpdateBase
	if json.Unmarshal(env.Params.Update, &base) != nil || base.Meta.Kiro.Replay {
		return
	}
	attr := rt.translator.StepAttribution(workflowIDOf(chatID), env.Params.SessionID, base.Meta.Kiro.Workflow)
	rt.translator.ReportStepProgress(attr)
	fn, ok := rt.runStepHandlers[base.Kind]
	if !ok {
		return
	}
	fn(ctx, chatID, env.Params.Update, attr)
}

// sessionUpdateHandler is the common signature for session-update sub-handlers.
type sessionUpdateHandler = func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution)

// sessionUpdateHandlers returns the kind → handler map initDispatch built.
func (rt *Runtime) sessionUpdateHandlers() map[marotte.ACPUpdateKind]sessionUpdateHandler {
	return rt.sessUpdateHandlers
}
