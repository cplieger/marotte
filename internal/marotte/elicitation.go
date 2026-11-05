package marotte

// MCP elicitation: KAS relays an MCP server's elicitation to us as _kiro/mcp/elicitation; we render a
// form from RequestedSchema, or a link in URL mode, and reply with an ElicitationResult ({action,
// content}).

import "encoding/json"

// ElicitationPropertySchema is one field of MCP 2025-11-25's restricted primitive schema
// (https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation): a string,
// number, integer or boolean; a single-select string with Enum (EnumNames its legacy labels)
// or titled OneOf options; or a multi-select array whose Items list the choices. Type is the
// discriminator and unset fields are omitted on the wire.
type ElicitationPropertySchema struct {
	Default     json.RawMessage         `json:"default,omitempty"`
	MinLength   *int                    `json:"minLength,omitempty"`
	MaxLength   *int                    `json:"maxLength,omitempty"`
	Minimum     *float64                `json:"minimum,omitempty"`
	Maximum     *float64                `json:"maximum,omitempty"`
	MinItems    *int                    `json:"minItems,omitempty"`
	MaxItems    *int                    `json:"maxItems,omitempty"`
	Items       *ElicitationItemsSchema `json:"items,omitempty"`
	Type        string                  `json:"type"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Format      string                  `json:"format,omitempty"`
	Pattern     string                  `json:"pattern,omitempty"`
	Enum        []string                `json:"enum,omitempty"`
	EnumNames   []string                `json:"enumNames,omitempty"`
	OneOf       []ElicitationEnumOption `json:"oneOf,omitempty"`
}

// ElicitationEnumOption is one titled choice: Const is the value answered, Title its label.
type ElicitationEnumOption struct {
	Const string `json:"const"`
	Title string `json:"title"`
}

// ElicitationItemsSchema lists a multi-select's choices, untitled in Enum or titled in AnyOf.
type ElicitationItemsSchema struct {
	Type  string                  `json:"type,omitempty"`
	Enum  []string                `json:"enum,omitempty"`
	AnyOf []ElicitationEnumOption `json:"anyOf,omitempty"`
}

// ElicitationRequestSchema is the JSON-schema-shaped object describing a
// form: a map of property name to its schema, plus the required set.
type ElicitationRequestSchema struct {
	Type        string                               `json:"type,omitempty"`
	Title       string                               `json:"title,omitempty"`
	Description string                               `json:"description,omitempty"`
	Properties  map[string]ElicitationPropertySchema `json:"properties,omitempty"`
	Required    []string                             `json:"required,omitempty"`
}

// ElicitationNeededPayload is the SSE payload for type="elicitation_needed".
// Mirrors PermissionNeededPayload's role: it carries everything the
// client needs to render the dialog plus the RequestID to echo back in
// the elicitation_response command. Mode is "form" or "url"; URL-mode
// elicitations carry URL and no RequestedSchema.
type ElicitationNeededPayload struct {
	RequestedSchema *ElicitationRequestSchema `json:"requested_schema,omitempty"`
	// MCPTool is the asking server and tool, read from the turn's tool call ToolCallID names;
	// nil when no open MCP call has that id.
	MCPTool    *MCPToolIdentity `json:"mcp_tool,omitempty"`
	Mode       string           `json:"mode,omitempty"`
	Message    string           `json:"message,omitempty"`
	URL        string           `json:"url,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	// RunID/NodeID: as on PermissionNeededPayload — a workflow step's ask,
	// attributed to its run and node from the step-session registry.
	RunID     string `json:"run_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	RequestID int64  `json:"request_id"`
}

// ElicitationModeURL is a link the user opens; MCP's other mode, "form", is built from RequestedSchema.
const ElicitationModeURL = "url"

// Elicitation actions per the MCP ElicitResult contract.
const (
	ElicitationActionAccept  = "accept"
	ElicitationActionDecline = "decline"
	ElicitationActionCancel  = "cancel"
)

// ElicitationResult is our answer to a _kiro/mcp/elicitation request, which
// KAS hands back to the MCP server.
// Action is one of accept/decline/cancel; Content carries the filled
// form values (object) only on accept.
type ElicitationResult struct {
	Content map[string]any `json:"content,omitempty"`
	Action  string         `json:"action"`
}
