package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	// Before the engine can ask: its sign-in state in memory dies with the previous engine, as KAS's own does.
	rt.inbound.secrets.ForgetSignInState()
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
	if msg == nil || rt.peerHeld(msg) {
		return
	}
	if msg.Method == methodKiroSessionNotify && rt.hostPeerTarget(h, msg) {
		return
	}
	if msg.ID != nil && msg.Method != "" {
		rt.engineRequest(h, msg)
		return
	}
	rt.engineNotification(msg)
}

// A peer message (KAS's send_message between two chats, `sender: "peer"`) to a chat marotte holds no session for
// is delivered all the same: KAS loads the target on demand and runs the turn it wakes inside the engine, so the
// notify and that turn's frames reach the engine lane. hostPeerTarget makes the target's chat resident and replays
// on its lane what the hold kept meanwhile.
type peerHolds struct {
	by map[marotte.SessionID]*peerHold
	mu sync.Mutex
}

// peerHold is one target session's frames, oldest first. chat and origin are set once the chat is resident, after
// which a frame still queued on the engine lane goes straight to it.
type peerHold struct {
	origin acpResponder
	chat   marotte.ChatID
	frames []*marotte.RPCResponse
}

// peerHoldCap bounds one hold; a frame past it takes the engine lane's own answer, as with no hold.
const peerHoldCap = 1024

// begin opens session's hold with the notify, false when one is already open (the notify then joins it).
func (p *peerHolds) begin(session marotte.SessionID, msg *marotte.RPCResponse) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h, ok := p.by[session]; ok {
		if h.chat == "" && len(h.frames) < peerHoldCap {
			h.frames = append(h.frames, msg)
		}
		return false
	}
	if p.by == nil {
		p.by = make(map[marotte.SessionID]*peerHold)
	}
	p.by[session] = &peerHold{frames: []*marotte.RPCResponse{msg}}
	return true
}

// take holds msg when session's hold is still waiting, or answers where a settled hold forwards it.
func (p *peerHolds) take(session marotte.SessionID, msg *marotte.RPCResponse) (h peerHold, held bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, ok := p.by[session]
	switch {
	case !ok:
		return peerHold{}, false
	case cur.chat != "":
		return peerHold{chat: cur.chat, origin: cur.origin}, true
	case len(cur.frames) >= peerHoldCap:
		return peerHold{}, false
	}
	cur.frames = append(cur.frames, msg)
	return peerHold{}, true
}

// drain hands back the frames held so far; once none are left the hold forwards to chat.
func (p *peerHolds) drain(session marotte.SessionID, chat marotte.ChatID, origin acpResponder) []*marotte.RPCResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.by[session]
	if h == nil {
		return nil
	}
	frames := h.frames
	h.frames = nil
	if len(frames) == 0 {
		h.chat, h.origin = chat, origin
	}
	return frames
}

// drop closes session's hold and hands back what it kept.
func (p *peerHolds) drop(session marotte.SessionID) []*marotte.RPCResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.by[session]
	delete(p.by, session)
	if h == nil {
		return nil
	}
	return h.frames
}

func (p *peerHolds) empty() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.by) == 0
}

// peerHeld takes a frame for a session a peer hold covers: kept while its chat is made resident, then handed to the
// chat's lane until the chat closes that session.
func (rt *Runtime) peerHeld(msg *marotte.RPCResponse) bool {
	if rt.peerHolds.empty() {
		return false
	}
	session := sessionIDOfParams(msg.Params)
	if session == "" {
		return false
	}
	fwd, held := rt.peerHolds.take(session, msg)
	if !held || fwd.chat == "" {
		return held
	}
	if rt.coord.ParentACPSession(fwd.chat) != string(session) {
		rt.peerHolds.drop(session)
		return false
	}
	rt.translateACPEvent(fwd.chat, fwd.origin, msg)
	return true
}

// hostPeerTarget opens a hold for a peer notify whose target no resident chat holds and makes that chat resident on
// a tracked goroutine, reporting whether the frame was taken.
func (rt *Runtime) hostPeerTarget(h engineHandle, msg *marotte.RPCResponse) bool {
	var p struct {
		SessionID marotte.SessionID `json:"sessionId"`
		Sender    string            `json:"sender"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.Sender != "peer" || p.SessionID == "" {
		return false
	}
	if _, resident := rt.coord.ChatForACPSession(string(p.SessionID)); resident {
		return false
	}
	if rt.peerHolds.begin(p.SessionID, msg) {
		rt.lifecycle.inflight.Go(func() { rt.residePeerTarget(h, p.SessionID) })
	}
	return true
}

// residePeerTarget opens the chat that records session and replays the held frames on its lane, in order. A chat
// that cannot be opened on that session drops them: each held request is answered as an unknown session's.
func (rt *Runtime) residePeerTarget(h engineHandle, session marotte.SessionID) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()
	chatID, sb, err := rt.peerTargetChat(ctx, session)
	if err != nil {
		slog.Warn("engine: a chat another chat messaged could not be opened, so the turn the message woke is not shown",
			"session_id", logsafe.Field(string(session)), "error", err)
		for _, f := range rt.peerHolds.drop(session) {
			if f.ID != nil && f.Method != "" {
				rt.answerUnknownSession(h, f)
			}
		}
		return
	}
	origin := sb.responder()
	for frames := rt.peerHolds.drain(session, chatID, origin); len(frames) > 0; frames = rt.peerHolds.drain(session, chatID, origin) {
		for _, f := range frames {
			rt.translateACPEvent(chatID, origin, f)
		}
	}
}

// peerTargetChat opens the chat whose record names session, which must come up on that session: openBridge can
// fall back to a new one, and a chat that moved past it no longer owns the woken turn.
func (rt *Runtime) peerTargetChat(ctx context.Context, session marotte.SessionID) (marotte.ChatID, *sharedBridge, error) {
	chatID, _ := rt.runs.chatForSession(ctx, string(session))
	if chatID == "" {
		return "", nil, errPeerTargetUnknown
	}
	sb, err := rt.coord.openBridge(ctx, chatID, "")
	if err != nil {
		return "", nil, err
	}
	if got := sb.SessionID(); got != session {
		return "", nil, fmt.Errorf("%w: chat %s opened on session %s", errPeerTargetMoved, chatID, got)
	}
	return chatID, sb, nil
}

var (
	errPeerTargetUnknown = errors.New("no chat records the session")
	errPeerTargetMoved   = errors.New("the chat is no longer on the session the message reached")
)

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
	case methodKiroSecretGet, methodKiroSecretStore, methodKiroSecretDelete:
		rt.answerUnlessDraining(origin, msg, func() {
			ctx, cancel := rt.lifecycle.derivedContext()
			defer cancel()
			rt.inbound.handleKiroSecretRequest(ctx, "", origin, msg)
		})
		return true
	case methodKiroOpenExternalURL:
		rt.answerUnlessDraining(origin, msg, func() {
			ctx, cancel := rt.lifecycle.derivedContext()
			defer cancel()
			rt.inbound.respondKiroOpenExternalURL(ctx, origin, msg)
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
	rt.translateACPEvent(host.chatID, host.sb.responder(), msg)
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
	case methodKiroMCPOAuthCompleted:
		var p struct {
			Server  string `json:"serverName"`
			FlowID  string `json:"oauthFlowId"`
			Success bool   `json:"success"`
		}
		if json.Unmarshal(msg.Params, &p) == nil {
			rt.mcpRegistry.endLazySignIn(ctx, p.FlowID, p.Server, p.Success)
		}
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
	env, base, ok := translate.DecodeSessionUpdate(msg.Params)
	if !ok {
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
func (rt *Runtime) observerForward(s engineSession) error {
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
	return nil
}

func (rt *Runtime) observerUpdate(msg *marotte.RPCResponse) {
	env, base, ok := translate.DecodeSessionUpdate(msg.Params)
	if ok && base.Kind == marotte.ACPUpdateAvailableCommands {
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
	s := rt.readSessionSettings(ctx)
	door := &marotte.SessionOpts{Presets: s.Presets, AdditionalDirectories: s.AdditionalDirectories}
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
