package marotte

// HostShellType is the shell marotte reports to KAS, at the session door and in
// answer to a _kiro/terminal/shell_type probe. One constant so the two cannot
// disagree; internal/kascap's shellType row carries the same literal.
const HostShellType = "bash"

// ClientSteeringDoc is one entry of the session door's _meta.kiro.steering:
// a steering document KAS loads for that session only and persists nowhere.
// KAS's schema: name non-empty, inclusion always|fileMatch|manual, content at
// most 1,000,000 characters.
type ClientSteeringDoc struct {
	Name      string `json:"name"`
	Inclusion string `json:"inclusion"`
	Content   string `json:"content"`
}
