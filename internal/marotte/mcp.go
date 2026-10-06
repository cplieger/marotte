package marotte

// MCP runtime types: the event payloads broadcast to clients when kiro-cli
// reports MCP server state via its _kiro.dev/mcp/* notifications.
//
// The persisted config types live in internal/mcp.

// No MCPConfig interface: internal/agent declares mcpNameSets at its consumer. No inline ACP
// servers (KAS merges `client > file-based`, so an inline copy would outrank the rendered config
// file) and no SetKnownTools (tool names are runtime state, held in the runtime's registry).

// Origin records where a running MCP server came from, read off KAS's stamp on
// each _kiro/mcp/status entry (_meta.kiro.resource.source). It attributes live
// status to a row and grants nothing: whether a definition is editable is
// decided by marotte's own store, never by the wire.
type Origin string

// The provenance values a runtime MCP status row can carry. An OriginUser
// status belongs to marotte's config row for the name; every other value is a
// read-only row of its own.
const (
	// OriginUser is a server from marotte's own config, enabled or disabled.
	OriginUser Origin = "user"
	// OriginWorkspace is a server from a workspace's .kiro/settings/mcp.json,
	// which KAS lets override a same-named server in the user file.
	OriginWorkspace Origin = "workspace"
	// OriginPower is a server an installed Power contributed.
	OriginPower Origin = "power"
	// OriginBundled is a server Kiro itself ships.
	OriginBundled Origin = "bundled"
	// OriginUnknown is a server marotte cannot attribute: a user-level agent's
	// server, a powers entry KAS could not tie to a Power, a client or cloud
	// origin, or a value a later KAS adds.
	OriginUnknown Origin = "unknown"
)

// MCPSource is the provenance KAS stamps on one MCP status entry. An empty
// Origin means the entry carried no _meta (an agent-declared server whose
// agent has no stamped origin).
type MCPSource struct {
	// Origin is the wire value verbatim: user, workspace, power, bundled,
	// client, cloud, or one KAS adds later.
	Origin string
	// Root is the workspace folder, set when Origin is "workspace".
	Root string
	// Power is the Power's name, set when Origin is "power".
	Power string
}

// --- SSE payloads ---
//
// The runtime emits one event per MCP state transition. Payloads are
// globally scoped (no chat_id) because MCP state is shared across all
// chats within the same container.

// MCPConnectedPayload is the payload for type="mcp_connected", emitted
// when kiro-cli reports _kiro.dev/mcp/server_initialized.
type MCPConnectedPayload struct {
	Server string `json:"server"`
}

// MCPPrewarmPayload is the payload for type="mcp_prewarm", emitted
// when a prewarm install starts, succeeds, or fails. The UI uses this
// to show "Installing..." next to the server name in the MCP panel.
type MCPPrewarmPayload struct {
	Package string `json:"package"`
	State   string `json:"state"` // "installing", "done", "failed"
}

// MCPOAuthPayload is the payload for type="mcp_oauth_needed", emitted
// when kiro-cli reports _kiro.dev/mcp/oauth_request. URL is the
// provider's authorisation endpoint; the user completes the flow in a
// new tab.
type MCPOAuthPayload struct {
	Server string `json:"server"`
	URL    string `json:"url"`
}

// MCPFailedPayload is the payload for type="mcp_failed", emitted
// when kiro-cli reports _kiro.dev/mcp/server_init_failure.
type MCPFailedPayload struct {
	Server string `json:"server"`
	Error  string `json:"error"`
}

// MCPDisconnectedPayload is the payload for type="mcp_disconnected".
// Emitted when the runtime's last bridge exits: kiro-cli's MCP subprocesses
// shut down with it, so no configured server is currently live.
// Clients use this to clear their runtime-state map.
type MCPDisconnectedPayload struct {
	Server string `json:"server"`
}

// MCPSnapshotServer is one entry in a runtime-to-steering MCP registry
// snapshot. Defined here to keep the steering package decoupled from
// internal/agent.
type MCPSnapshotServer struct {
	Name string `json:"name"`
}

// Discovery: on v3 a connected server's prompts and resources arrive in _kiro/mcp/status beside its
// tools; the registry caches them per server and /api/mcp/status surfaces them, fetched via
// _kiro/mcp/getPrompt and getResource (KAS 2.12).

// MCPPromptArg describes one argument of an MCP prompt.
type MCPPromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// MCPPromptInfo describes one prompt a connected MCP server advertises.
// PromptName is the machine id passed to _kiro/mcp/getPrompt; Name is the
// human-readable display title (they differ — e.g. "Simple Prompt" vs
// "simple-prompt"). Arguments lists the prompt's parameters, if any.
type MCPPromptInfo struct {
	Name        string         `json:"name"`
	PromptName  string         `json:"prompt_name"`
	Description string         `json:"description,omitempty"`
	Arguments   []MCPPromptArg `json:"arguments,omitempty"`
}

// MCPResourceInfo describes one resource a connected MCP server advertises.
// URI is the identifier passed to _kiro/mcp/getResource.
type MCPResourceInfo struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

// MCPResourceTemplateInfo is one resource template a connected MCP server
// advertises; URITemplate is RFC 6570.
type MCPResourceTemplateInfo struct {
	Name        string `json:"name"`
	URITemplate string `json:"uri_template"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

// MCPServerState is the lifecycle status of one MCP server KAS reported: "idle", "connected",
// "needs_auth", "failed" or "disabled", declared where produced (the runtime's mcpRegistry).
// "disabled" is recorded only for a server marotte did NOT configure; a configured one's off state
// is its config row's `enabled: false`.
type MCPServerState string
