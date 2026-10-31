package agent

// Live MCP control over KAS's _kiro/mcp/*: reconnect is one engine call, since every session shares the engine's
// pool. _kiro/mcp/toggle is not wired: it is a global notification with no serverName.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
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
