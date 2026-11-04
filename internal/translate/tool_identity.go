package translate

import (
	"maps"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// builtinToolByTitle maps KAS's display title back to the built-in tool's id, for the tools whose
// glyph their ACP kind does not decide. KAS stamps `_meta.kiro.toolId` on disclose_context and
// create_diagram alone, and titles every other built-in from its tool class's static title (or
// the bare tool id when the class has none), so the title is the one identity the frame carries.
// kascap's TestBuiltinToolTitlesMatchTheBundle pins each pair against the pinned agent server.
var builtinToolByTitle = map[string]string{
	"Run Workflow":             "run_workflow",
	"Inspect Workflow":         "inspect_workflow",
	"Update Workflow":          "update_workflow",
	"Save Workflow Definition": "save_workflow_definition",
	"Send Message":             "send_message",
	"Tool Load":                "tool_load",
	"Load Resource":            "load_resource",
	"web_search":               "web_search",
}

// BuiltinToolTitles returns a copy of the display-title-to-tool-id table the translator reads.
func BuiltinToolTitles() map[string]string {
	return maps.Clone(builtinToolByTitle)
}

// builtinToolName is the id of the built-in tool a create frame runs, "" when the frame names
// none the transcript distinguishes. An MCP call is never a built-in, whatever its title.
func builtinToolName(toolID, title string, mcp *marotte.MCPToolIdentity) string {
	if mcp != nil {
		return ""
	}
	if toolID != "" {
		return displayText(toolID)
	}
	return builtinToolByTitle[title]
}

// mcpCallIdentity is the MCP server and tool a create frame names, nil unless title is KAS's
// MCP title for serverName: `@<server>/<tool>`, or a Power route's `@<power>/<server>/<tool>`
// (tried first, so a Power named like its server parses right). The title supplies the tool
// half no _meta field carries, and a stray serverName on a built-in's frame names nothing.
func mcpCallIdentity(serverName, title string) *marotte.MCPToolIdentity {
	rest, ok := strings.CutPrefix(title, "@")
	if serverName == "" || !ok {
		return nil
	}
	var tool string
	if _, after, found := strings.Cut(rest, "/"+serverName+"/"); found {
		tool = after
	} else if after, found := strings.CutPrefix(rest, serverName+"/"); found {
		tool = after
	}
	server, tool := displayText(serverName), displayText(tool)
	if server == "" || tool == "" {
		return nil
	}
	return &marotte.MCPToolIdentity{ServerName: server, ToolName: tool}
}
