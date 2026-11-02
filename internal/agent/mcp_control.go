package agent

// Live MCP control over KAS's _kiro/mcp/*: reconnect and sign-in are one engine call each, since every session
// shares the engine's pool. _kiro/mcp/toggle is not wired: it is a global notification with no serverName.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/webhttp/v4"
)

const (
	// mcpReconnectTimeout bounds an engine that never answers resetServer (an engine call has no timeout). KAS itself
	// answers within 60 s whether or not the reset finished (kiro-cli 2.31.0 awaitBoundedReset), so the reply is
	// never the verdict: the server's state arrives on _kiro/mcp/status.
	mcpReconnectTimeout = 90 * time.Second
)

// keyServerName is the _kiro/mcp/* params key naming the server.
const keyServerName = "serverName"

const keyExitCode = "exitCode"

// keyStartOAuth asks resetServer for KAS's explicit sign-in: it discards the server's stored credentials, then
// waits on its redirect listener for the callback, the one attempt a pasted callback can complete.
const keyStartOAuth = "startOAuth"

// serverReachable reports whether live control may name this server: enabled in marotte's store,
// or reported running by KAS. A nil mcpConfig accepts any non-empty name.
func (reg *mcpRegistry) serverReachable(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	if reg.config == nil {
		return true
	}
	if _, ok := reg.config.EnabledNames(ctx)[name]; ok {
		return true
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	s, ok := reg.servers[name]
	return ok && s.State != mcpStateDisabled
}

// errMCPResetNoEngine is a reconnect asked while no engine is ready to take it.
var errMCPResetNoEngine = errors.New("the agent engine is not running")

// reconnectServer sends _kiro/mcp/resetServer to the engine: errMCPResetNoEngine while the engine is down, else the
// call's failure or refusal (logged).
func (reg *mcpRegistry) reconnectServer(ctx context.Context, name string) error {
	h, release, ok := reg.engine.tryDoor()
	if !ok {
		return errMCPResetNoEngine
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, mcpReconnectTimeout)
	defer cancel()
	resp, err := h.Call(cctx, methodV3MCPResetServer, map[string]any{keyServerName: name})
	if err == nil && resp != nil && resp.Error != nil {
		err = resp.Error
	}
	if err != nil {
		slog.Warn("mcp reconnect: the engine refused or did not answer the reset", "server", name, "error", err)
	}
	return err
}

type mcpReconnectReq struct {
	Server string `json:"server"`
}

// handleReconnect serves POST /api/mcp/reconnect {server}: 204 once the engine took the reset, 503 while no engine
// is running, 502 when it refused or did not answer.
func (reg *mcpRegistry) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpReconnectReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if !reg.serverReachable(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	switch err := reg.reconnectServer(r.Context(), body.Server); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errMCPResetNoEngine):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("The agent engine is not running. Try again once it is back."))
	default:
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("kiro-cli did not reset the server."))
	}
}

// Sign-in refusals: the server answers 409 with the text.
var (
	errSignInConnected  = errors.New("this server is already connected, so signing in again would only discard its credentials")
	errSignInInProgress = errors.New("a sign-in for this server is already in progress")
	errSignInNotNeeded  = errors.New("this server is not waiting for a sign-in")
	errSignInSlotTaken  = errors.New("another server's sign-in is in progress; finish it or let it time out, then try again")
)

// claimSignIn takes the engine's one sign-in slot for name, whose row must be waiting for sign-in: KAS's
// startOAuth discards every credential the server holds. An expired slot is taken over.
func (reg *mcpRegistry) claimSignIn(name string) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if s := reg.signIn; s.server != "" && reg.now().Before(s.deadline) {
		if s.server == name {
			return errSignInInProgress
		}
		return errSignInSlotTaken
	}
	switch rec := reg.servers[name]; {
	case rec == nil:
		return errSignInNotNeeded
	case rec.State == mcpStateConnected:
		return errSignInConnected
	case rec.State == mcpStateSigningIn:
		return errSignInInProgress
	case rec.State != mcpStateOAuth:
		return errSignInNotNeeded
	}
	reg.signIn = signInSlot{server: name, deadline: reg.now().Add(signInSlotTTL)}
	return nil
}

func (reg *mcpRegistry) releaseSignIn(name string) {
	reg.mu.Lock()
	if reg.signIn.server == name {
		reg.signIn = signInSlot{}
	}
	reg.mu.Unlock()
}

// claimSignInURL gives a consent URL KAS opens while the slot is held to the slot's server: its row turns
// signing_in with the URL as the relay's anchor. A lazy sign-in another server starts meanwhile is attributed to
// the slot's server too; the relay still delivers to the listener that URL names.
func (reg *mcpRegistry) claimSignInURL(ctx context.Context, url string) (string, bool) {
	reg.mu.Lock()
	slot := reg.signIn
	if slot.server == "" || !reg.now().Before(slot.deadline) {
		reg.mu.Unlock()
		return "", false
	}
	rec := &mcpServerRuntime{Name: slot.server, State: mcpStateSigningIn, Origin: marotte.OriginUser, OAuthURL: url}
	if prev := reg.servers[slot.server]; prev != nil {
		rec.Origin, rec.OriginRoot, rec.OriginPower, rec.Shadows = prev.Origin, prev.OriginRoot, prev.OriginPower, prev.Shadows
	}
	reg.servers[slot.server] = rec
	reg.mu.Unlock()

	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPSignInOpen, "", marotte.MCPSignInOpenPayload{Server: slot.server, URL: url}))
	reg.signalChange()
	return slot.server, true
}

// startSignIn claims the slot and sends resetServer {startOAuth} in the background: KAS answers within 60 s
// whatever the connect does, so the answer is no verdict, and the slot ends with the server's next status.
func (reg *mcpRegistry) startSignIn(name string) error {
	if err := reg.claimSignIn(name); err != nil {
		return err
	}
	started := reg.lifetime.goClaimed(func() func() {
		h, release, ok := reg.engine.tryDoor()
		if !ok {
			return nil
		}
		return func() {
			defer release()
			reg.sendSignIn(h, name)
		}
	})
	if !started {
		reg.releaseSignIn(name)
		return errMCPResetNoEngine
	}
	return nil
}

func (reg *mcpRegistry) sendSignIn(h engineHandle, name string) {
	ctx, cancel := context.WithTimeout(reg.lifetime.shutdownCtx, mcpReconnectTimeout)
	defer cancel()
	resp, err := h.Call(ctx, methodV3MCPResetServer, map[string]any{keyServerName: name, keyStartOAuth: true})
	if err == nil && resp != nil && resp.Error != nil {
		err = resp.Error
	}
	if err == nil {
		return
	}
	slog.Warn("mcp sign-in: the engine refused or did not answer the reset", "server", name, "error", err)
	reg.failSignIn(reg.lifetime.shutdownCtx, name, rpcerr.Text(err))
}

// failSignIn frees a slot the engine never took and says why on the row. A slot already freed (the engine ended,
// or the server reported) has its status, so nothing is recorded.
func (reg *mcpRegistry) failSignIn(ctx context.Context, name, reason string) {
	reg.mu.Lock()
	if reg.signIn.server != name {
		reg.mu.Unlock()
		return
	}
	reg.signIn = signInSlot{}
	var src marotte.MCPSource
	if prev := reg.servers[name]; prev != nil {
		src = marotte.MCPSource{Origin: string(prev.Origin), Root: prev.OriginRoot, Power: prev.OriginPower}
	}
	reg.mu.Unlock()
	reg.RecordInitFailure(ctx, name, src, reason)
}

// handleSignIn serves POST /api/mcp/sign-in {server}: 202 once the sign-in is queued, 404 for an unknown or disabled
// server, 409 when the server is not waiting for sign-in or a sign-in holds the slot, 503 while no engine runs.
func (reg *mcpRegistry) handleSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpReconnectReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if !reg.serverReachable(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	switch err := reg.startSignIn(body.Server); {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, errMCPResetNoEngine):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("The agent engine is not running. Try again once it is back."))
	default:
		httpreply.Conflict(w, err.Error())
	}
}
