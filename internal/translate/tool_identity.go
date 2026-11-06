package translate

import (
	"encoding/json"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// webSearchTool is both a title and an id: KAS titles web_search by its bare id.
const webSearchTool = "web_search"

// builtinToolByTitle maps KAS's display title back to the built-in tool's id, for the tools whose
// glyph their ACP kind does not decide. KAS stamps `_meta.kiro.toolId` on disclose_context and
// create_diagram alone, and titles every other built-in from its tool class's static title (or
// the bare tool id when the class has none). kascap's TestBuiltinToolTitlesMatchTheBundle pins
// each pair against the pinned agent server.
var builtinToolByTitle = map[string]string{
	"Run Workflow":             "run_workflow",
	"Inspect Workflow":         "inspect_workflow",
	"Update Workflow":          "update_workflow",
	"Save Workflow Definition": "save_workflow_definition",
	"Send Message":             "send_message",
	"Tool Load":                "tool_load",
	"Load Resource":            "load_resource",
	webSearchTool:              webSearchTool,
}

// KAS's remote wrapper serves a tool Kiro hosts itself (web_search, the learnings tools) under the
// id remote_<name> and the MCP identity remote/<name>; it is never a user MCP integration, whose ids
// KAS builds as mcp_<server>_<tool>. kascap's TestRemoteWrapperIdentityMatchesTheBundle pins both.
const (
	remoteWrapperServer   = "remote"
	remoteWrapperIDPrefix = "remote_"
)

// remoteWrappedTool is the tool a remote-wrapper id serves, ok only for a non-empty name.
func remoteWrappedTool(toolID string) (string, bool) {
	name, ok := strings.CutPrefix(toolID, remoteWrapperIDPrefix)
	return name, ok && name != ""
}

// distinguishedBuiltins is every built-in id the transcript draws a glyph of its own for: the
// titled ones, and disclose_context, which KAS names by toolId alone.
var distinguishedBuiltins = func() map[string]bool {
	set := map[string]bool{"disclose_context": true}
	for _, id := range builtinToolByTitle {
		set[id] = true
	}
	return set
}()

// callIdentity names the tool a create frame runs: an MCP server and tool, or the id of a
// built-in the transcript distinguishes, or neither. An MCP call is never a built-in.
func callIdentity(tc *acpToolCallWire) (string, *marotte.MCPToolIdentity) {
	if mcp := mcpCallIdentity(tc.Meta.Kiro.ServerName, tc.Title); mcp != nil {
		return "", mcp
	}
	if name := builtinToolName(tc.Meta.Kiro.ToolID, tc.Title); name != "" {
		return name, nil
	}
	return envelopeIdentity(tc.RawInput)
}

// builtinToolName is the built-in tool KAS's toolId or display title names, "" for neither.
func builtinToolName(toolID, title string) string {
	if toolID != "" {
		if distinguishedBuiltins[toolID] {
			return toolID
		}
		return ""
	}
	return builtinToolByTitle[title]
}

// mcpCallIdentity is the MCP server and tool a create frame names, nil unless title is KAS's
// MCP title for serverName: `@<server>/<tool>`, or a Power route's `@<power>/<server>/<tool>`
// (tried first, so a Power named like its server parses right; a power name holds no `/`, so
// a tool name containing `/<server>/` keeps its head). The title supplies the tool half no
// _meta field carries, and a stray serverName on a built-in's frame names nothing.
func mcpCallIdentity(serverName, title string) *marotte.MCPToolIdentity {
	rest, ok := strings.CutPrefix(title, "@")
	if serverName == "" || !ok {
		return nil
	}
	var tool string
	if power, after, found := strings.Cut(rest, "/"+serverName+"/"); found && !strings.Contains(power, "/") {
		tool = after
	} else if after, found := strings.CutPrefix(rest, serverName+"/"); found {
		tool = after
	}
	return mcpIdentity(serverName, tool)
}

// envelopeIdentity reads the id a `tool_call` envelope carries in rawInput.tool_id. KAS titles
// an envelope whose tool it has not loaded "Tool Call", so this id is all such a frame names.
// The grammar is KAS's resource id: `tool::<builtin>`, `tool::<server>::<tool>`,
// `<power|plugin>::<name>::tool::<server>::<tool>`, and the older `builtin::<builtin>`.
func envelopeIdentity(rawInput json.RawMessage) (string, *marotte.MCPToolIdentity) {
	var in struct {
		ToolID string `json:"tool_id"`
	}
	if json.Unmarshal(rawInput, &in) != nil {
		return "", nil
	}
	if name, ok := strings.CutPrefix(in.ToolID, "builtin::"); ok {
		return envelopeBuiltin(name), nil
	}
	id, member := in.ToolID, false
	for _, kind := range [...]string{"power::", "plugin::"} {
		if rest, ok := strings.CutPrefix(id, kind); ok {
			_, id, member = strings.Cut(rest, "::")
			if !member {
				return "", nil
			}
			break
		}
	}
	rest, ok := strings.CutPrefix(id, "tool::")
	if !ok {
		return "", nil
	}
	server, tool, isMCP := strings.Cut(rest, "::")
	switch {
	case isMCP:
		return "", mcpIdentity(server, tool)
	case member:
		return "", nil
	default:
		return envelopeBuiltin(rest), nil
	}
}

func envelopeBuiltin(id string) string {
	if name, ok := remoteWrappedTool(id); ok {
		id = name
	}
	if !distinguishedBuiltins[id] {
		return ""
	}
	return id
}

func mcpIdentity(serverName, toolName string) *marotte.MCPToolIdentity {
	server, tool := displayText(serverName), displayText(toolName)
	if server == "" || tool == "" {
		return nil
	}
	return &marotte.MCPToolIdentity{ServerName: server, ToolName: tool}
}
