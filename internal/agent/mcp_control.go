package agent

// Live MCP control over KAS's _kiro/mcp/*: reconnect fans out to every live bridge (each has its
// own pool); getPrompt/getResource read one bridge's pool. _kiro/mcp/toggle is not wired: it is
// a global notification with no serverName.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

const (
	// mcpReconnectTimeout bounds one bridge's resetServer (may include an OAuth redirect); bridge.Call has no timeout.
	mcpReconnectTimeout = 90 * time.Second
	// mcpFetchTimeout bounds a getPrompt / getResource round-trip.
	mcpFetchTimeout = 30 * time.Second
)

var errNoLiveBridge = errors.New("no active chat session")

// keyServerName is the _kiro/mcp/* params key naming the server.
const keyServerName = "serverName"

const keyExitCode = "exitCode"

// firstLiveBridge returns one live bridge, or nil; a read answers for its pool.
func (reg *mcpRegistry) firstLiveBridge() *sharedBridge {
	for _, sb := range reg.bridges.all() {
		return sb
	}
	return nil
}

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

// args is always an object.
func (reg *mcpRegistry) promptFor(ctx context.Context, server, promptName string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	return reg.fetch(ctx, methodV3MCPGetPrompt, map[string]any{
		keyServerName: server,
		"promptName":  promptName,
		"arguments":   args,
	})
}

func (reg *mcpRegistry) resourceFor(ctx context.Context, server, uri string) (json.RawMessage, error) {
	return reg.fetch(ctx, methodV3MCPGetResource, map[string]any{
		keyServerName: server,
		"uri":         uri,
	})
}

func (reg *mcpRegistry) fetch(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	sb := reg.firstLiveBridge()
	if sb == nil {
		return nil, errNoLiveBridge
	}
	cctx, cancel := context.WithTimeout(ctx, mcpFetchTimeout)
	defer cancel()
	resp, err := sb.bridge.Call(cctx, method, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("empty MCP response")
	}
	return resp.Result, nil
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

type mcpGetPromptReq struct {
	Arguments map[string]any `json:"arguments"`
	Server    string         `json:"server"`
	Prompt    string         `json:"prompt"`
}

func (reg *mcpRegistry) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpGetPromptReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if body.Prompt == "" {
		httpreply.BadRequest(w, "prompt required")
		return
	}
	if !reg.serverReachable(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	res, err := reg.promptFor(r.Context(), body.Server, body.Prompt, body.Arguments)
	if err != nil {
		writeFetchErr(w, err)
		return
	}
	writeMCPResult(w, res)
}

type mcpGetResourceReq struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

func (reg *mcpRegistry) handleResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpGetResourceReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if body.URI == "" {
		httpreply.BadRequest(w, "uri required")
		return
	}
	if !reg.serverReachable(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	res, err := reg.resourceFor(r.Context(), body.Server, body.URI)
	if err != nil {
		writeFetchErr(w, err)
		return
	}
	writeMCPResult(w, res)
}

// writeMCPResult writes the raw result verbatim, or {} when empty.
func writeMCPResult(w http.ResponseWriter, res json.RawMessage) {
	if len(res) == 0 {
		res = json.RawMessage("{}")
	}
	webhttp.WriteJSON(w, res)
}

func writeFetchErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoLiveBridge) {
		httpreply.Conflict(w, "no active chat session. Open a chat to use MCP prompts and resources")
		return
	}
	slog.Warn("mcp fetch failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("MCP server request failed"))
}
