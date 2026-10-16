package marotte

import "time"

// ConfigStanding is one non-active item of the workspace's composed configuration. Key is the Docs row's Path for a
// file-backed item (steering, agent, workflow) and the server name for an MCP server; an item with neither has no
// row to join. Kind is kiro-cli's reason (`displaced`, `disabled`), kept as it came.
type ConfigStanding struct {
	Type          string `json:"type"`
	Key           string `json:"key"`
	Kind          string `json:"kind"`
	DecidedBy     string `json:"decided_by"`
	DecidedByName string `json:"decided_by_name"`
	Compose       string `json:"compose,omitempty"`
	Setting       string `json:"setting,omitempty"`
}

// AgentLoadError is the last load error kiro-cli reported for an agent file.
type AgentLoadError struct {
	At         time.Time `json:"at"`
	ReasonCode string    `json:"reason_code"`
	Error      string    `json:"error"`
}

// ConfigStandingResponse is GET /api/config/standing's answer. LoadErrors is keyed by the Docs row's Path, and is
// shown whether or not the row also has a standing.
type ConfigStandingResponse struct {
	LoadErrors map[string]AgentLoadError `json:"load_errors"`
	Standing   []ConfigStanding          `json:"standing"`
}
