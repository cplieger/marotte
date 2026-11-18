package marotte

// CodeIndexStatusPayload is the payload for type="code_index_status" and one row of GET /api/code-index:
// kiro-cli's latest report on a workspace root's code index (_kiro/codeIntelligence/indexStatus). Build is
// the newest build kiro-cli knows for the root, nil when none ran since the engine started.
type CodeIndexStatusPayload struct {
	Build         *CodeIndexBuild `json:"build,omitempty"`
	WorkspaceRoot string          `json:"workspace_root"`
	Store         CodeIndexStore  `json:"store"`
}

// CodeIndexStore is the saved index. State is kiro-cli's none, ready or unreadable, kept as sent because
// its set is open; Summary counts a ready index.
type CodeIndexStore struct {
	Summary *CodeIndexSummary `json:"summary,omitempty"`
	State   string            `json:"state"`
}

// CodeIndexSummary is what a saved index holds.
type CodeIndexSummary struct {
	Files     int `json:"files"`
	Functions int `json:"functions"`
}

// CodeIndexBuild is one index build. State is kiro-cli's pending, running, succeeded, failed or canceled;
// Phase is scan, parse, graph or save, "" when kiro-cli names none. Both are kept as sent. Message is
// kiro-cli's progress line; Reason and Detail say why a build failed or was canceled.
type CodeIndexBuild struct {
	BuildID string `json:"build_id"`
	State   string `json:"state"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// CodeIndexView is GET /api/code-index: Root is the workspace root Build and Cancel act on, and Statuses
// is the latest status of every root kiro-cli reported, sorted by root.
type CodeIndexView struct {
	Root     string                   `json:"root"`
	Statuses []CodeIndexStatusPayload `json:"statuses"`
}
