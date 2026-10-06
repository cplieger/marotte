package translate

// kiro-cli's tool-interruption sentinel: on a security-filter cancel it emits ONE fixed text
// chunk and goes idle without answering session/prompt, holding the prompt slot. Matched
// exactly, as kiro-cli's own TUI does (crates/chat-cli-v2/src/agent/acp/acp_agent.rs, 2.19.0).

import "strings"

// interruptSentinel is the whole text of the chunk kiro-cli sends instead of ending the turn,
// kept beside its matcher as a foreign contract.
const interruptSentinel = "Tool uses were interrupted, waiting for the next user prompt"

// interruptReason is the transcript divider's attribution: which layer stopped the turn.
const interruptReason = "Stopped by kiro-cli's tool-use security filter"

// isInterruptSentinel reports whether one assistant text delta IS the sentinel: exact
// equality on the trimmed delta. A substring would fire on a model quoting it; a prefix
// (`Tool uses were` opens ordinary English) would cost unbounded false positives to catch a
// split delta never observed (steer_marker.go's carry is the tool if one appears). The TUI's
// second sentinel, the user-cancel one, is not matched: CmdCancel already ends that turn.
func isInterruptSentinel(text string) bool {
	return strings.TrimSpace(text) == interruptSentinel
}
