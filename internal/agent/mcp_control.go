package agent

// Live MCP control over KAS's _kiro/mcp/*: reconnect fans out to every live bridge (each has its
// own pool). _kiro/mcp/toggle is not wired: it is a global notification with no serverName.

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

const (
	// mcpReconnectTimeout bounds a bridge that never answers resetServer (bridge.Call has no timeout). KAS itself
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

// reconnectServer sends _kiro/mcp/resetServer to every live bridge concurrently and returns how
// many it targeted. Per-bridge errors are logged.
func (reg *mcpRegistry) reconnectServer(ctx context.Context, name string) int {
	bridges := reg.bridges.all()
	if len(bridges) == 0 {
		return 0
	}
	cctx, cancel := context.WithTimeout(ctx, mcpReconnectTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for chatID, sb := range bridges {
		wg.Go(func() {
			if _, err := sb.bridge.Call(cctx, methodV3MCPResetServer, map[string]any{keyServerName: name}); err != nil {
				slog.Warn("mcp reconnect: bridge call failed",
					"chat_id", chatID, "server", name, "error", err)
			}
		})
	}
	wg.Wait()
	return len(bridges)
}

type mcpReconnectReq struct {
	Server string `json:"server"`
}

// handleReconnect serves POST /api/mcp/reconnect {server}, returning {"reconnected": N} bridges targeted.
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
	n := reg.reconnectServer(r.Context(), body.Server)
	webhttp.WriteJSON(w, map[string]int{"reconnected": n})
}
