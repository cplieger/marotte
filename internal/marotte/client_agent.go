package marotte

// ClientCustomAgent is one `_meta.kiro.customAgents` entry on a session door: a custom agent KAS registers in that
// session's own scope. The keys are KAS's client agent schema, which strips a key it does not know.
type ClientCustomAgent struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
	// Tools is "*" for every tool: KAS reads an absent list as no tools.
	Tools         string   `json:"tools"`
	ExcludedTools []string `json:"excludedTools,omitempty"`
	// Permissions are the agent's whole policy: KAS adds only a context allow to a client agent's own rules. Zero
	// sends no block, since KAS's schema refuses a block without a rules array.
	Permissions    ClientAgentPermissions `json:"permissions,omitzero"`
	IncludeMCPJSON bool                   `json:"includeMcpJson"`
	IncludePowers  bool                   `json:"includePowers"`
}

// ClientAgentPermissions is a client custom agent's `permissions` block.
type ClientAgentPermissions struct {
	Rules []PolicyRuleCore `json:"rules"`
}

// SessionContextParams are one `_kiro/session/context` add or remove of a context file.
type SessionContextParams struct {
	SessionID  SessionID `json:"sessionId"`
	Subcommand string    `json:"subcommand"`
	Path       string    `json:"path"`
	// Force adds a path that does not exist yet: KAS skips a missing file at each prompt and loads it once it exists.
	Force bool `json:"force,omitempty"`
}

// The SessionContextParams subcommands marotte sends.
const (
	SessionContextAdd    = "add"
	SessionContextRemove = "remove"
)
