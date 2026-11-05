package translate

// v3 _kiro/* notification handlers whose payloads are reshaped from their v2 _kiro.dev/*
// counterparts. Shape-compatible ones reuse the v2 handlers through the dispatch table.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// unstatedFailureReason stands in for an errorMessage KAS left empty: every surface
// appends the reason to a lead, so an empty one renders a bare colon.
const unstatedFailureReason = "the server did not report a reason"

// v3MCPStatus is the _kiro/mcp/status payload: one list keyed by a status enum, replacing
// v2's per-server notifications.
type v3MCPStatus struct {
	SessionID marotte.SessionID `json:"sessionId"`
	// Present only while the organization sets an MCP registry; "registry" mode drops every
	// server that is not a registry entry.
	AccessMode      string             `json:"accessMode"`
	Servers         []v3MCPServer      `json:"servers"`
	Filtered        []string           `json:"accessModeFilteredServers"`
	Unresolved      []string           `json:"unresolvedRegistryServers"`
	RegistryServers []v3RegistryServer `json:"registryServers"`
}

// Enabled reports whether it is running.
type v3RegistryServer struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// mcpAccessModeRegistry is the access mode under which KAS runs registry entries only.
const mcpAccessModeRegistry = "registry"

// nil outside registry mode.
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

func (s *v3MCPServer) source() marotte.MCPSource {
	src := s.Meta.Kiro.Resource.Source
	return marotte.MCPSource{Origin: src.Origin, Root: src.Root, Power: src.Power.Name}
}

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

// v3MCPPrompt mirrors one prompt entry in a connected server's status. promptName is the MCP
// prompt's own name; name is the display title.
type v3MCPPrompt struct {
	Name        string `json:"name"`
	PromptName  string `json:"promptName"`
	Description string `json:"description"`
}

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

// MCPReportState is how one server stands in its session's _kiro/mcp/status frame.
type MCPReportState uint8

const (
	// MCPReportConnecting is transient: the session's previous entry for the server stands.
	MCPReportConnecting MCPReportState = iota
	// MCPReportConnected is a running server, reported with the four lists it offers.
	MCPReportConnected
	// MCPReportNeedsAuth is a failed server carrying an authorization URL.
	MCPReportNeedsAuth
	// MCPReportFailed is a server that failed for any other reason, carried in Error.
	MCPReportFailed
	// MCPReportDisabled is a server KAS reports switched off.
	MCPReportDisabled
)

// MCPServerReport is one server as its session's frame reports it. Error is set for
// MCPReportFailed, AuthorizationURL for MCPReportNeedsAuth, the four lists for MCPReportConnected.
type MCPServerReport struct {
	Name              string
	Source            marotte.MCPSource
	Error             string
	AuthorizationURL  string
	Tools             []string
	Prompts           []marotte.MCPPromptInfo
	Resources         []marotte.MCPResourceInfo
	ResourceTemplates []marotte.MCPResourceTemplateInfo
	State             MCPReportState
}

// MCPSessionStatus is one session's whole _kiro/mcp/status frame: every server the session
// binds, so a server it omits is no longer the session's.
type MCPSessionStatus struct {
	// Registry is nil outside registry mode.
	Registry *marotte.GovernanceMCPRegistry
	Servers  []MCPServerReport
}

// HandleMCPStatus hands a session's _kiro/mcp/status frame to the recorder whole. Under
// sessionScopedMcpStatus every frame names its session; one that does not is dropped.
func (t *Translator) HandleMCPStatus(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3MCPStatus](msg, "mcp/status")
	if !ok {
		return
	}
	if p.SessionID == "" {
		slog.Debug("mcp/status with no sessionId, dropped")
		return
	}
	status := MCPSessionStatus{Registry: p.registry(), Servers: make([]MCPServerReport, 0, len(p.Servers))}
	for i := range p.Servers {
		if r, ok := p.Servers[i].report(); ok {
			status.Servers = append(status.Servers, r)
		}
	}
	t.mcp.RecordSessionStatus(ctx, p.SessionID, status)
}

func (s *v3MCPServer) report() (MCPServerReport, bool) {
	if s.Name == "" {
		return MCPServerReport{}, false
	}
	r := MCPServerReport{Name: s.Name, Source: s.source()}
	switch s.Status {
	case "connected":
		r.State = MCPReportConnected
		r.Tools, r.Prompts = mcpToolNames(s.Tools), mcpPrompts(s.Prompts)
		r.Resources, r.ResourceTemplates = mcpResources(s.Resources), mcpResourceTemplates(s.ResourceTemplates)
	case "failed":
		// The URL outranks failedAuthorization: an OAuth server needing sign-in carries both.
		if s.AuthorizationURL != "" {
			r.State, r.AuthorizationURL = MCPReportNeedsAuth, s.AuthorizationURL
		} else {
			r.State, r.Error = MCPReportFailed, s.failureReason()
		}
	case "disabled":
		r.State = MCPReportDisabled
	default:
		r.State = MCPReportConnecting
	}
	return r, true
}

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

// mcpPrompts maps the wire prompt entries to the marotte discovery type, dropping entries
// with no promptName, which no MCP server can serve.
func mcpPrompts(in []v3MCPPrompt) []marotte.MCPPromptInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]marotte.MCPPromptInfo, 0, len(in))
	for _, p := range in {
		if p.PromptName == "" {
			continue
		}
		out = append(out, marotte.MCPPromptInfo{Name: p.Name, Description: p.Description})
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
