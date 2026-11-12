package marotte

// MCP runtime types: the status rows and event payloads marotte derives from
// kiro-cli's _kiro/mcp/status reports.
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
// Payloads are globally scoped (no chat_id) because MCP state is shared across all
// chats within the same container.

// MCPServerStatus is one server's row of GET /api/mcp/status and of an mcp_status frame.
type MCPServerStatus struct {
	Name  string         `json:"name"`
	State MCPServerState `json:"state"`
	// Origin is never omitted: the client attributes a status to marotte's row only on "user".
	Origin      Origin `json:"origin"`
	OriginRoot  string `json:"origin_root,omitempty"`
	OriginPower string `json:"origin_power,omitempty"`
	// OAuthURL is the standing Sign in's consent URL, set only while signing_in.
	OAuthURL string `json:"oauth_url,omitempty"`
	// Error is the server's failure, or why kiro-cli refused the last Sign in while it waits for one.
	Error string `json:"error,omitempty"`
	// Tools is the connected server's tool names, for the per-tool deny editor's suggestions.
	Tools             []string                  `json:"tools,omitempty"`
	Prompts           []MCPPromptInfo           `json:"prompts,omitempty"`
	Resources         []MCPResourceInfo         `json:"resources,omitempty"`
	ResourceTemplates []MCPResourceTemplateInfo `json:"resource_templates,omitempty"`
	// Relayed stops a reload or second device offering the paste box for a spent code.
	Relayed bool `json:"relayed,omitempty"`
	// Shadows keeps the client from putting this status on marotte's config row.
	Shadows bool `json:"shadows,omitempty"`
}

// MCPStatusPayload is the payload for type="mcp_status": Server's row changed, stamped with the
// `mcp_status` version. A nil Status means the server left the status inventory.
type MCPStatusPayload struct {
	Status *MCPServerStatus `json:"status,omitempty"`
	Server string           `json:"server"`
}

// MCPStatusResponse is GET /api/mcp/status's reply, sorted by server name.
type MCPStatusResponse struct {
	// Subject is the `mcp_status` digest stamp with the hub epoch, read with the rows.
	Subject *SubjectStamp     `json:"subject,omitempty"`
	Servers []MCPServerStatus `json:"servers"`
}

// MCPPrewarmState is the phase of a prewarm install, for the servers one frame names.
type MCPPrewarmState string

// The phases of a prewarm install. A server that leaves an install in flight gets Done.
const (
	MCPPrewarmInstalling MCPPrewarmState = "installing"
	MCPPrewarmDone       MCPPrewarmState = "done"
	MCPPrewarmFailed     MCPPrewarmState = "failed"
)

// MCPPrewarmPayload is the payload for type="mcp_prewarm", emitted when a prewarm install starts,
// succeeds or fails, and when a server joins or leaves one in flight. State applies to exactly the
// servers ServerIDs names.
type MCPPrewarmPayload struct {
	Package   string          `json:"package"`
	State     MCPPrewarmState `json:"state"`
	ServerIDs []string        `json:"server_ids"`
}

// MCPSnapshotServer is one entry in a runtime-to-steering MCP registry
// snapshot. Defined here to keep the steering package decoupled from
// internal/agent.
type MCPSnapshotServer struct {
	Name string `json:"name"`
}

// Discovery: on v3 a connected server's prompts and resources arrive in _kiro/mcp/status beside its
// tools; the registry caches them per server and /api/mcp/status surfaces them. A resource is read
// through _kiro/mcp/getResource (KAS 2.12).

// MCPPromptInfo describes one prompt a connected MCP server advertises. Name is KAS's display
// title, the prompt's title when it has one, else its name.
type MCPPromptInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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
// "needs_auth", "signing_in", "failed" or "disabled", declared where produced (the runtime's mcpRegistry).
// "disabled" is recorded only for a server marotte did NOT configure; a configured one's off state
// is its config row's `enabled: false`.
type MCPServerState string
