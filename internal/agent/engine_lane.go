package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// attachEngineLane starts generation gen's engine-lane consumer before Start writes initialize: KAS asks for the
// sign-in around initialize, and an unanswered ask holds the next session door. It lives as long as the engine, which
// Shutdown stops before it joins the lanes.
func (rt *Runtime) attachEngineLane(h engineHandle, gen uint64) {
	rt.lifecycle.goLaneUnlessDraining(func() {
		lane := laneRef{gen: gen}
		for n := range h.EngineNotifCh() {
			rt.engineFrame(h, n.Msg)
			rt.runs.mutations.observeFrame(lane, n.Seq, frameWorkflow(n.Msg, rt.router.StepRun))
			rt.runs.stepReplays.settleConsumed(drainPoint{gen: gen, seq: n.Seq}, false)
		}
		rt.runs.stepReplays.settleConsumed(drainPoint{gen: gen}, true)
	})
}

// engineFrame handles one engine-lane frame. Every request is answered exactly once. A row that does I/O or waits
// runs on a tracked goroutine, so none blocks the consumer, but for a sign-in ask once Shutdown has begun, answered
// in place after a vault lookup drainAnswerBound bounds.
func (rt *Runtime) engineFrame(h engineHandle, msg *marotte.RPCResponse) {
	if msg == nil {
		return
	}
	if msg.ID != nil && msg.Method != "" {
		rt.engineRequest(h, msg)
		return
	}
	rt.engineNotification(msg)
}

// engineRequest answers a request no session lane took: a connection-level one, or one naming a session marotte
// does not hold.
func (rt *Runtime) engineRequest(h engineHandle, msg *marotte.RPCResponse) {
	if rt.answerConnectionRequest(h, msg) {
		return
	}
	if wf, ok := rt.router.StepRun(sessionIDOfParams(msg.Params)); ok {
		if _, leased := rt.runs.lease(wf); leased {
			rt.lifecycle.inflight.Go(func() { rt.heldStepRequest(h, wf, msg) })
			return
		}
	}
	rt.answerUnknownSession(h, msg)
}

// answerConnectionRequest answers the rows any session's lane shares with the engine lane (the declared host
// callback, secrets, the shell type, an external URL), reporting whether msg was one.
func (rt *Runtime) answerConnectionRequest(origin acpResponder, msg *marotte.RPCResponse) bool {
	if answer, ok := rt.signin.hostCallback(msg.Method); ok {
		id := *msg.ID
		if !rt.lifecycle.goUnlessDraining(func() { rt.answerHostCallback(origin, id, answer) }) {
			rt.answerDrainingHostCallback(origin, id, answer)
		}
		return true
	}
	switch msg.Method {
	case methodKiroSecretGet, methodKiroSecretStore, methodKiroSecretDelete, methodKiroOpenExternalURL:
		rt.answerUnlessDraining(origin, msg, func() {
			ctx, cancel := rt.lifecycle.derivedContext()
			defer cancel()
			if !rt.inbound.handleKiroSecretRequest(ctx, "", origin, msg) {
				rt.inbound.handleKiroClientRequest(ctx, "", origin, msg)
			}
		})
		return true
	case methodKiroShellType:
		_ = origin.Respond(rt.lifecycle.shutdownCtx, *msg.ID, kiroShellTypeResult(), nil)
		return true
	}
	return false
}

// answerUnlessDraining runs answer, which answers msg, on inflight; once Shutdown has begun msg is answered at once
// as an unknown session's. An observer pass's or a host relay's frame pump calls it unjoined by Shutdown, so an
// ungated inflight.Go could race Shutdown's Wait.
func (rt *Runtime) answerUnlessDraining(origin acpResponder, msg *marotte.RPCResponse, answer func()) {
	if !rt.lifecycle.goUnlessDraining(answer) {
		rt.answerUnknownSession(origin, msg)
	}
}

// answerHostCallback answers one host callback; the vault bounds its own answer (signin.Vault.Answer). A refusal
// waits in Runs.pauseBeforeRefusal and, when the gate is open, is asked once more: a refusal while the gate stays
// open has no run pause to wait for.
func (rt *Runtime) answerHostCallback(
	origin acpResponder, id int64, answer func(context.Context) (json.RawMessage, *marotte.RPCError),
) {
	ctx := rt.lifecycle.shutdownCtx
	result, rpcErr := answer(ctx)
	for rpcErr != nil && rt.runs.pauseBeforeRefusal(ctx) {
		if result, rpcErr = answer(ctx); rpcErr != nil && rt.runs.gateOpen() {
			break
		}
	}
	respondHostCallback(ctx, origin, id, result, rpcErr)
}

// drainAnswerBound bounds the vault lookup of a host callback answered once Shutdown has begun, on the frame pump's
// own goroutine: a served credential is immediate, and a renewal is cut short, answered as the vault's unreachable
// refusal, rather than holding the pump through the engine's stop. The answer's write has the engine's own bound.
const drainAnswerBound = 2 * time.Second

// answerDrainingHostCallback answers a host callback once Shutdown has begun with the vault's own answer, waiting for
// no pause: an unknown session's refusal would fail the asking step for good (run_gate.go's var block), where a served
// credential lets the engine's stop leave the step to the next boot's heal.
func (rt *Runtime) answerDrainingHostCallback(
	origin acpResponder, id int64, answer func(context.Context) (json.RawMessage, *marotte.RPCError),
) {
	ctx, cancel := context.WithTimeout(rt.lifecycle.shutdownCtx, drainAnswerBound)
	result, rpcErr := answer(ctx)
	cancel()
	respondHostCallback(rt.lifecycle.shutdownCtx, origin, id, result, rpcErr)
}

func respondHostCallback(ctx context.Context, origin acpResponder, id int64, result json.RawMessage, rpcErr *marotte.RPCError) {
	var err error
	if rpcErr != nil {
		err = rpcErr
	}
	if wErr := origin.Respond(ctx, id, result, err); wErr != nil {
		slog.Warn("engine: the sign-in answer could not be written", "error", wErr)
	}
}

// heldStepRequest makes a resumed run's owner resident, then hands it the request its step sent before the owner
// was, with the owner as origin; a failed residency answers it as an unknown session's.
func (rt *Runtime) heldStepRequest(h engineHandle, workflowID string, msg *marotte.RPCResponse) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()
	host, err := rt.runs.ensureResident(ctx, workflowID)
	if err != nil {
		slog.Warn("engine: a resumed run's launching session could not be made resident to answer its step",
			"workflow_id", workflowID, "method", msg.Method, "error", err)
		rt.answerUnknownSession(h, msg)
		return
	}
	rt.translateACPEvent(host.chatID, host.sb.current(), msg)
}

// answerUnknownSession is the one answer for a request whose session marotte does not hold. A permission is answered
// cancelled, never an error: KAS fails open on a thrown permission.
func (rt *Runtime) answerUnknownSession(origin acpResponder, msg *marotte.RPCResponse) {
	ctx := rt.lifecycle.shutdownCtx
	id := *msg.ID
	if msg.Method == marotte.MethodRequestPermission {
		slog.Warn("engine: refusing a permission request for a session marotte does not hold",
			"session_id", logsafe.Field(string(sessionIDOfParams(msg.Params))))
		_ = origin.Respond(ctx, id, marotte.PermissionOutcomeCancelled(), nil)
		return
	}
	slog.Warn("engine: refusing a request for a session marotte does not hold", "method", logsafe.Field(msg.Method),
		"session_id", logsafe.Field(string(sessionIDOfParams(msg.Params))))
	_ = origin.Respond(ctx, id, nil, &marotte.RPCError{
		Code: marotte.RPCCodeMethodNotFound, Message: "no marotte session takes this request: " + msg.Method,
	})
}

// engineNotification dispatches a notification no session lane took: workspace state, agent load errors, knowledge
// indexing, a step replay's frames, and the lifecycle and step frames of a run whose owner is not resident.
func (rt *Runtime) engineNotification(msg *marotte.RPCResponse) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()
	if rt.dispatchWorkspace(ctx, "", msg) {
		return
	}
	switch msg.Method {
	case methodV3CustomAgentConfigErr, methodV3CustomAgentNotFound:
		rt.standing.noteAgentFrame(msg)
		return
	case methodKiroKnowledgeIndexingStarted, methodKiroKnowledgeIndexingCompleted, methodKiroSessionNotify:
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		return
	case marotte.MethodSessionUpdate:
		rt.engineSessionUpdate(ctx, msg)
		return
	}
	if strings.HasPrefix(msg.Method, workflowMethodPrefix) {
		rt.dispatch(ctx, "", nil, msg)
		return
	}
	if _, ok := rt.noopMethods[msg.Method]; ok {
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/") {
		slog.Debug("engine lane: unhandled kiro extension", "method", msg.Method)
	}
}

// engineSessionUpdate takes a step's session/update: a replay frame to its transcript read, a live one to its run's
// record when the run's owner is not resident.
func (rt *Runtime) engineSessionUpdate(ctx context.Context, msg *marotte.RPCResponse) {
	var env translate.ACPSessionUpdateEnvelope
	if json.Unmarshal(msg.Params, &env) != nil || env.Update == nil {
		return
	}
	var base translate.ACPSessionUpdateBase
	if json.Unmarshal(env.Update, &base) != nil {
		return
	}
	if base.Meta.Kiro.Replay {
		if !rt.runs.stepReplays.ingest(env.SessionID, base.Kind, env.Update) {
			slog.Debug("engine lane: dropping a replayed frame no read is open for", "session_id", logsafe.Field(env.SessionID))
		}
		return
	}
	if wf, ok := rt.router.StepRun(marotte.SessionID(env.SessionID)); ok {
		rt.handleRunStepFrame(ctx, runChatID(wf), msg)
	}
}

// answerDetached answers each request a session lane held when it overflowed, exactly once, as a request for a
// session marotte no longer holds would be answered.
func (rt *Runtime) answerDetached(h engineHandle, d bridge.DetachedSession) {
	for _, req := range d.Requests {
		if req.Method == marotte.MethodRequestPermission {
			_ = h.Respond(rt.lifecycle.shutdownCtx, *req.ID, marotte.PermissionOutcomeCancelled(), nil)
			continue
		}
		_ = h.Respond(rt.lifecycle.shutdownCtx, *req.ID, nil, &marotte.RPCError{
			Code:    marotte.RPCCodeInternal,
			Message: "marotte dropped this chat's session because it stopped reading; send the message again",
		})
	}
	slog.Error("a chat stopped reading its frames; its session was closed",
		"session_id", d.SessionID, "requests_answered", len(d.Requests), "notifications_dropped", d.DroppedNotifications)
}

// observerForward consumes an observer pass's frames: its requests as the engine lane answers an unknown session's
// (it runs no tools), its workspace notifications through the one table, and its own command list as the slash
// menu's seed.
func (rt *Runtime) observerForward(s engineSession) {
	go func() {
		ctx, cancel := rt.lifecycle.derivedContext()
		defer cancel()
		for n := range s.NotifCh() {
			msg := n.Msg
			switch {
			case msg == nil:
			case msg.ID != nil && msg.Method != "":
				if !rt.answerConnectionRequest(s, msg) {
					rt.answerUnknownSession(s, msg)
				}
			case rt.dispatchWorkspace(ctx, "", msg):
			case msg.Method == marotte.MethodSessionUpdate:
				rt.observerUpdate(msg)
			}
		}
	}()
}

func (rt *Runtime) observerUpdate(msg *marotte.RPCResponse) {
	var env translate.ACPSessionUpdateEnvelope
	if json.Unmarshal(msg.Params, &env) != nil || env.Update == nil {
		return
	}
	var base translate.ACPSessionUpdateBase
	if json.Unmarshal(env.Update, &base) == nil && base.Kind == marotte.ACPUpdateAvailableCommands {
		rt.applySlashCommands(env.Update)
	}
}

// answerEarly answers a request a new host session's door sent before its run's lane was known.
func (rt *Runtime) answerEarly(s engineSession, msg *marotte.RPCResponse) {
	if !rt.answerConnectionRequest(s, msg) {
		rt.answerUnknownSession(s, msg)
	}
}

// observe runs fn on an observer pass carrying the profile's current presets and additional directories
// (engineOwner.observe).
func (rt *Runtime) observe(ctx context.Context, fn func(ctx context.Context, s engineSession) error) error {
	door := &marotte.SessionOpts{
		Presets:               securityPresets(ctx, rt.lifecycle.configDir),
		AdditionalDirectories: rt.lifecycle.sessionDirs(ctx),
	}
	return rt.engine.observe(ctx, door, rt.observerForward, fn)
}

// sessionIDOfParams is a frame's params.sessionId, "" when absent.
func sessionIDOfParams(params json.RawMessage) marotte.SessionID {
	var p struct {
		SessionID marotte.SessionID `json:"sessionId"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.SessionID
}
