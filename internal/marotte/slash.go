package marotte

// SlashCommandKind is what KAS does with a leading `/name` before the model sees
// the text. Only kinds KAS resolves deterministically are listed; a skill or a
// custom agent reaches the model as prose, so the catalog drops them.
type SlashCommandKind string

// The kinds the slash menu lists.
const (
	// SlashKindSteering activates a manual steering document for one turn.
	SlashKindSteering SlashCommandKind = "steering"
	// SlashKindPrompt expands a saved prompt file or an MCP prompt.
	SlashKindPrompt SlashCommandKind = "prompt"
	// SlashKindGoal is KAS's /goal parser, which starts a goal run.
	SlashKindGoal SlashCommandKind = "goal"
)

// SlashArgument is one declared argument of a saved prompt.
type SlashArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// SlashCommand is one row of the slash menu. Name is KAS's normalised command
// name, the spelling a user must type for KAS to resolve it.
type SlashCommand struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Kind        SlashCommandKind `json:"kind"`
	// Hint is KAS's argument hint, already formatted ("<arg> [opt]").
	Hint      string          `json:"hint,omitempty"`
	Arguments []SlashArgument `json:"arguments,omitempty"`
}

// SlashCommandsResponse is GET /api/slash-commands's reply. Ready is false until
// KAS has sent a catalog, so an empty Commands then means "not known yet" rather
// than "none".
type SlashCommandsResponse struct {
	Commands []SlashCommand `json:"commands"`
	Ready    bool           `json:"ready"`
}

// SlashCommandsChangedPayload is the empty payload of type="slash_commands_changed":
// the client refetches GET /api/slash-commands.
type SlashCommandsChangedPayload struct{}

// SteeringIssue is one configuration problem KAS found in a steering document.
type SteeringIssue struct {
	Code        string   `json:"code"`
	Reference   string   `json:"reference,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Remediation string   `json:"remediation"`
	Patterns    []string `json:"patterns,omitempty"`
}

// SteeringIssuesResponse is GET /api/steering/issues's reply, keyed by the
// document's path in KiroDoc.Path's spelling (absolute, leading "/" dropped).
type SteeringIssuesResponse struct {
	Issues map[string][]SteeringIssue `json:"issues"`
}

// SteeringIssuesChangedPayload is the empty payload of type="steering_issues_changed".
type SteeringIssuesChangedPayload struct{}
