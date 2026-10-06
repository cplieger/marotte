package translate

// v3 _kiro/* notification handlers whose payloads are reshaped from their v2 _kiro.dev/*
// counterparts. Shape-compatible ones reuse the v2 handlers through the dispatch table.

import (
	"context"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// unstatedFailureReason stands in for an errorMessage KAS left empty: every surface
// appends the reason to a lead, so an empty one renders a bare colon.
const unstatedFailureReason = "the server did not report a reason"

// v3MCPStatus is the _kiro/mcp/status payload: one list keyed by a status enum, replacing
// v2's per-server notifications.
type v3MCPStatus struct {
	// Present only while the organization sets an MCP registry; "registry" mode drops every
	// server that is not a registry entry.
	AccessMode      string             `json:"accessMode"`
	Servers         []v3MCPServer      `json:"servers"`
	Filtered        []string           `json:"accessModeFilteredServers"`
	Unresolved      []string           `json:"unresolvedRegistryServers"`
	RegistryServers []v3RegistryServer `json:"registryServers"`
}

// v3RegistryServer is one entry of the organization's MCP catalog; Enabled reports
// whether it is running.
type v3RegistryServer struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// mcpAccessModeRegistry is the access mode under which KAS runs registry entries only.
const mcpAccessModeRegistry = "registry"

// registry projects the status's registry fields; nil outside registry mode.
func (s *v3MCPStatus) registry() *marotte.GovernanceMCPRegistry {
	if s.AccessMode != mcpAccessModeRegistry {
		return nil
	}
	r := &marotte.GovernanceMCPRegistry{
		Servers:    make([]marotte.GovernanceRegistryServer, 0, len(s.RegistryServers)),
		Filtered:   nonNilStrings(s.Filtered),
		Unresolved: nonNilStrings(s.Unresolved),
	}
	for _, rs := range s.RegistryServers {
		if rs.Name == "" {
			continue
		}
		r.Servers = append(r.Servers, marotte.GovernanceRegistryServer{
			Name:        rs.Name,
			Version:     displayText(rs.Version),
			Description: displayText(rs.Description),
			Enabled:     rs.Enabled,
		})
	}
	return r
}

func nonNilStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// credentialsRejectedReason is the reason for a server KAS marks failedAuthorization with
// no authorization URL: the remedy is editing its headers or token, not signing in.
const credentialsRejectedReason = "the server rejected its credentials (check its headers or token)" //nolint:gosec // G101: a failure sentence, not a credential

// v3MCPServer is one entry in the _kiro/mcp/status servers list. connected carries the
// tool, prompt and resource lists; failed carries errorMessage, failedAuthorization and,
// for OAuth sign-in, authorizationUrl. KAS reports its internal "unavailable" as failed.
type v3MCPServer struct {
	Meta             v3MCPServerMeta `json:"_meta"`
	Name             string          `json:"name"`
	Status           string          `json:"status"`
	ErrorMessage     string          `json:"errorMessage"`
	AuthorizationURL string          `json:"authorizationUrl"`
	Tools            []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Prompts             []v3MCPPrompt           `json:"prompts"`
	Resources           []v3MCPResource         `json:"resources"`
	ResourceTemplates   []v3MCPResourceTemplate `json:"resourceTemplates"`
	FailedAuthorization bool                    `json:"failedAuthorization"`
}

// v3MCPServerMeta is an entry's _meta.kiro.resource.source provenance stamp.
type v3MCPServerMeta struct {
	Kiro struct {
		Resource struct {
			Source struct {
				Origin string `json:"origin"`
				Root   string `json:"root"`
				Power  struct {
					Name string `json:"name"`
				} `json:"power"`
			} `json:"source"`
		} `json:"resource"`
	} `json:"kiro"`
}

// source projects the provenance stamp onto the recorder's type.
func (s *v3MCPServer) source() marotte.MCPSource {
	src := s.Meta.Kiro.Resource.Source
	return marotte.MCPSource{Origin: src.Origin, Root: src.Root, Power: src.Power.Name}
}

// failureReason is the reason recorded for a failed entry with no
// authorization URL.
func (s *v3MCPServer) failureReason() string {
	msg := strings.TrimSpace(s.ErrorMessage)
	if s.FailedAuthorization {
		if msg == "" || strings.EqualFold(msg, "unauthorized") {
			return credentialsRejectedReason
		}
		return credentialsRejectedReason + ": " + msg
	}
	if msg == "" {
		return unstatedFailureReason
	}
	return s.ErrorMessage
}

// v3MCPPrompt mirrors one prompt entry in a connected server's status. promptName is the
// machine id (passed to _kiro/mcp/getPrompt); name is the display title.
type v3MCPPrompt struct {
	Name        string `json:"name"`
	PromptName  string `json:"promptName"`
	Description string `json:"description"`
	Arguments   []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Required    bool   `json:"required"`
	} `json:"arguments"`
}

// v3MCPResource mirrors one resource entry in a connected server's status.
type v3MCPResource struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

type v3MCPResourceTemplate struct {
	Name        string `json:"name"`
	URITemplate string `json:"uriTemplate"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// HandleMCPStatus maps _kiro/mcp/status onto the MCP-registry state: connected servers
// record their tools, failed ones an init failure, or an OAuth prompt when a URL is present.
func (t *Translator) HandleMCPStatus(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3MCPStatus](msg, "mcp/status")
	if !ok {
		return
	}
	t.governance.SetMCPRegistry(ctx, p.registry())
	for i := range p.Servers {
		s := &p.Servers[i]
		if s.Name == "" {
			continue
		}
		src := s.source()
		switch s.Status {
		case "connected":
			t.mcp.RecordConnected(ctx, s.Name, src, mcpToolNames(s.Tools), mcpPrompts(s.Prompts),
				mcpResources(s.Resources), mcpResourceTemplates(s.ResourceTemplates))
		case "failed":
			// The URL outranks failedAuthorization: an OAuth server needing sign-in carries both.
			if s.AuthorizationURL != "" {
				t.mcp.RecordOAuth(ctx, s.Name, src, s.AuthorizationURL)
				continue
			}
			t.mcp.RecordInitFailure(ctx, s.Name, src, s.failureReason())
		case "disabled":
			// The recorder drops this for marotte's own server (its config row renders the off
			// state); for every other origin this frame is the only evidence the server exists.
			t.mcp.RecordDisabled(ctx, s.Name, src)
		default:
			// connecting is transient: the next frame for this server replaces it.
		}
	}
}

// MCPPoolServer is what one connected server offers the `#` menu.
type MCPPoolServer struct {
	Name              string
	Resources         []marotte.MCPResourceInfo
	ResourceTemplates []marotte.MCPResourceTemplateInfo
}

// ReadMCPPool decodes a _kiro/mcp/status frame into the connected servers offering
// resources or templates. Each frame lists every server, so the result replaces the pool
// whole; ok is false only for a frame that does not decode.
func ReadMCPPool(msg *marotte.RPCResponse) (servers []MCPPoolServer, ok bool) {
	p, err := decodeParams[v3MCPStatus](msg)
	if err != nil {
		return nil, false
	}
	for i := range p.Servers {
		s := &p.Servers[i]
		if s.Name == "" || s.Status != "connected" {
			continue
		}
		resources, templates := mcpResources(s.Resources), mcpResourceTemplates(s.ResourceTemplates)
		if len(resources) == 0 && len(templates) == 0 {
			continue
		}
		servers = append(servers, MCPPoolServer{Name: s.Name, Resources: resources, ResourceTemplates: templates})
	}
	return servers, true
}

// mcpToolNames extracts the tool-name list from a v3 MCP server entry.
func mcpToolNames(tools []struct {
	Name string `json:"name"`
},
) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	return names
}

// mcpPrompts maps the wire prompt entries to the marotte discovery type,
// dropping entries with no machine promptName (unaddressable).
func mcpPrompts(in []v3MCPPrompt) []marotte.MCPPromptInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]marotte.MCPPromptInfo, 0, len(in))
	for _, p := range in {
		if p.PromptName == "" {
			continue
		}
		info := marotte.MCPPromptInfo{Name: p.Name, PromptName: p.PromptName, Description: p.Description}
		for _, a := range p.Arguments {
			if a.Name == "" {
				continue
			}
			info.Arguments = append(info.Arguments, marotte.MCPPromptArg{Name: a.Name, Description: a.Description, Required: a.Required})
		}
		out = append(out, info)
	}
	return out
}

// mcpResources maps the wire resource entries to the marotte discovery type,
// dropping entries with no uri (unaddressable).
func mcpResources(in []v3MCPResource) []marotte.MCPResourceInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]marotte.MCPResourceInfo, 0, len(in))
	for _, res := range in {
		if res.URI == "" {
			continue
		}
		out = append(out, marotte.MCPResourceInfo{Name: res.Name, URI: res.URI, Description: res.Description, MimeType: res.MimeType})
	}
	return out
}

func mcpResourceTemplates(in []v3MCPResourceTemplate) []marotte.MCPResourceTemplateInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]marotte.MCPResourceTemplateInfo, 0, len(in))
	for _, res := range in {
		if res.URITemplate == "" {
			continue
		}
		out = append(out, marotte.MCPResourceTemplateInfo{Name: res.Name, URITemplate: res.URITemplate, Description: res.Description, MimeType: res.MimeType})
	}
	return out
}

// _kiro/sessions/changed has no consumer: on v3 subagents are tool calls, not sessions.
// The runtime dispatch noops it.
